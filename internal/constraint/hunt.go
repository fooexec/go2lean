package constraint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andreasfoo/go2lean/internal/builtin"
	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/prover"
	"github.com/andreasfoo/go2lean/internal/repo"
	"github.com/andreasfoo/go2lean/internal/ssaout"
)

// Engine selects which counterexample engines Hunt runs.
type Engine string

const (
	EngineRandom     Engine = "random"
	EngineExhaustive Engine = "exhaustive"
	// EngineAll runs random search first and, when it finds nothing, the
	// exhaustive engine so that a small domain can be certified.
	EngineAll Engine = "all"
)

// SearchOptions tune the engines.
type SearchOptions struct {
	Engine Engine
	Seed   int
	Trials int
	Fuel   int
	MaxLen int
	// ExhaustLen bounds slice lengths in the exhaustive engine; ExhaustLimit
	// bounds the domain size it will attempt.
	ExhaustLen   int
	ExhaustLimit int
	// NilInputs lets top-level pointer arguments and receivers be nil. Off by
	// default: "it panics on a nil receiver" is true of most Go code and
	// drowns the findings people act on. Nested pointers may still be nil.
	NilInputs bool
	// Jobs is how many properties HuntAll searches at once (default: the
	// number of CPUs); each search is its own process.
	Jobs int
	// BudgetSeconds bounds the wall-clock time of each engine on one property
	// (0: the default, 10; negative: none). A property with expensive inputs
	// then reports fewer trials instead of stalling the sweep.
	BudgetSeconds int
	// Prove tries a Lean proof over all inputs when the exhaustive engine
	// passed on a function of integer and bool parameters (internal/prover).
	Prove bool
	// ProveSeconds bounds each proof attempt (0: 120).
	ProveSeconds int
	// Mutate checks, for a spec that held, that it tells the code under
	// test from its mutants (see mutationCheck).
	Mutate bool
	// MaxMutants bounds the mutants tried per property (0: 24).
	MaxMutants int
}

func (o SearchOptions) withDefaults() SearchOptions {
	if o.Trials == 0 {
		o.Trials = 2000
	}
	if o.Fuel == 0 {
		o.Fuel = 1000
	}
	if o.Jobs <= 0 {
		o.Jobs = runtime.NumCPU()
	}
	if o.BudgetSeconds == 0 {
		o.BudgetSeconds = 10
	}
	if o.MaxLen == 0 {
		o.MaxLen = 8
	}
	if o.Engine == "" {
		o.Engine = EngineAll
	}
	if o.ExhaustLen == 0 {
		o.ExhaustLen = 3
	}
	if o.ExhaustLimit == 0 {
		o.ExhaustLimit = 300000
	}
	if o.MaxMutants == 0 {
		o.MaxMutants = 24
	}
	return o
}

// searchResult is the JSON line Go2Lean.Search.run prints.
type searchResult struct {
	Found       bool                       `json:"found"`
	Trial       int                        `json:"trial"`
	Seed        int                        `json:"seed"`
	Trials      int                        `json:"trials"`
	OutOfFuel   int                        `json:"out_of_fuel"`
	Moot        int                        `json:"moot"` // postcondition runs where the target panicked first
	Exhaustive  bool                       `json:"exhaustive"`
	Domain      float64                    `json:"domain"` // may exceed int64 when the domain is too large
	Reason      string                     `json:"reason"`
	MaxLen      int                        `json:"max_len"`
	HavocSites  int                        `json:"havoc_sites"`
	HavocRead   int                        `json:"havoc_read"`        // exhaustive inputs whose run read a havoc value
	Schedules   int                        `json:"schedules"`         // exhaustive runs under enumerated schedules (C3)
	SchedSample int                        `json:"schedules_sampled"` // inputs with more schedules than the engine runs
	BudgetHit   bool                       `json:"budget_hit"`
	StuckSites  map[string]int             `json:"stuck_sites"` // where the model got stuck, by count
	SpecExits   map[string]int             `json:"spec_exits"`  // spec return block -> trials that left through it
	Checked     int                        `json:"checked"`
	Inputs      map[string]string          `json:"inputs"`
	Memory      map[string][]string        `json:"memory"`
	Objects     map[string]json.RawMessage `json:"objects"`
	Havoc       []contract.HavocValue      `json:"havoc"`
	Relevant    []contract.HavocRead       `json:"relevant_havoc"`
	Perturbed   int                        `json:"havoc_perturbed"`
	Skipped     bool                       `json:"relevance_skipped"`
	ModelOutput *contract.ModelOutput      `json:"model_output"`
	Probes      []contract.Probe           `json:"probes"`
	PanicAt     string                     `json:"panic_at"`
	Assumptions []string                   `json:"assumptions"`
	Schedule    []contract.SchedStep       `json:"schedule"`
	// Init: the package initializers' slices run before the trials (#55).
	Init *initResult `json:"init"`

	// Out-of-fuel runs rerun with their state watched at back edges (#86):
	// how many, how many came back to a state (they loop forever), and where.
	LoopsChecked int            `json:"loops_checked"`
	LoopsForever int            `json:"loops_forever"`
	LoopSites    map[string]int `json:"loop_sites"`
	// Out-of-fuel runs whose fuel ran out inside one goroutine step, by
	// where the step started (a loop without a scheduling point).
	FuelInStep    int            `json:"out_of_fuel_in_step"`
	FuelStepSites map[string]int `json:"fuel_step_sites"`

	// The mutation engine's report (Mutation set).
	Mutation     bool                  `json:"mutation"`
	MutantsTotal int                   `json:"mutants_total"`
	Tried        int                   `json:"tried"`
	Killed       int                   `json:"killed"`
	KilledBy     []contract.MutantKill `json:"killed_by"`
	Survived     []string              `json:"survived"`
}

