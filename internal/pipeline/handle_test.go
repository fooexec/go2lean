package pipeline

import (
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

func TestOpaqueHandle(t *testing.T) {
	opaqueStruct := &contract.ABIParam{Go: "cc", GoType: "grpc.ClientConn", TypePkg: "google.golang.org/grpc", Kind: contract.ABIOpaque, Zero: "grpc.ClientConn{}"}
	stdStruct := &contract.ABIParam{Go: "re", GoType: "regexp.Regexp", TypePkg: "regexp", Kind: contract.ABIOpaque, Zero: "regexp.Regexp{}"}
	opaqueIface := &contract.ABIParam{Go: "c", GoType: "net.Conn", Kind: contract.ABIOpaque, Zero: "nil"}
	handle := contract.ABIParam{Go: "cc", GoType: "*grpc.ClientConn", Kind: contract.ABIPtr, Pointee: opaqueStruct}
	toIface := contract.ABIParam{Go: "c", GoType: "*net.Conn", Kind: contract.ABIPtr, Pointee: opaqueIface}
	toStd := contract.ABIParam{Go: "re", GoType: "*regexp.Regexp", Kind: contract.ABIPtr, Pointee: stdStruct}
	scalar := contract.ABIParam{Go: "v", GoType: "int", Kind: contract.ABIInt, Width: 64, Signed: true}

	cases := []struct {
		name string
		abi  contract.ABI
		want bool
	}{
		{"scalar", contract.ABI{Params: []contract.ABIParam{scalar}}, false},
		{"pointer to an opaque interface", contract.ABI{Params: []contract.ABIParam{toIface}}, false},
		{"standard library struct", contract.ABI{Params: []contract.ABIParam{toStd}}, false},
		{"handle parameter", contract.ABI{Params: []contract.ABIParam{scalar, handle}}, true},
		{"handle in a receiver's field", contract.ABI{Params: []contract.ABIParam{{
			Go: "s", GoType: "*server", Kind: contract.ABIPtr,
			Pointee: &contract.ABIParam{Go: "s", GoType: "server", Kind: contract.ABIStruct, Fields: []contract.ABIParam{scalar, handle}},
		}}}, true},
		{"handle in a recursive type", contract.ABI{
			Types: []contract.ABIType{{GoType: "node", Fields: []contract.ABIParam{
				{Go: "next", GoType: "*node", Kind: contract.ABIPtr, Pointee: &contract.ABIParam{Go: "next", GoType: "node", Kind: contract.ABIStruct, TypeRef: "node"}},
				handle,
			}}},
			Params: []contract.ABIParam{{Go: "n", GoType: "node", Kind: contract.ABIStruct, TypeRef: "node"}},
		}, true},
	}
	for _, c := range cases {
		if got := opaqueHandle(&c.abi); got != c.want {
			t.Errorf("%s: opaqueHandle = %v, want %v", c.name, got, c.want)
		}
	}
}
