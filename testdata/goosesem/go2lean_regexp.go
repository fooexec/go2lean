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

// A package-level regexp whose pattern is another package variable built
// from constants (phonenumbers' VALID_START_CHAR_PATTERN, #46).
const semPlus = "+＋"

var (
	semStartChar    = "[" + semPlus + "0-9]"
	semStartPattern = regexp.MustCompile(semStartChar)
)

func testRegexpPackageVarPattern() bool {
	loc := semStartPattern.FindStringIndex("tel: +41 44")
	return loc != nil && loc[0] == 5 && loc[1] == 6 && semStartPattern.FindStringIndex("tel") == nil
}

// The []byte methods whose results are indices or a bool (#46).
func testRegexpBytesIndex() bool {
	b := []byte("tel: +41 44")
	loc := semStartPattern.FindIndex(b)
	all := semStartPattern.FindAllIndex(b, -1)
	return loc != nil && loc[0] == 5 && loc[1] == 6 && semStartPattern.Match(b) &&
		!semStartPattern.Match([]byte("tel")) && len(all) == 5 && all[4][0] == 10
}

// regexp.Compile of a pattern built at run time, with named groups,
// QuoteMeta and NumSubexp, as gorilla/mux builds its route patterns (#173).
var compileFunc = regexp.Compile

func testRegexpCompileNamed() bool {
	raw := "/a.b/"
	re, err := compileFunc("^" + regexp.QuoteMeta(raw) + "(?P<v0>[a-z]+)" + "(?<v1>[0-9]*)$")
	if err != nil || re.NumSubexp() != 2 {
		return false
	}
	m := re.FindStringSubmatch("/a.b/id42")
	return len(m) == 3 && m[1] == "id" && m[2] == "42" && !re.MatchString("/aXb/id")
}

// A pattern Go rejects: Compile returns a nil regexp and an error.
func testRegexpCompileError() bool {
	open := "("
	re, err := regexp.Compile("a" + open)
	_, err2 := regexp.Compile("(?P<>a)")
	return re == nil && err != nil && err2 != nil
}

// QuoteMeta escapes exactly the metacharacters.
func testRegexpQuoteMeta() bool {
	s := `a.b+c*d?e(f)g|h[i]j{k}l^m$n\o-p`
	return regexp.QuoteMeta(s) == `a\.b\+c\*d\?e\(f\)g\|h\[i\]j\{k\}l\^m\$n\\o-p` && regexp.QuoteMeta("") == ""
}
