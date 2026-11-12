package builtin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fooexec/go2lean/internal/contract"
)

// Assessment preserves the exact card/source and the static compiler
// assessment together. It is diagnostic evidence, NOT an active property,
// resume record, or executable replay manifest.
type Assessment struct {
	Binding    *Binding    `json:"binding"`
	Capability *Capability `json:"capability"`
}

func WriteAssessment(w contract.Workspace, b *Binding, c *Capability) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	if c == nil || c.Builtin != b.ID || c.SourceSHA256 != b.SHA256 ||
		(c.Status != "unsupported" && c.Status != "static-model-eligible") ||
		(c.Status == "static-model-eligible") != (len(c.Issues) == 0) ||
		(c.Status == "static-model-eligible" && len(c.Functions) == 0) || c.CallFallbacks < 0 {
		return "", fmt.Errorf("compare-order: inconsistent static assessment")
	}
	dir := filepath.Join(w.Root, "builtins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := json.MarshalIndent(Assessment{Binding: b, Capability: c}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, b.ID+"-"+b.SHA256+".json")
	// Do not leave a truncated diagnostic as apparently complete evidence.
	tmp, err := os.CreateTemp(dir, ".assessment-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(src, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
