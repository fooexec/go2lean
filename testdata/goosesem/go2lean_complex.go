package semantics

import "math"

// go2lean's own tests (#207): complex numbers, including division's
// corrections of a NaN result (runtime.complex128div).

func cmul(a, b complex128) complex128 { return a * b }
func cdiv(a, b complex128) complex128 { return a / b }
func cdiv64(a, b complex64) complex64 { return a / b }

func testComplexArith() bool {
	a := complex(1.5, -2)
	b := complex(0.25, 4)
	s, d := a+b, a-b
	return real(s) == 1.75 && imag(s) == 2 && real(d) == 1.25 && imag(d) == -6 &&
		cmul(a, b) == complex(1.5*0.25+8, 1.5*4-2*0.25) &&
		cdiv(complex(1, 1), complex(1, -1)) == 1i &&
		a != b && a == complex(1.5, -2)
}

func testComplexDivEdges() bool {
	inf := math.Inf(1)
	z := cdiv(complex(1, 2), 0)
	w := cdiv(complex(inf, 1), complex(2, 3))
	v := cdiv(complex(1, 1), complex(inf, inf))
	return math.IsInf(real(z), 1) && math.IsInf(imag(z), 1) &&
		math.IsInf(real(w), 1) && math.IsInf(imag(w), -1) &&
		real(v) == 0 && imag(v) == 0
}

func testComplex64() bool {
	q := cdiv64(complex64(complex(1, 0)), complex64(complex(3, 0)))
	c := complex128(q)
	return real(q) == float32(1.0/3.0) && imag(q) == 0 && real(c) == float64(float32(1.0/3.0))
}
