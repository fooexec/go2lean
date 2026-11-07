package semantics

import (
	"math"
	"strconv"
)

// go2lean's own tests of strconv.FormatFloat, translated from source under
// -deps (internal/strconv's genericFtoa: shortest digits, fixed precision).

func testFormatFloatShortest() bool {
	return strconv.FormatFloat(0.5, 'f', -1, 64) == "0.5" &&
		strconv.FormatFloat(1234.25, 'f', -1, 64) == "1234.25" &&
		strconv.FormatFloat(-0.125, 'g', -1, 64) == "-0.125" &&
		strconv.FormatFloat(1e21, 'g', -1, 64) == "1e+21" &&
		strconv.FormatFloat(100, 'e', -1, 64) == "1e+02"
}

func testFormatFloatPrecision() bool {
	return strconv.FormatFloat(2.5, 'f', 2, 64) == "2.50" &&
		strconv.FormatFloat(2.675, 'f', 0, 64) == "3" &&
		strconv.FormatFloat(1.0/3, 'f', 4, 64) == "0.3333" &&
		strconv.FormatFloat(123456.0, 'e', 3, 64) == "1.235e+05"
}

func testFormatFloatSpecial() bool {
	return strconv.FormatFloat(math.Inf(1), 'f', -1, 64) == "+Inf" &&
		strconv.FormatFloat(math.Copysign(0, -1), 'f', -1, 64) == "-0" &&
		strconv.FormatFloat(math.NaN(), 'g', -1, 64) == "NaN"
}

func testAppendFloat() bool {
	b := strconv.AppendFloat([]byte("x="), 0.1, 'f', -1, 64)
	return string(b) == "x=0.1"
}
