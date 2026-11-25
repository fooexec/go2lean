//go:build !(llvm15 || llvm16 || llvm17 || llvm18 || llvm19 || llvm20 || llvm21 || llvm22 || llvm23)

package llvm

import "errors"

// Available says whether this binary was built with the LLVM frontend.
const Available = false

var errNoLLVM = errors.New("go2lean was built without the LLVM frontend: rebuild with -tags llvm18 (or the tag of your LLVM version)")

// Gen is how the search generates one parameter.
type Gen struct {
	K      string `json:"k"`
	Name   string `json:"name"`
	W      int    `json:"w,omitempty"`
	EB     int    `json:"eb,omitempty"`
	MaxLen int    `json:"maxLen,omitempty"`
	Of     int    `json:"of"`
	// Random: an object's bytes are random (it holds no pointers).
	Random bool `json:"random,omitempty"`
	// Ty is the type of an aggregate passed by value (k "agg").
	Ty any `json:"ty,omitempty"`
}

// Func is one lowered function.
type Func struct {
	Name   string
	Index  int
	Gens   []Gen
	Issues []string
	// Consts are the integer constants of the function, which the search
	// tries as values (a magic number guarding a branch).
	Consts   []int64
	Internal bool
	Builtin  bool
	Spec     bool
}

// Model is a lowered module.
type Model struct {
	Funcs []Func
	Lean  string
}

func Compile(src, dir string, flags ...string) (string, error) { return "", errNoLLVM }

// CompileAll needs the LLVM frontend.
func CompileAll(srcs []string, dir string, flags ...string) (string, error) { return "", errNoLLVM }

// Options say what to lower and how parameters are passed.
type Options struct {
	Keep    func(name string) bool
	GoABI   bool
	Callees bool
	Types   map[string][]any
	MaxBuf  int
}

func Load(path string, opts Options) (*Model, error)      { return nil, errNoLLVM }
func WriteModule(model *Model, leanDir, key string) error { return errNoLLVM }
