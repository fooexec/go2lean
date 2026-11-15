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

// fmt.Sprintln: every operand with %v, spaces between them and a newline,
// as logrus's Infoln formats its arguments.

func testFmtSprintln() bool {
	return fmt.Sprintln("a", 1, true) == "a 1 true\n" && fmt.Sprintln() == "\n" &&
		fmt.Sprintln("x") == "x\n"
}

func sprintlnOf(args ...any) string { return fmt.Sprintln(args...) }

func testFmtSprintlnSpread() bool {
	return sprintlnOf("level", 7) == "level 7\n"
}

// fmt.Sprint: every operand with %v, a space between two operands only
// when neither is a string (Masterminds/semver's spec formats versions
// with it; havoc made a spurious verdict).

func testFmtSprint() bool {
	return fmt.Sprint(1, 2) == "1 2" && fmt.Sprint("a", 1, 2, "b") == "a1 2b" &&
		fmt.Sprint() == "" && fmt.Sprint("x", "y") == "xy" && fmt.Sprint(true, 3) == "true 3"
}

type sprintName string

func sprintOf(args ...any) string { return fmt.Sprint(args...) }

func testFmtSprintSpread() bool {
	// A named string type is a string to Sprint: no space next to it.
	return sprintOf(sprintName("v"), 1, 2) == "v1 2" && sprintOf(uint8(255), -1) == "255 -1"
}
