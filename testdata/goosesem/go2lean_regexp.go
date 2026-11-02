package semantics

import (
	"regexp"
	"strconv"
)

// go2lean's own tests (#46): regexps whose pattern is built at run time,
// compiled by the model's own compiler, against Go's regexp.

func testRegexpRuntimeRepeat() bool {
	for n := 1; n <= 3; n++ {
		re := regexp.MustCompile("a{" + strconv.Itoa(n) + "}")
		loc := re.FindStringIndex("baaa")
		if loc == nil || loc[0] != 1 || loc[1] != 1+n {
			return false
		}
	}
	return true
}

func testRegexpRuntimeClass() bool {
	r := "a-c"
	re := regexp.MustCompile("^[" + r + "]+$")
	return re.MatchString("abcab") && !re.MatchString("abd") && !re.MatchString("")
}

func testRegexpRuntimeGroups() bool {
	sep := "-"
	re := regexp.MustCompile(`(\d+)` + sep + `(\w+)`)
	m := re.FindStringSubmatch("x 12-ab y")
	return len(m) == 3 && m[0] == "12-ab" && m[1] == "12" && m[2] == "ab"
}

// Leftmost-first: the first alternative that leads to a match wins, and a
// lazy star takes as little as it can.
func testRegexpRuntimeAltLazy() bool {
	lazy := "c*?"
	re := regexp.MustCompile("a(b|bc)" + lazy)
	greedy := regexp.MustCompile("a(b|bc)" + "c*")
	return re.FindString("abcc") == "ab" && greedy.FindString("abcc") == "abcc"
}

// Escapes, anchors, dot and word boundaries.
func testRegexpRuntimeEscapes() bool {
	dot := `\.`
	re := regexp.MustCompile(`^v\d+` + dot + `\d+\b`)
	any := regexp.MustCompile("a" + "." + "c")
	return re.MatchString("v1.25 rest") && !re.MatchString("v1x25") && any.MatchString("abc") && !any.MatchString("a\nc")
}
