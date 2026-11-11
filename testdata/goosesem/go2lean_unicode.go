package semantics

import "unicode"

// go2lean's own tests (#184): unicode's tables beyond Latin-1, which the
// package initializer builds (var Letter = _L, var _L = &RangeTable{...}).
func testUnicodeLetterTables() bool {
	return unicode.IsLetter('λ') && unicode.IsLetter('中') && unicode.IsLetter('a') &&
		!unicode.IsLetter('1') && !unicode.IsLetter('→') && !unicode.IsLetter(' ')
}

func testUnicodeUpperLower() bool {
	return unicode.IsUpper('Δ') && !unicode.IsUpper('δ') && unicode.IsLower('δ') && unicode.IsDigit('٣')
}
