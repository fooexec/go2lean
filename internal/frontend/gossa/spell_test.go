package gossa

import (
	"go/token"
	"go/types"
	"testing"
)

// A type of the package's test variant (a second copy of the package,
// same path) is spelled without a qualifier in its own package.
func TestSpellTestVariant(t *testing.T) {
	pkg := types.NewPackage("github.com/jmespath/go-jmespath", "jmespath")
	variant := types.NewPackage("github.com/jmespath/go-jmespath", "jmespath")
	node := types.NewNamed(types.NewTypeName(token.NoPos, variant, "ASTNode", nil), types.NewStruct(nil, nil), nil)
	if src, ip := spell(pkg, node); src != "ASTNode" || ip != "" {
		t.Errorf("spell: %q %q, want ASTNode and no import", src, ip)
	}
	other := types.NewPackage("example.com/other", "other")
	if src, ip := spell(other, node); src != "jmespath.ASTNode" || ip != "github.com/jmespath/go-jmespath" {
		t.Errorf("spell from another package: %q %q", src, ip)
	}
}
