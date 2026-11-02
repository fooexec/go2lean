package semantics

import "fmt"

// go2lean's own tests (#46): fmt.Sprintf with widths and flags, and %x on
// integers, as Go's fmt_test.go checks them.

func testFmtWidthZeroHex() bool {
	return fmt.Sprintf("%02x", 10) == "0a" && fmt.Sprintf("%02x", 255) == "ff" &&
		fmt.Sprintf("%04X", 3054) == "0BEE"
}

func testFmtWidthSpaces() bool {
	return fmt.Sprintf("%5d|", 42) == "   42|" && fmt.Sprintf("%-4d|", 7) == "7   |" &&
		fmt.Sprintf("%3s|", "ab") == " ab|" && fmt.Sprintf("%-3s|", "ab") == "ab |"
}

func testFmtZeroNegative() bool {
	return fmt.Sprintf("%03d", -5) == "-05" && fmt.Sprintf("%x", -255) == "-ff"
}

// A width narrower than the value changes nothing.
func testFmtWidthNarrow() bool {
	return fmt.Sprintf("%2d", 12345) == "12345" && fmt.Sprintf("%1x", 4096) == "1000"
}

// Every byte prints as two hex digits under %02x.
func testFmtHexByte() bool {
	for _, b := range []uint8{0, 9, 10, 15, 16, 255} {
		if s := fmt.Sprintf("%02x", b); len(s) != 2 {
			return false
		}
	}
	return fmt.Sprintf("%02x", uint8(10)) == "0a"
}
