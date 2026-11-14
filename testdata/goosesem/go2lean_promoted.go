package semantics

// go2lean's own tests: an interface call of a method promoted from an
// embedded field (gomarkdown's nodes embed ast.Container, #234).

type prNode interface{ Kids() int }

type prBase struct{ n int }

func (b *prBase) Kids() int { return b.n }

type prVal struct{ k int }

func (v prVal) Kids() int { return v.k + 100 }

type prDoc struct{ prBase }

type prRef struct{ *prBase }

type prDeep struct{ prDoc }

type prByVal struct{ prVal }

func testPromotedPointerMethod() bool {
	var n prNode = &prDoc{prBase{n: 3}}
	return n.Kids() == 3
}

func testPromotedEmbeddedPointer() bool {
	b := &prBase{n: 4}
	var n prNode = prRef{b}
	b.n = 5
	return n.Kids() == 5
}

func testPromotedTwoLevels() bool {
	var n prNode = &prDeep{prDoc{prBase{n: 6}}}
	return n.Kids() == 6
}

func testPromotedValueMethod() bool {
	var a prNode = prByVal{prVal{k: 1}}
	var b prNode = &prByVal{prVal{k: 2}}
	return a.Kids() == 101 && b.Kids() == 102
}

func prKidsPanics(n prNode) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	n.Kids()
	return false
}

func testPromotedNilEmbeddedPanics() bool {
	return prKidsPanics(prRef{nil})
}
