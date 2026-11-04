package semantics

import "math"

// go2lean's own tests (#148): floats as IEEE 754 values. Operands are
// variables, so the arithmetic happens at run time, not in the compiler.

func testFloatAddRounds() bool {
	a, b := 0.1, 0.2
	s := a + b
	return s != 0.3 && math.Float64bits(s) == 0x3FD3333333333334
}

func testFloatArith() bool {
	x, y := 7.5, 2.0
	return x+y == 9.5 && x-y == 5.5 && x*y == 15 && x/y == 3.75 && -x == -7.5
}

func testFloatNaN() bool {
	zero := 0.0
	nan := zero / zero
	return nan != nan && !(nan == nan) && !(nan < 1) && !(nan >= 1) && math.IsNaN(nan)
}

func testFloatSignedZero() bool {
	zero := 0.0
	neg := -zero
	return neg == zero && math.Float64bits(neg) == 1<<63 && 1/neg < 0 && 1/zero > 0
}

func testFloatInf() bool {
	one, zero := 1.0, 0.0
	inf := one / zero
	return inf > math.MaxFloat64 && -inf < -math.MaxFloat64 && inf+1 == inf && math.IsInf(inf-inf, 0) == false
}

func testFloatToInt() bool {
	a, b, c := 3.7, -3.7, 255.9
	return int(a) == 3 && int(b) == -3 && uint8(c) == 255 && int64(b*1e6) == -3700000
}

func testIntToFloat() bool {
	n, m := int64(-3), uint64(1<<64-1)
	var big int64 = 1<<60 + 1<<36 + 1 // rounds to float32 once, upward
	return float64(n) == -3 && float64(m) == 18446744073709551615.0 &&
		math.Float32bits(float32(big)) == 0x5D800001
}

func testFloat32Rounds() bool {
	d := 0.1
	f := float32(d)
	return float64(f) != d && math.Float32bits(f) == 0x3DCCCCCD && float64(f) == 0.10000000149011612
}

func testFloat32Arith() bool {
	a, b := float32(16777216), float32(1)
	return a+b == a && float64(a)+float64(b) != float64(a) // 2^24 + 1 rounds back in float32
}

func testFloatCompareMixed() bool {
	xs := []float64{3, -1, 2.5, 0}
	max := xs[0]
	for _, x := range xs[1:] {
		if x > max {
			max = x
		}
	}
	return max == 3
}
