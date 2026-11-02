package builtin

import (
	"fmt"
	"strings"

	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/ir"
)

// FunctionRef identifies a function inspected by the static capability gate.
type FunctionRef struct {
	Package string `json:"package"`
	ID      int    `json:"id"`
	Name    string `json:"name"`
}

type CapabilityIssue struct {
	Package  string `json:"package,omitempty"`
	Function string `json:"function,omitempty"`
	FuncID   int    `json:"func_id"`
	Block    int    `json:"block"` // -1 when the issue concerns the function/card
	Code     string `json:"code"`
	Reason   string `json:"reason"`
}

// Capability is a static precondition for search, not a passed property,
// proof of model fidelity, or proof of the user's purity/domain assumptions.
type Capability struct {
	Builtin       string            `json:"builtin"`
	SourceSHA256  string            `json:"source_sha256"`
	Status        string            `json:"status"` // static-model-eligible | unsupported
	Functions     []FunctionRef     `json:"functions"`
	CallFallbacks int               `json:"call_fallbacks"`
	Issues        []CapabilityIssue `json:"issues,omitempty"`
}

// AssessCompareOrder checks the generated target, its oracle, the selected
// comparator and all direct callees in the compiler IR. It uses actual control
// flow/call terminators, not the reports' literal-mining Calls summaries.
//
// progs must be the compiler output produced with this binding's overlay.
// This API does not prove source/IR correspondence; pipeline integration must
// tie their snapshots together before enabling search.
//
// Synthetic call fallback blocks are not normal successors of a complete
// direct callee. They are counted, not declared impossible: a future search
// must separately account for and reject stuck/havoc/assumed trials.
func AssessCompareOrder(b *Binding, progs []*ir.Prog) *Capability {
	c := &Capability{Builtin: CompareOrderID, Status: "unsupported", Functions: []FunctionRef{}}
	issue := func(ref FunctionRef, block int, code, reason string) {
		c.Issues = append(c.Issues, CapabilityIssue{
			Package: ref.Package, Function: ref.Name, FuncID: ref.ID, Block: block,
			Code: code, Reason: reason,
		})
	}
	if err := b.Validate(); err != nil {
		issue(FunctionRef{}, -1, "invalid-binding", err.Error())
		return c
	}
	c.SourceSHA256 = b.SHA256
	packages := map[string]*ir.Prog{}
	for _, p := range progs {
		if p == nil || p.Package == "" {
			issue(FunctionRef{}, -1, "invalid-program", "nil program or missing package identity")
			continue
		}
		if _, exists := packages[p.Package]; exists {
			issue(FunctionRef{Package: p.Package}, -1, "ambiguous-program", "more than one program for this package")
			continue
		}
		packages[p.Package] = p
	}
	main := packages[b.Package]
	if main == nil {
		issue(FunctionRef{Package: b.Package}, -1, "missing-package", "bound package was not compiled")
		return c
	}
	var queue []FunctionRef
	for _, name := range []string{b.Target, b.Oracle, b.Function} {
		found := -1
		for id, f := range main.Funcs {
			if f != nil && f.Name == name {
				if found >= 0 {
					issue(FunctionRef{Package: b.Package, Name: name}, -1, "ambiguous-function", "duplicate function name")
				}
				found = id
			}
		}
		if found < 0 {
			issue(FunctionRef{Package: b.Package, Name: name}, -1, "missing-root", "required generated helper/comparator is missing")
			continue
		}
		queue = append(queue, FunctionRef{Package: b.Package, ID: found, Name: name})
		if name == b.Oracle && main.Funcs[found].SpecFor != b.Target {
			issue(queue[len(queue)-1], -1, "oracle-target-mismatch", "oracle is not annotated for the generated target")
		}
	}
	type key struct {
		pkg string
		id  int
	}
	seen := map[key]bool{}
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		k := key{ref.Package, ref.ID}
		if seen[k] {
			continue
		}
		seen[k] = true
		p := packages[ref.Package]
		if p == nil || ref.ID < 0 || ref.ID >= len(p.Funcs) || p.Funcs[ref.ID] == nil {
			issue(ref, -1, "missing-callee", "required direct callee/package has no model")
			continue
		}
		f := p.Funcs[ref.ID]
		ref.Name = f.Name
		c.Functions = append(c.Functions, ref)
		if p.Frontend != "gossa" {
			issue(ref, -1, "unsupported-frontend", "comparison-order requires the Go SSA frontend")
		}
		if f.Status != contract.ModelComplete {
			issue(ref, -1, "incomplete-model", fmt.Sprintf("model status is %q", f.Status))
		}
		for _, note := range f.Issues {
			issue(ref, -1, "reported-model-issue", note)
		}
		if f.Sync || f.Daemon {
			issue(ref, -1, "concurrency", "concurrent/stateful comparator execution is not supported")
		}
		blocks := []int{f.Entry}
		visited := map[int]bool{}
		for len(blocks) > 0 {
			bid := blocks[0]
			blocks = blocks[1:]
			if visited[bid] {
				continue
			}
			visited[bid] = true
			if bid < 0 || bid >= len(f.Blocks) {
				issue(ref, bid, "invalid-control-flow", "required block is missing")
				continue
			}
			blk := f.Blocks[bid]
			for _, in := range blk.Instrs {
				switch {
				case in.Assume != "":
					issue(ref, bid, "assumption", in.Assume)
				case in.Probe != "" && in.Op != nil:
					issue(ref, bid, "invalid-probe", "probe instruction also contains an operation")
				case in.Probe != "":
					// A probe observes a value, not a modeled target effect.
				case in.Op == nil:
					issue(ref, bid, "havoc", fmt.Sprintf("instruction reads havoc site %d", in.HavocSite))
				default:
					if reason := unsupportedOp(in.Op); reason != "" {
						issue(ref, bid, "unsupported-operation", reason)
					}
				}
			}
			t := blk.Term
			switch t.Kind {
			case ir.TermRet, ir.TermPanic:
			case ir.TermJump:
				blocks = append(blocks, t.B)
			case ir.TermBr:
				blocks = append(blocks, t.T, t.F)
			case ir.TermCall, ir.TermXCall:
				pkg := ref.Package
				if t.Kind == ir.TermXCall {
					pkg = t.CalleePkg
				}
				queue = append(queue, FunctionRef{Package: pkg, ID: t.Callee})
				blocks = append(blocks, t.Next)
				c.CallFallbacks++
				if t.Fallback < 0 || t.Fallback >= len(f.Blocks) {
					issue(ref, bid, "invalid-fallback", "direct call has no valid fallback block")
				}
			case ir.TermCallV:
				issue(ref, bid, "dynamic-call", "callee closure/interface value is not statically resolved by this gate")
				blocks = append(blocks, t.Next)
			case ir.TermSpawn, ir.TermSync:
				issue(ref, bid, "concurrency", "goroutine/synchronisation terminator is unsupported")
				blocks = append(blocks, t.Next)
			default:
				issue(ref, bid, "invalid-terminator", fmt.Sprintf("unknown or missing terminator %d", t.Kind))
			}
		}
	}
	if len(c.Issues) == 0 {
		c.Status = "static-model-eligible"
	}
	return c
}