// gens renders the Search.Gen list for a function's parameters.
func gens(abi *contract.ABI, maxLen int) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, p := range abi.Params {
		g, err := gen(abi, p, maxLen)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func typeIndex(abi *contract.ABI, ref string) int {
	for i, t := range abi.Types {
		if t.GoType == ref {
			return i
		}
	}
	return -1
}

// typeTable renders the Search.TypeTable for the ABI's recursive types.
func typeTable(abi *contract.ABI, maxLen int) ([][]map[string]any, error) {
	entries := [][]map[string]any{}
	for _, t := range abi.Types {
		fs := []map[string]any{}
		for _, f := range t.Fields {
			g, err := genIn(abi, f, maxLen, []string{t.GoType})
			if err != nil {
				return nil, err
			}
			fs = append(fs, g)
		}
		entries = append(entries, fs)
	}
	return entries, nil
}

// gen describes the Search.Gen for one parameter as JSON; the shape is
// decoded by `genOfJson` in lean/Go2Lean/Driver.lean.
func gen(abi *contract.ABI, p contract.ABIParam, maxLen int) (map[string]any, error) {
	g, err := genIn(abi, p, maxLen, nil)
	if err == nil && p.Kind == contract.ABIOpaque && p.GoType == "context.Context" {
		g["v"] = "nonnil" // M3 passes context.Background() for a context parameter
	}
	return g, err
}

// genIn is gen along a path of the struct types under construction: a
// pointer to one of them is a back-edge of a recursive type (`link`),
// nullable at every depth, where any other pointer is non-nil by default.
func genIn(abi *contract.ABI, p contract.ABIParam, maxLen int, open []string) (map[string]any, error) {
	switch p.Kind {
	case contract.ABIInt:
		w, signed, ok := p.IntInfo()
		if !ok {
			return nil, fmt.Errorf("parameter %s: unsupported int type %s", p.Go, p.GoType)
		}
		return map[string]any{"k": "int", "name": p.Go, "w": w, "signed": signed}, nil
	case contract.ABIBool:
		return map[string]any{"k": "bool", "name": p.Go}, nil
	case contract.ABIError:
		return map[string]any{"k": "err", "name": p.Go}, nil
	case contract.ABISlice:
		if p.ElemParam != nil {
			e, err := genIn(abi, *p.ElemParam, maxLen, open)
			if err != nil {
				return nil, err
			}
			return map[string]any{"k": "sliceOf", "name": p.Go, "elem": e, "maxLen": maxLen}, nil
		}
		w, signed, ok := p.ElemInfo()
		if !ok {
			return nil, fmt.Errorf("parameter %s: unsupported element type %s", p.Go, p.Elem)
		}
		return map[string]any{"k": "slice", "name": p.Go, "w": w, "signed": signed, "maxLen": maxLen}, nil
	case contract.ABIStruct:
		if p.TypeRef != "" {
			ref := map[string]any{"k": "ref", "name": p.Go, "ty": typeIndex(abi, p.TypeRef)}
			for _, t := range abi.Types {
				if t.GoType == p.TypeRef && t.JSONDesc > 0 {
					// An SDK response value: generated, then decoded from
					// the document the harness builds for it.
					return map[string]any{"k": "sdkDoc", "name": p.Go, "desc": t.JSONDesc - 1, "inner": ref}, nil
				}
			}
			return ref, nil
		}
		fs := []map[string]any{}
		for _, f := range p.Fields {
			g, err := genIn(abi, f, maxLen, append(open, p.GoType))
			if err != nil {
				return nil, err
			}
			fs = append(fs, g)
		}
		return map[string]any{"k": "structv", "name": p.Go, "fields": fs}, nil
	case contract.ABIString:
		if p.MaxLen < 0 {
			return map[string]any{"k": "str", "name": p.Go, "maxLen": 0}, nil
		}
		if p.JSONText {
			return map[string]any{"k": "jsonText", "name": p.Go}, nil
		}
		g := map[string]any{"k": "str", "name": p.Go, "maxLen": maxLen}
		if len(p.Enum) > 0 {
			g["enum"] = p.Enum
		}
		return g, nil
	case contract.ABIOpaque:
		// M3 passes the zero value: nil for a func, map, channel or
		// interface (its ABI zero literal is "nil"), so the model may treat
		// the input as nil; other opaque values (floats, opaque structs)
		// stay unknown.
		v := "unknown"
		if p.Zero == "nil" {
			v = "nil"
		}
		g := map[string]any{"k": "opaque", "name": p.Go, "v": v}
		if p.ZeroModel != nil {
			// An unexported field of a foreign struct: always its zero.
			g["zero"] = p.ZeroModel
		}
		return g, nil
	case contract.ABIMap:
		k, err := genIn(abi, *p.Key, maxLen, open)
		if err != nil {
			return nil, err
		}
		v, err := genIn(abi, *p.Value, maxLen, open)
		if err != nil {
			return nil, err
		}
		return map[string]any{"k": "mapOf", "name": p.Go, "key": k, "val": v, "maxLen": maxLen}, nil
	case contract.ABIAny:
		alts := []map[string]any{}
		for _, a := range p.Alts {
			g, err := genIn(abi, a, maxLen, open)
			if err != nil {
				return nil, err
			}
			alts = append(alts, map[string]any{"tag": a.Tag, "gen": g})
		}
		return map[string]any{"k": "anyOf", "name": p.Go, "alts": alts}, nil
	case contract.ABIArray:
		e, err := genIn(abi, p.ElemAsParam(), maxLen, open)
		if err != nil {
			return nil, err
		}
		return map[string]any{"k": "arrOf", "name": p.Go, "elem": e, "n": p.Len}, nil
	case contract.ABIPtr:
		if p.Pointee == nil {
			// The frontend's depth cut-off for recursive types: always nil.
			return map[string]any{"k": "nilptr", "name": p.Go}, nil
		}
		g, err := genIn(abi, *p.Pointee, maxLen, open)
		if err != nil {
			return nil, err
		}
		kind := "ptr"
		if p.Pointee.TypeRef != "" && slices.Contains(open, p.Pointee.TypeRef) {
			kind = "link"
		}
		return map[string]any{"k": kind, "name": p.Go, "pointee": g}, nil
	}
	return nil, fmt.Errorf("parameter %s: unsupported kind %s", p.Go, p.Kind)
}

// pool is what the engines mine from a call tree: integer and string
// literals, the strings used as map keys and compared with ==, and whether
// some function decodes JSON.
type pool struct {
	consts, strConsts, mapKeys, cmpStrs, variants []string
	usesJSON                                      bool
}

// literalPool collects the literals of the given functions and,
// transitively, of the functions they call.
func literalPool(ws contract.Workspace, report *contract.ModelReport, roots []int) pool {
	var pl pool
	reports := map[string]*contract.ModelReport{report.Package: report}
	byID := map[contract.XCall]contract.ModelFunc{}
	index := func(r *contract.ModelReport) {
		for _, f := range r.Funcs {
			byID[contract.XCall{Package: r.Package, ID: f.ID}] = f
		}
	}
	index(report)
	seen := map[contract.XCall]bool{}
	add := func(into *[]string, from []string) {
		for _, s := range from {
			if !slices.Contains(*into, s) {
				*into = append(*into, s)
			}
		}
	}
	var walk func(x contract.XCall)
	walk = func(x contract.XCall) {
		if _, loaded := reports[x.Package]; !loaded {
			// A sibling package's report, when it was compiled alongside.
			r, err := ws.ModelReport(report.Frontend, x.Package)
			reports[x.Package] = r
			if err == nil {
				index(r)
			}
		}
		f, ok := byID[x]
		if !ok || seen[x] {
			return
		}
		seen[x] = true
		add(&pl.consts, f.Consts)
		add(&pl.strConsts, f.StrConsts)
		add(&pl.mapKeys, f.MapKeys)
		add(&pl.cmpStrs, f.CmpStrs)
		add(&pl.variants, f.Variants)
		pl.usesJSON = pl.usesJSON || f.UsesJSON
		for _, callee := range f.Calls {
			walk(contract.XCall{Package: x.Package, ID: callee})
		}
		for _, callee := range f.XCalls {
			walk(callee)
		}
	}
	for _, r := range roots {
		if r >= 0 {
			walk(contract.XCall{Package: report.Package, ID: r})
		}
	}
	return pl
}

// jsonTexts builds JSON documents from the literals of a call tree that
// decodes JSON: objects of one map key with a string literal, null, a bool
// or an empty string, plus the scalars and empty containers. Random bytes
// are almost never valid JSON, so without these a decoder only ever fails.
func jsonTexts(mapKeys, strConsts []string) []string {
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	vals := []string{"null", "true", `""`, "0", "1", "-1", "[]", "{}"}
	for i, s := range strConsts {
		if i == 8 {
			break
		}
		vals = append(vals, quote(s))
	}
	out := []string{"null", "{}", "[]", `""`, "true", "0"}
	for i, k := range mapKeys {
		if i == 8 {
			break
		}
		for _, v := range vals {
			out = append(out, "{"+quote(k)+":"+v+"}")
		}
	}
	for _, v := range vals[8:] {
		out = append(out, v)
	}
	return out
}

// jsonGens gives every JSON text generator its keys and string values.
func jsonGens(node any, keys, vals []string) {
	switch n := node.(type) {
	case map[string]any:
		if n["k"] == "jsonText" {
			n["keys"], n["vals"] = keys, vals
		}
		for _, v := range n {
			jsonGens(v, keys, vals)
		}
	case []map[string]any:
		for _, v := range n {
			jsonGens(v, keys, vals)
		}
	case [][]map[string]any:
		for _, v := range n {
			jsonGens(v, keys, vals)
		}
	case []any:
		for _, v := range n {
			jsonGens(v, keys, vals)
		}
	}
}

// keyMaps gives every generated map with string keys and no enum of its
// own the map keys of the call tree as its key enum.
// hotEnums weights the values of a documented enum that the call tree
// compares (item.Type == "reasoning"): repeated in the enum list until
// they make half of it, so a field with twenty documented values takes
// the two the code tests for half the time.
func hotEnums(node any, cmp []string) {
	switch n := node.(type) {
	case map[string]any:
		if enum, ok := n["enum"].([]string); ok && n["k"] == "str" {
			var hot []string
			for _, e := range enum {
				if slices.Contains(cmp, e) && !slices.Contains(hot, e) {
					hot = append(hot, e)
				}
			}
			if len(hot) > 0 && len(hot) < len(enum) {
				out := append([]string{}, enum...)
				for i := 0; i < len(enum); i++ {
					out = append(out, hot[i%len(hot)])
				}
				n["enum"] = out
			}
		}
		for _, v := range n {
			hotEnums(v, cmp)
		}
	case []map[string]any:
		for _, v := range n {
			hotEnums(v, cmp)
		}
	case [][]map[string]any:
		for _, v := range n {
			hotEnums(v, cmp)
		}
	}
}

// hotVariants marks the union variant fields the call tree reads
// (`hot`): a union of 28 variants whose code handles three would
// otherwise rarely hold one of those three.
func hotVariants(node any, hot []string) {
	switch n := node.(type) {
	case map[string]any:
		for _, v := range n {
			hotVariants(v, hot)
		}
	case []map[string]any:
		for i, v := range n {
			hotVariants(v, hot)
			if name, _ := v["name"].(string); v["k"] != "hot" && slices.Contains(hot, name) {
				n[i] = map[string]any{"k": "hot", "name": name, "inner": v}
			}
		}
	case [][]map[string]any:
		for _, v := range n {
			hotVariants(v, hot)
		}
	}
}

func keyMaps(node any, keys []string) {
	switch n := node.(type) {
	case map[string]any:
		if n["k"] == "mapOf" {
			if k, ok := n["key"].(map[string]any); ok && k["k"] == "str" && k["enum"] == nil && len(keys) > 0 {
				k["enum"] = keys
			}
		}
		for _, v := range n {
			keyMaps(v, keys)
		}
	case []map[string]any:
		for _, v := range n {
			keyMaps(v, keys)
		}
	case [][]map[string]any:
		for _, v := range n {
			keyMaps(v, keys)
		}
	case []any:
		for _, v := range n {
			keyMaps(v, keys)
		}
	}
}

// Hunt runs the selected engines on one property. It returns the
// counterexample it wrote to the workspace (nil when none was found) and a
// one-line summary. When the exhaustive engine covers its whole domain
// without a counterexample, a ProofResult with status "exhausted" is written
// ("exhausted_havoc" when some inputs read havoc values, "exhausted_assuming"
// when some runs took an under-approximation).
func Hunt(ws contract.Workspace, root string, prop *contract.Property, opts SearchOptions) (*contract.Counterexample, string, error) {
	if prop.MaxLen > 0 {
		opts.MaxLen = prop.MaxLen
	}
	opts = opts.withDefaults()
	target, err := ws.Target(prop.Target)
	if err != nil {
		return nil, "", err
	}
	if builtin.IsGeneratedProperty(prop, target) {
		if _, err := builtin.ValidateProperty(ws, prop); err != nil {
			return nil, "", err
		}
	}
	abi, err := ws.ABI(target, prop.Frontend)
	if err != nil {
		return nil, "", err
	}
	report, err := ws.ModelReport(prop.Frontend, target.Package)
	if err != nil {
		return nil, "", err
	}
	fid, sid := -1, -1
	for _, f := range report.Funcs {
		if f.Name == target.Func {
			fid = f.ID
		}
		if prop.Spec != nil && f.Name == prop.Spec.GoFunc {
			sid = f.ID
		}
	}
	// The literal pool: the target's and the spec's constants and those of
	// everything they call, so the branch a callee takes on "assistant" is
	// reachable from the caller's inputs.
	pl := literalPool(ws, report, []int{fid, sid})
	consts, strConsts, mapKeys := pl.consts, pl.strConsts, pl.mapKeys
	if pl.usesJSON {
		for _, s := range jsonTexts(mapKeys, strConsts) {
			if !slices.Contains(strConsts, s) {
				strConsts = append(strConsts, s)
			}
		}
	}
	var specExits []contract.ExitSite
	for _, f := range report.Funcs {
		if f.ID == sid && sid >= 0 {
			specExits = f.Exits
		}
	}
	if fid < 0 {
		return nil, "", fmt.Errorf("function %s not in model report", target.Func)
	}
	var leanProp map[string]any
	switch prop.Kind {
	case contract.KindNoPanic:
		leanProp = map[string]any{"kind": "noPanic", "f": fid}
	case contract.KindPostcondition:
		if sid < 0 {
			return nil, "", fmt.Errorf("spec %s not in model report", prop.Spec.GoFunc)
		}
		leanProp = map[string]any{"kind": "postcondition", "f": fid, "spec": sid}
	case contract.KindSequence:
		leanProp = map[string]any{"kind": "sequence", "f": fid}
	case contract.KindNoHang:
		leanProp = map[string]any{"kind": "noHang", "f": fid}
	default:
		return nil, "", fmt.Errorf("property kind %s has no search engine yet", prop.Kind)
	}
	g, err := gens(abi, opts.MaxLen)
	if err != nil {
		return nil, "", err
	}
	table, err := typeTable(abi, opts.MaxLen)
	if err != nil {
		return nil, "", err
	}
	keyMaps(g, mapKeys)
	keyMaps(table, mapKeys)
	// Generated JSON texts take their keys from the map keys and compared
	// strings, their string values from those and the other literals.
	jsonKeys := append(append([]string{}, mapKeys...), pl.cmpStrs...)
	jsonVals := append(append([]string{}, pl.cmpStrs...), strConsts...)
	if len(jsonVals) > 64 {
		jsonVals = jsonVals[:64]
	}
	jsonGens(g, jsonKeys, jsonVals)
	jsonGens(table, jsonKeys, jsonVals)
	hotVariants(g, pl.variants)
	hotVariants(table, pl.variants)
	hotEnums(g, pl.cmpStrs)
	hotEnums(table, pl.cmpStrs)

	extra := []json.Number{}
	for _, c := range consts {
		extra = append(extra, json.Number(c))
	}
	if strConsts == nil {
		strConsts = []string{}
	}
	leanDir := repo.WorkspaceLeanDir(ws.Root)
	core, err := repo.CoreDir(root)
	if err != nil {
		return nil, "", err
	}
	if err := ssaout.EnsureProject(leanDir, core); err != nil {
		return nil, "", err
	}
	if err := buildDriver(core, leanDir); err != nil {
		return nil, "", err
	}
	spec := map[string]any{
		"prog": strings.TrimPrefix(report.LeanModule, "Go2Lean.Generated."), "prop": leanProp,
		"gens": g, "extra": extra, "strs": strConsts, "table": table, "allowNil": opts.NilInputs,
		"seed": opts.Seed, "trials": opts.Trials, "fuel": opts.Fuel,
		"maxLen": opts.ExhaustLen, "limit": opts.ExhaustLimit,
		"budgetMs": max(opts.BudgetSeconds, 0) * 1000,
	}

	var summary []string
	if opts.Engine == EngineRandom || opts.Engine == EngineAll {
		res, err := runSearch(leanDir, prop.ID+"_random", spec, "random")
		if err != nil {
			return nil, "", err
		}
		if res.Found {
			cx, err := writeCx(ws, prop, res, "random", opts)
			return cx, fmt.Sprintf("random: found at trial %d", res.Trial), err
		}
		note := ""
		if res.BudgetHit {
			note = fmt.Sprintf(" [stopped by the %ds budget]", opts.BudgetSeconds)
		}
		summary = append(summary, fmt.Sprintf("random: none in %d trials (%d out of fuel, %d moot/stuck)%s%s%s%s%s", res.Trials, res.OutOfFuel, res.Moot, loopNote(res), exitNote(specExits, res.SpecExits), note, initNote(res.Init), stuckNote(res.StuckSites, report.Havoc, reportsByKey(ws, report.Frontend, res.StuckSites))))
	}
	if opts.Engine == EngineExhaustive || opts.Engine == EngineAll {
		if prop.Kind == contract.KindSequence {
			// Bounded model checking: every sequence up to the exhaustive
			// length over the program's own constants (narrowMark in
			// Search.lean), not over all boundary values.
			spec["extra"] = append(slices.Clone(extra), json.Number(narrowMark))
			spec["limit"] = max(opts.ExhaustLimit, seqExhaustLimit)
		}
		res, err := runSearch(leanDir, prop.ID+"_exhaustive", spec, "exhaustive")
		if err != nil {
			return nil, "", err
		}
		switch {
		case res.Found:
			cx, err := writeCx(ws, prop, res, "exhaustive", opts)
			return cx, fmt.Sprintf("exhaustive: found at input %d", res.Trial), err
		case res.Exhaustive:
			domain := fmt.Sprintf("%.0f inputs: boundary values, mined literals and |v|<=8, slices up to length %d, strings of a named type limited to its constants, \"\" and \"a\"", res.Domain, res.MaxLen)
			if prop.Kind == contract.KindSequence {
				domain = fmt.Sprintf("%.0f inputs: every sequence of up to %d events whose integers are the program's constants (with their neighbours), 0 and 1", res.Domain, res.MaxLen)
			}
			if prop.Kind == contract.KindPostcondition {
				_, checked := exitCounts(specExits, res.SpecExits)
				domain += fmt.Sprintf("; %d of them got past the spec's guards", checked)
			}
			if res.Schedules > 0 {
				domain += fmt.Sprintf("; every schedule of each (%d runs)", res.Schedules)
				if res.SchedSample > 0 {
					domain = strings.Replace(domain, "every schedule of each", fmt.Sprintf("every schedule of each, but %d inputs had too many and were sampled", res.SchedSample), 1)
				}
			}
			status := contract.ProofExhausted
			if res.HavocRead > 0 {
				// Havoc values are sampled, not enumerated: no certificate
				// for every value they stand for.
				status = contract.ProofExhaustedHavoc
				domain += fmt.Sprintf("; %d of them read havoc values (%d sites), three values tried each", res.HavocRead, res.HavocSites)
			}
			if len(res.Assumptions) > 0 {
				// The model left out behaviours Go has on these paths.
				status = contract.ProofExhaustedAssuming
				domain += fmt.Sprintf("; runs took %d under-approximations (see assumptions)", len(res.Assumptions))
			}
			pr := &contract.ProofResult{Property: prop.ID, Status: status, Fuel: opts.Fuel, Tactic: "exhaustive", Domain: domain, Assumptions: res.Assumptions}
			// A certificate for the sample; try one for every input. The
			// prover runs the sequential interpreter: not for a function that
			// synchronises (its schedules were enumerated instead). First the
			// bounded theorem by case split (integer and bool no_panic), then
			// the weakest precondition (Go2Lean.Proof.WP: specs and integer
			// slices too, any fuel, kernel only) for loop-free functions.
			if opts.Prove && status == contract.ProofExhausted && res.Schedules == 0 {
				var why string
				var p *contract.ProofResult
				if prop.Kind == contract.KindNoPanic && prover.Eligible(abi) {
					p, why = prove(leanDir, report, prop, abi, fid, sid, opts)
				}
				if p == nil && wpEligible(report, prop, abi, fid, sid) {
					p, why = proveWP(root, ws, leanDir, report, prop, abi, fid, sid, opts)
				}
				if p != nil {
					pr = p
				} else if why != "" {
					summary = append(summary, "prover: not proved ("+why+")")
				}
			}
			if err := ws.WriteProofResult(pr); err != nil {
				return nil, "", err
			}
			summary = append(summary, fmt.Sprintf("exhaustive: holds on %.0f inputs%s (recorded as %s)", res.Domain, exitNote(specExits, res.SpecExits), pr.Status))
		case res.Reason == "budget":
			summary = append(summary, fmt.Sprintf("exhaustive: stopped by the %ds budget after %d of %.0f inputs", opts.BudgetSeconds, res.Checked, res.Domain))
		case res.Reason == "signature too wide":
			summary = append(summary, fmt.Sprintf("exhaustive: skipped (%s: %.0f leaf generators > 24)", res.Reason, res.Domain))
		case res.Reason != "":
			summary = append(summary, fmt.Sprintf("exhaustive: skipped (%s: %.3g > %d)", res.Reason, res.Domain, opts.ExhaustLimit))
		case res.Moot > 0:
			summary = append(summary, fmt.Sprintf("exhaustive: no certificate, %d of %.0f inputs were moot (the model got stuck, or the target panicked before the oracle ran)", res.Moot, res.Domain))
		default:
			summary = append(summary, fmt.Sprintf("exhaustive: no certificate, %d inputs ran out of fuel%s", res.OutOfFuel, loopNote(res)))
		}
	}
	if opts.Mutate && (prop.Kind == contract.KindPostcondition || prop.Kind == contract.KindSequence) {
		note, err := mutationCheck(ws, leanDir, report, prop, spec, fid, sid, opts)
		if err != nil {
			return nil, "", err
		}
		if note != "" {
			summary = append(summary, note)
		}
	}
	return nil, strings.Join(summary, "; "), nil
}

// mutationCheck asks whether a spec that held tells the code under test
// from its mutants (a branch negated, a comparison or + / - swapped, in the
// target and the functions of its package it calls): the mutation engine
// searches each for an input on which the mutant fails the spec and the
// original passes it. A spec no mutant fails checks nothing a one-line
// change breaks: it is vacuous, or too weak to have found a bug there.
// Records the result and returns the summary note ("" when there is
// nothing to mutate).
func mutationCheck(ws contract.Workspace, leanDir string, report *contract.ModelReport, prop *contract.Property,
	spec map[string]any, fid, sid int, opts SearchOptions) (string, error) {
	root := fid
	if prop.Kind == contract.KindSequence {
		// A sequence spec drives its target; mutate the target.
		root = -1
		for _, f := range report.Funcs {
			if f.ID == fid {
				for _, g := range report.Funcs {
					if g.Name == f.SequenceFor {
						root = g.ID
					}
				}
			}
		}
		if root < 0 {
			return "", nil
		}
	}
	// The code under test: the root and what it calls, never the spec.
	var fids []int
	for _, id := range prover.Closure(report, root) {
		if id != sid && (id != fid || id == root) {
			fids = append(fids, id)
		}
	}
	ms := maps.Clone(spec)
	ms["mutate"] = fids
	ms["maxMutants"] = opts.MaxMutants
	ms["trials"] = min(opts.Trials, 500)
	res, err := runSearch(leanDir, prop.ID+"_mutate", ms, "mutate")
	if err != nil {
		return "", err
	}
	if !res.Mutation || res.Tried == 0 {
		return "", nil
	}
	if err := ws.WriteMutation(&contract.MutationResult{Property: prop.ID, Total: res.MutantsTotal, Tried: res.Tried,
		Killed: res.Killed, KilledBy: res.KilledBy, Survived: res.Survived}); err != nil {
		return "", err
	}
	if res.Killed == 0 {
		return fmt.Sprintf("mutation: no mutant of the code under test fails the spec (%d tried): it checks nothing a one-line change breaks", res.Tried), nil
	}
	return fmt.Sprintf("mutation: %d of %d mutants fail the spec", res.Killed, res.Tried), nil
}

// narrowMark is Search.lean's narrowMark (2^64 + 1): in the literal pool,
// it selects the narrow integer domain of the exhaustive engine.
const narrowMark = "18446744073709551617"

// seqExhaustLimit is the domain a sequence spec's exhaustive pass may
// enumerate: its runs are short, and covering every sequence up to the
// length is the point.
const seqExhaustLimit = 2_000_000

// prove writes and checks the theorem that the property holds for every
// input; nil and the reason when Lean did not prove it (the exhaustive
// result stands).
func prove(leanDir string, report *contract.ModelReport, prop *contract.Property, abi *contract.ABI,
	fid, sid int, opts SearchOptions) (*contract.ProofResult, string) {
	rel, err := prover.Theorem(leanDir, report, prop, abi, fid, sid, prover.Closure(report, fid, sid))
	if err != nil {
		return nil, err.Error()
	}
	budget := time.Duration(opts.ProveSeconds) * time.Second
	if budget <= 0 {
		budget = 120 * time.Second
	}
	r := prover.Check(leanDir, rel, budget)
	if !r.Proved {
		return nil, r.Error
	}
	return &contract.ProofResult{Property: prop.ID, Status: contract.ProofProved, Tactic: "simp, split, bv_decide",
		Domain:   fmt.Sprintf("every input (all %d parameters over their full width); runs within fuel %d, no havoc read, no assumption taken", len(abi.Params), prover.Fuel),
		LeanFile: filepath.Join("lean", rel), TimeMs: r.Time.Milliseconds(), Trust: r.Trust}, ""
}

// wpEligible says whether proveWP applies: a no_panic or spec property of a
// function whose parameters a statement covers and which, with its spec,
// has no loop and makes no call (wp_auto walks
// loop-free code; a loop needs an invariant, a call a contract).
func wpEligible(report *contract.ModelReport, prop *contract.Property, abi *contract.ABI, fid, sid int) bool {
	if prop.Kind != contract.KindNoPanic && prop.Kind != contract.KindPostcondition {
		return false
	}
	if prop.Kind == contract.KindPostcondition && sid < 0 {
		return false
	}
	for _, f := range report.Funcs {
		if (f.ID == fid || f.ID == sid) && (len(f.LoopHeaders) > 0 || len(f.Calls) > 0 || len(f.XCalls) > 0) {
			return false
		}
	}
	_, err := prover.Statement(abi, fid, "t", "True")
	return err == nil
}

// proveWP states the property as the function's contract (no panic, or the
// spec returns true) and proves it with wp_auto: for every input, every
// oracle and havoc value, and every run that ends, checked by the kernel
// alone. Lean's check of the file in the workspace is the first pass; the
// proof judge (prover.Judge) decides: it holds the theorems to the
// statements go2lean writes and replays them in Lean's kernel.
func proveWP(root string, ws contract.Workspace, leanDir string, report *contract.ModelReport, prop *contract.Property, abi *contract.ABI,
	fid, sid int, opts SearchOptions) (*contract.ProofResult, string) {
	g := prover.Goal{Func: abi.GoFunc, Name: "holds", Post: "True"}
	if prop.Kind == contract.KindPostcondition {
		post, err := prover.SpecPost(abi, report.LeanModule, sid)
		if err != nil {
			return nil, err.Error()
		}
		g.Post, g.Spec, g.SpecFid = post, prop.Spec.GoFunc, sid
	}
	// With the companions: the same property for nil slices and empty
	// strings, which the first theorem does not cover.
	goals := append([]prover.Goal{g}, prover.Companions(g, abi)...)
	var abis []*contract.ABI
	var fids []int
	var all []int
	for i := range goals {
		abis, fids, all = append(abis, abi), append(fids, fid), append(all, i)
	}
	rel := filepath.Join("Proofs", "WP_"+prop.ID+".lean")
	f, err := prover.FunctionalFileAt(leanDir, rel, report, goals, abis, fids)
	if err != nil {
		return nil, err.Error()
	}
	budget := time.Duration(opts.ProveSeconds) * time.Second
	if budget <= 0 {
		budget = 120 * time.Second
	}
	start := time.Now()
	// Lean's check in the workspace first: most attempts fail (a havoc
	// value wp_auto cannot reason about), and it costs less than the judge.
	rs, _, err := prover.CheckGoals(leanDir, report.LeanModule, f, goals, budget)
	if err != nil {
		return nil, err.Error()
	}
	for _, r := range rs {
		if !r.Proved {
			return nil, r.Theorem + ": " + r.Error
		}
	}
	dir := filepath.Join(ws.Root, "judge", fmt.Sprintf("%s-%d", prop.ID, time.Now().UnixNano()))
	certs, err := prover.Judge(prover.JudgeInput{Root: root, WSLean: leanDir, Dir: dir,
		Report: report, Goals: goals, ABIs: abis, Fids: fids, Candidates: all, Budget: 4 * budget})
	if err != nil {
		return nil, "judge: " + err.Error()
	}
	for i, g := range goals {
		if certs[i] == nil {
			return nil, prover.TheoremName(g) + ": not proved (" + prover.JudgeError(dir) + ")"
		}
	}
	domain := fmt.Sprintf("every input (all %d parameters; slices and strings of any length, nil and empty included, with capacity equal to length); every oracle and havoc value; every run that ends (partial correctness) and takes no under-approximation", len(abi.Params))
	if u := prover.Uncovered(abi); len(u) > 0 {
		domain = strings.Replace(domain, "nil and empty included", fmt.Sprintf("nil and empty not covered (%d slice and string parameters)", len(u)), 1)
	}
	return &contract.ProofResult{Property: prop.ID, Status: contract.ProofProved, Tactic: "wp_auto",
		Domain: domain, LeanFile: filepath.Join("lean", rel), TimeMs: time.Since(start).Milliseconds(),
		Trust: prover.TrustKernel, Certificate: certs[0]}, ""
}

// stuckNote names the most frequent site at which the model got stuck.
// initResult is the engine's report on the package initializers' slices.
type initResult struct {
	Ran        int    `json:"ran"`
	HavocReads int    `json:"havoc_reads"`
	Error      string `json:"error"`
}

// initNote says when package initializers' slices did not run or read
// havoc (#55): the trials then start without what those build (zero
// values), or with a fixed choice for what they read.
func initNote(r *initResult) string {
	if r == nil {
		return ""
	}
	note := ""
	if r.Error != "" {
		note += "; package initializers that did not run: " + r.Error
	}
	if r.HavocReads > 0 {
		note += fmt.Sprintf("; the package initializers read %d havoc values (fixed for every trial)", r.HavocReads)
	}
	return note
}

func stuckNote(sites map[string]int, havoc []contract.ABIHavoc, reports map[string]*contract.ModelReport) string {
	best, n := "", 0
	for s, c := range sites {
		if c > n || c == n && s < best {
			best, n = s, c
		}
	}
	if n == 0 {
		return ""
	}
	// An assumption is named by its havoc site; sites past the target's
	// own program belong to a linked package.
	if rest, ok := strings.CutPrefix(best, "assumption at havoc site "); ok {
		// "<model key>#<local index>" in a linked program, else a plain index.
		pkgKey, idx, linked := strings.Cut(rest, "#")
		if !linked {
			idx = pkgKey
		}
		if i, err := strconv.Atoi(idx); err == nil {
			switch {
			case linked && reports[pkgKey] != nil && i < len(reports[pkgKey].Havoc):
				best = "an assumed value: " + reports[pkgKey].Havoc[i].Site
			case !linked && i < len(havoc):
				best = "an assumed value: " + havoc[i].Site
			default:
				best = "an assumed value at " + rest
			}
		}
	}
	return fmt.Sprintf("; stuck mostly at %s (%d)", best, n)
}

// driverBuilt remembers that the search executable is up to date with the
// generated models; InvalidateDriver resets it after a compile.
var (
	driverMu    sync.Mutex
	driverBuilt string
)

// InvalidateDriver makes the next Hunt rebuild the search executable.
func InvalidateDriver() {
	driverMu.Lock()
	defer driverMu.Unlock()
	driverBuilt = ""
}

// buildDriver links a workspace's native search driver against its
// generated models once per compile: first go2lean's core library (shared
// by every workspace, so built under the core's lock), then the workspace's
// project (under its own lock: a round's parallel hunts share it).
func buildDriver(coreDir, leanDir string) error {
	driverMu.Lock()
	defer driverMu.Unlock()
	if driverBuilt == leanDir {
		return nil
	}
	if err := locked(coreDir, func() error {
		if out, err := lake(coreDir, "build", "Go2Lean"); err != nil {
			return fmt.Errorf("lake build Go2Lean (core): %w\n%s", err, out)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := locked(leanDir, func() error {
		if out, err := lake(leanDir, "build", "go2lean-search"); err != nil {
			return fmt.Errorf("lake build go2lean-search: %w\n%s", err, out)
		}
		return nil
	}); err != nil {
		return err
	}
	driverBuilt = leanDir
	return nil
}

// locked runs f holding an exclusive lock on dir's lake build: hunts run
// as parallel processes (round scripts use xargs -P), and a second lake
// build in the same project fails on the first's half-written outputs.
func locked(dir string, f func() error) error {
	if err := os.MkdirAll(filepath.Join(dir, ".lake"), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lake", "go2lean-build.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return f()
}

// runSearch writes the search description under the workspace's
// lean/Search and runs the
// native driver on it, parsing the JSON line it prints.
func runSearch(leanDir, name string, spec map[string]any, engine string) (*searchResult, error) {
	specFile := filepath.Join(leanDir, "Search", name+".json")
	spec["engine"] = engine
	src, err := json.MarshalIndent(spec, "", " ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(specFile), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(specFile, src, 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(leanDir, ".lake", "build", "bin", "go2lean-search"), specFile)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go2lean-search %s: %w\n%s", specFile, err, buf.String())
	}
	line := lastLine(buf.String())
	var res searchResult
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		return nil, fmt.Errorf("parsing search output %q: %w", line, err)
	}
	return &res, nil
}

func writeCx(ws contract.Workspace, prop *contract.Property, res *searchResult, engine string, opts SearchOptions) (*contract.Counterexample, error) {
	cx := &contract.Counterexample{
		ID:               "cx_" + strings.TrimPrefix(prop.ID, "p_") + "_" + engine + fmt.Sprintf("_s%d", opts.Seed),
		Property:         prop.ID,
		Frontend:         prop.Frontend,
		Engine:           engine,
		Fuel:             opts.Fuel,
		Inputs:           res.Inputs,
		Memory:           res.Memory,
		Objects:          res.Objects,
		Havoc:            res.Havoc,
		RelevantHavoc:    res.Relevant,
		HavocPerturbed:   res.Perturbed,
		RelevanceSkipped: res.Skipped,
		ModelOutput:      res.ModelOutput,
		// The reported values are the ones the model actually executed on.
		RevalidatedInLean: true,
		Probes:            res.Probes,
		PanicAt:           res.PanicAt,
		Assumptions:       res.Assumptions,
		Schedule:          res.Schedule,
	}
	return cx, ws.WriteCounterexample(cx)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// lake runs the lake binary, falling back to elan's default location.
func lake(dir string, args ...string) (string, error) {
	bin, err := exec.LookPath("lake")
	if err != nil {
		home, _ := os.UserHomeDir()
		bin = filepath.Join(home, ".elan", "bin", "lake")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

// exitCounts splits the trials that ran a spec to its end into those that
// left through a `return true` guard (vacuous) and the others (checked).
func exitCounts(sites []contract.ExitSite, exits map[string]int) (vacuous, checked int) {
	for b, n := range exits {
		vac := false
		for _, s := range sites {
			if strconv.Itoa(s.Block) == b && s.ConstTrue && !s.Last {
				vac = true
			}
		}
		if vac {
			vacuous += n
		} else {
			checked += n
		}
	}
	return vacuous, checked
}

// exitNote says how many trials got past the spec's guards, and through
// which return statements (by source line) they left.
// loopNote says how many of the out-of-fuel runs rerun loop forever, and
// at the most frequent loop (#86). A loop the run entered under an
// under-approximation may be the model's, not the program's.
func loopNote(res *searchResult) string {
	step := ""
	if res.FuelInStep > 0 {
		step = fmt.Sprintf("; %d of them inside one goroutine step (most from %s)", res.FuelInStep, mostSite(res.FuelStepSites))
	}
	if res.LoopsChecked == 0 {
		return step
	}
	if res.LoopsForever == 0 {
		return step + fmt.Sprintf("; none of %d out-of-fuel runs rerun comes back to a state", res.LoopsChecked)
	}
	return step + fmt.Sprintf("; %d of %d out-of-fuel runs rerun loop forever (most at %s)", res.LoopsForever, res.LoopsChecked, mostSite(res.LoopSites))
}

// mostSite is the site with the highest count (the first by name on a tie).
func mostSite(sites map[string]int) string {
	best, n := "", 0
	for s, c := range sites {
		if c > n || c == n && s < best {
			best, n = s, c
		}
	}
	return best
}

func exitNote(sites []contract.ExitSite, exits map[string]int) string {
	if len(exits) == 0 {
		return ""
	}
	vacuous, checked := exitCounts(sites, exits)
	var parts []string
	for _, s := range sites {
		n := exits[strconv.Itoa(s.Block)]
		if n == 0 {
			continue
		}
		label := fmt.Sprintf("line %d", s.Line)
		if s.ConstTrue && !s.Last {
			label += " (return true)"
		}
		parts = append(parts, fmt.Sprintf("%s: %d", label, n))
	}
	return fmt.Sprintf("; spec checked %d, vacuous %d [%s]", checked, vacuous, strings.Join(parts, ", "))
}

// reportsByKey loads the model reports of the linked packages a stuck
// site names ("<module key>#<index>"), keyed by module key.
func reportsByKey(ws contract.Workspace, frontend string, sites map[string]int) map[string]*contract.ModelReport {
	out := map[string]*contract.ModelReport{}
	for site := range sites {
		rest, ok := strings.CutPrefix(site, "assumption at havoc site ")
		if !ok {
			continue
		}
		key, _, linked := strings.Cut(rest, "#")
		if !linked || out[key] != nil {
			continue
		}
		if r, err := ws.ModelReportByKey(frontend, strings.TrimPrefix(key, frontend+".")); err == nil {
			out[key] = r
		}
	}
	return out
}
