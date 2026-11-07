package semantics

import "math/big"

// go2lean's own tests of math/big, translated from its portable Go under
// -deps (math_big_pure_go, #171): the arithmetic and the decimal form of
// numbers past 64 bits.

// A product past 64 bits, and its decimal form.
func testBigMulString() bool {
	x := new(big.Int).Mul(big.NewInt(9223372036854775807), big.NewInt(10))
	return x.String() == "92233720368547758070" && x.Sign() == 1 && !x.IsInt64()
}

// Add, Sub and Cmp across zero.
func testBigAddSubCmp() bool {
	a := big.NewInt(-5)
	b := new(big.Int).Add(a, big.NewInt(12))
	c := new(big.Int).Sub(a, big.NewInt(12))
	return b.Int64() == 7 && c.Int64() == -17 && a.Cmp(b) < 0 && c.Cmp(a) < 0 && b.Cmp(big.NewInt(7)) == 0
}

// Exp with a small exponent, and Abs and Neg.
func testBigExpAbsNeg() bool {
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)
	n := new(big.Int).Neg(p)
	return p.String() == "100000000000000000000" && n.Sign() == -1 && new(big.Int).Abs(n).Cmp(p) == 0
}

// QuoRem and DivMod: truncated and Euclidean division.
func testBigQuoRemDivMod() bool {
	q, r := new(big.Int).QuoRem(big.NewInt(-7), big.NewInt(2), new(big.Int))
	d, m := new(big.Int).DivMod(big.NewInt(-7), big.NewInt(2), new(big.Int))
	return q.Int64() == -3 && r.Int64() == -1 && d.Int64() == -4 && m.Int64() == 1
}

// SetString parses a long decimal; a bad one fails.
func testBigSetString() bool {
	x, ok := new(big.Int).SetString("123456789012345678901234567890", 10)
	_, bad := new(big.Int).SetString("12a", 10)
	return ok && !bad && x.String() == "123456789012345678901234567890"
}