// New IR op types and unstructured Raw terms require explicit review instead
// of silently becoming supported when the compiler grows a new operation.
func unsupportedOp(op ir.Op) string {
	switch v := op.(type) {
	case ir.Simple:
		if !reviewedSimpleOps[v] {
			return fmt.Sprintf("unreviewed or nondeterministic primitive %q", v)
		}
	case ir.Const, ir.Shl, ir.Shr, ir.Conv, ir.Load, ir.Extract,
		ir.AssertIface, ir.Dispatch, ir.FuncRef, ir.XFuncRef, ir.Box, ir.Unbox,
		ir.JSONUnmarshal, ir.JSONMarshal, ir.JSONEncode, ir.JSONDecode,
		ir.MapLookup, ir.MapNext, ir.Alloc, ir.FieldAddr, ir.Field,
		ir.MakeSlice, ir.Append, ir.StrConst, ir.ReFind, ir.StrReplacer,
		ir.ReFindIn, ir.ReIn, ir.ReflectKind, ir.ErrTagKnown:
	default:
		return fmt.Sprintf("unsupported IR operation %T", op)
	}
	return ""
}

// A known primitive is not a fidelity certificate: runtime stuck/assumption
// accounting and independent model/Go parity remain required.
const knownSimpleOps = `
add sub mul udiv sdiv urem srem and or xor andNot eq ne slt sle sgt sge
ult ule ugt uge neg bnot not len cap indexCheck reslice reslice3 mkTuple
loadPath storePath indexAddr arrIndex mkStruct copySlice isNil ite
strLen strIndex strSlice strConcat bytesToStr strToBytes strToRunes runesToStr
utf16Encode strCut strLastIndexByte runeToStr strNext strHasPrefix strHasSuffix
strContains strIndexOf strTrimSpace strTrimPrefix strTrimSuffix strAtoi strItoa
strParseInt strParseUint strParseBool strFormatInt strFormatUint eqDeep neDeep
hexEncode base64Decode base64Encode sprintf sprintfOk deepEqual deepEqualOk reCompileOk reflectOf reflectIsZero
errNew errKnown nilKnown isNilOpaque makeMap mapDelete mapLen mapRange mapUpdate
jsonValid strIndexByte strLastIndex strCount strEqualFold strTrim strTrimLeft
strTrimRight strToLower strToUpper strReplaceAll strSplit strFields strJoin
strRepeat sbWriteString sbWriteByte sbWriteRune sbString sbLen sbReset
`

var reviewedSimpleOps = func() map[ir.Simple]bool {
	out := map[ir.Simple]bool{}
	for _, op := range strings.Fields(knownSimpleOps) {
		out[ir.Simple(op)] = true
	}
	return out
}()
