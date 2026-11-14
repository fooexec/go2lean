package semantics

// go2lean's own tests: method expressions as func values, called through
// a table (gomarkdown's block rules are a table of (*Parser).M).

type mxCounter struct{ n int }

func (c *mxCounter) add(k int) int { c.n += k; return c.n }

func (c mxCounter) get(k int) int { return c.n + k }

type mxRule struct {
	f func(*mxCounter, int) int
}

func testMethodExprPointer() bool {
	c := &mxCounter{n: 1}
	rules := [2]mxRule{{f: (*mxCounter).add}, {f: (*mxCounter).add}}
	r := 0
	for _, x := range rules {
		r = x.f(c, 2)
	}
	return r == 5 && c.n == 5
}

func testMethodExprValue() bool {
	c := mxCounter{n: 3}
	f := mxCounter.get
	return f(c, 4) == 7
}

func testMethodExprValueViaPointer() bool {
	c := &mxCounter{n: 3}
	f := (*mxCounter).get
	c.n = 10
	return f(c, 1) == 11
}
