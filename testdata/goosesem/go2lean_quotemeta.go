package semantics

import "regexp"

// go2lean's own test: a pattern built with regexp.QuoteMeta of a constant
// (raymond's lexer) is a constant pattern the model runs.

var qmLook = `[\s` + regexp.QuoteMeta("=~}/)|") + `]`

var qmDotID = regexp.MustCompile(`^\.` + qmLook)

func testRegexpBuiltWithQuoteMeta() bool {
	return qmDotID.MatchString(". x") && qmDotID.MatchString(".}") && !qmDotID.MatchString(".a")
}
