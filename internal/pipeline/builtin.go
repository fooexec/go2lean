package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"

	"github.com/fooexec/go2lean/internal/builtin"
	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/ir"
)

// ConfigureCompareOrder explicitly selects the builtin role, enforces a
// dedicated default-build workspace, and prepares its canonical overlay.
// It supplies the law; the caller supplies the purity/totality/ordering intent.
func ConfigureCompareOrder(w contract.Workspace, env Env, selected string, patterns ...string) (Env, error) {
	if env.Tests || len(env.Overlay) != 0 {
		return env, fmt.Errorf("compare-order: test targets and additional source overlays are not supported in this mode")
	}
	root, err := filepath.Abs(env.module())
	if err != nil {
		return env, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return env, err
	}
	env.Module = root
	b, err := builtin.BindCompareOrder(root, selected, nil, patterns...)
	if err != nil {
		return env, err
	}
	rel, err := filepath.Rel(root, filepath.Dir(b.File))
	if err != nil {
		return env, err
	}
	sources, err := builtin.CaptureSources(root, filepath.ToSlash(rel))
	if err != nil {
		return env, err
	}
	active, err := builtin.ActiveRef(w)
	if err != nil {
		return env, err
	}
	if active != nil {
		if !env.Resume {
			return env, fmt.Errorf("compare-order: check workspace already exists; use -resume or a fresh workspace")
		}
		s, err := builtin.LoadSnapshot(w, active)
		if err != nil {
			return env, err
		}
		if s.Binding.SHA256 != b.SHA256 || !s.Sources.Equal(sources) {
			return env, fmt.Errorf("compare-order: selected binding, Go sources or configuration changed; use a fresh workspace")
		}
		env.compareRef = active
	} else {
		if err := builtin.RequireFreshWorkspace(w); err != nil {
			return env, err
		}
	}
	env.CompareOrder, env.compareSources = b, sources
	env.Overlay, env.Deps, env.FuzzSpecs = b.Overlay(), true, false
	return env, nil
}

func refuseActiveCompile(w contract.Workspace) error {
	ref, err := builtin.ActiveRef(w)
	if err != nil {
		return err
	}
	if ref != nil {
		return fmt.Errorf("builtin check workspace is immutable; use run -resume or compile in a fresh workspace")
	}
	return nil
}

func validateBuiltinHunt(w contract.Workspace, env Env, p *contract.Property) error {
	t, err := w.Target(p.Target)
	if err != nil {
		return err
	}
	if !builtin.IsGeneratedProperty(p, t) {
		return nil
	}
	s, err := builtin.ValidateProperty(w, p)
	if err != nil {
		return err
	}
	if env.Resume {
		_, err = s.RestoreOverlay(env.module())
	}
	return err
}

func activeProperties(w contract.Workspace, report *contract.ModelReport) ([]*contract.Property, bool, error) {
	ref, err := builtin.ActiveRef(w)
	if err != nil || ref == nil {
		return nil, false, err
	}
	s, err := builtin.LoadSnapshot(w, ref)
	if err != nil {
		return nil, true, err
	}
	if report.Package != s.Binding.Package {
		return nil, true, fmt.Errorf("builtin: package is not the selected snapshot target")
	}
	p := s.Property(ref)
	if err := w.WriteProperty(p); err != nil {
		return nil, true, err
	}
	return []*contract.Property{p}, true, nil
}

func validateCompareOrderOverlay(env Env) error {
	b := env.CompareOrder
	if b == nil {
		return nil
	}
	if err := b.Validate(); err != nil {
		return err
	}
	if env.compareSources != nil && len(env.Overlay) != 1 {
		return fmt.Errorf("compare-order: activated checks allow only their canonical helper overlay")
	}
	if src, ok := env.Overlay[b.File]; !ok || !bytes.Equal(src, []byte(b.Source)) {
		return fmt.Errorf("compare-order: compiler overlay does not contain the canonical bound source")
	}
	for path := range env.Overlay {
		if path != b.File && filepath.Clean(path) == b.File {
			return fmt.Errorf("compare-order: ambiguous generated overlay path %s", path)
		}
	}
	return nil
}

func assessCompareOrder(w contract.Workspace, env Env, progs []*ir.Prog, out io.Writer) error {
	if env.CompareOrder == nil {
		return nil
	}
	c := builtin.AssessCompareOrder(env.CompareOrder, progs)
	path, err := builtin.WriteAssessment(w, env.CompareOrder, c)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "compare-order: %s (%d required functions, %d call fallback sites); assessment: %s\n",
		c.Status, len(c.Functions), c.CallFallbacks, path)
	for _, issue := range c.Issues {
		fmt.Fprintf(out, "  %s.%s block %d: %s: %s\n",
			issue.Package, issue.Function, issue.Block, issue.Code, issue.Reason)
	}
	if c.Status != "static-model-eligible" {
		return fmt.Errorf("compare-order: required call graph is unsupported; see %s", path)
	}
	return nil
}
