// Package portable is a ground-truth example from running go2lean on code
// it was not developed on (one-api, spf13/cast): each function stands for a
// construct whose model gave a spurious or missed result there.
package portable

import (
	"errors"
	"math/rand"
	"regexp"
	"strconv"
	"sync/atomic"

	"github.com/andreasfoo/go2lean/examples/portable/meta"
)

// URL is the first byte of the base URL. Bug: panics on an empty BaseURL.
// A pointer to a struct of exported fields from another package is data,
// even when that package has a function returning one.
func URL(m *meta.Meta) string {
	return m.BaseURL[:1]
}

// Digit is a random decimal digit: rand.Intn(n) is in [0, n), so the index
// is always in range (holds).
func Digit() byte {
	const digits = "0123456789"
	return digits[rand.Intn(len(digits))]
}

// Pick is a random element. Bug: rand.Intn panics on an empty slice.
func Pick(xs []string) string {
	return xs[rand.Intn(len(xs))]
}

// Parse divides by a parsed count. Bug: "0" and anything not a number give
// zero. parse, below, has the same name up to case: each is a target.
func Parse(s string) int { return parse(s) + 1 }

func parse(s string) int {
	n, _ := strconv.Atoi(s)
	return 10 / n
}

// PerMax is how many of a limit fit in 1000. A limit out of the int64
// range parses to the nearest bound (with an error), not to zero, so the
// division is safe (holds).
func PerMax() int {
	n, _ := strconv.Atoi("9223372036854775808")
	return 1000 / n
}

// Must returns v or panics with err. Generic: its instances are modelled,
// not the function itself (Must[int] is used by Port); run searches it as
// Must[int] (-generics), confirmed: it panics on a non-nil error, as it
// says.
func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Port parses a port. Bug: panics on anything but a number.
func Port(s string) int {
	return Must(strconv.Atoi(s))
}

var kb = 1024
var mb = kb * 1024

// PerMB is how many megabytes n bytes are: mb is a constant expression
// over another package variable, never zero (holds).
func PerMB(n int) int { return n / mb }

var intPart = regexp.MustCompile(`^(\d+)\.`)

// IntPart is the integer part of a decimal: the pattern has one group, so
// a match has two elements (holds).
func IntPart(s string) string {
	if m := intPart.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// Unit is the unit of a size like "10kb". Bug: the pattern has one group,
// and m[2] is out of range on every match (the model runs the pattern, and
// the search draws strings it matches).
func Unit(s string) string {
	if m := regexp.MustCompile(`^\d+([a-z]+)$`).FindStringSubmatch(s); m != nil {
		return m[2]
	}
	return ""
}

// ErrEmpty is a sentinel error.
var ErrEmpty = errors.New("empty")

func first(xs []int) (int, error) {
	if len(xs) == 0 {
		return 0, ErrEmpty
	}
	return xs[0], nil
}

// Count is ten over the length: the sentinel check covers the empty slice
// (holds; errors.Is on a sentinel the model returned is exact).
func Count(xs []int) int {
	if _, err := first(xs); errors.Is(err, ErrEmpty) {
		return 0
	}
	return 10 / len(xs)
}

// Head is a hundred over the first element. Bug: a first element of 0.
func Head(xs []int) int {
	v, err := first(xs)
	if err == ErrEmpty {
		return -1
	}
	return 100 / v
}

// Per is a hundred over a count of any integer type, cast-style. Bug: a
// uint8 count of 0 (an input of type any reaches every case the switch
// names).
func Per(v any) int {
	switch x := v.(type) {
	case int64:
		if x == 0 {
			return 0
		}
		return int(100 / x)
	case uint8:
		return 100 / int(x)
	case string:
		return len(x)
	}
	return -1
}

var (
	verRe  = regexp.MustCompile(`^v(\d+)\.(\d+)$`)
	dateRe = regexp.MustCompile(`^(\d{4})-(\d\d)$`)
)

// group is the n-th group of the first match of re in s, or "".
func group(re *regexp.Regexp, s string, n int) string {
	if m := re.FindStringSubmatch(s); m != nil && n < len(m) {
		return m[n]
	}
	return ""
}

// Minor is a hundred over the minor version of "v1.2". Bug: "v1.0" (the
// regexp reaches group through a parameter: picked at run time).
func Minor(s string) int {
	n, err := strconv.Atoi(group(verRe, s, 2))
	if err != nil {
		return 0
	}
	return 100 / n
}

// Month is the month of "2024-05" as an index into twelve names (holds:
// the check covers both ends).
func Month(s string) string {
	n, err := strconv.Atoi(group(dateRe, s, 2))
	if err != nil || n < 1 || n > 12 {
		return ""
	}
	return "JFMAMJJASOND"[n-1 : n]
}

// Label tags a name with the session's model. Bug: an empty name divides
// by zero (a field read through a handle is havoc, not the end of the run).
func Label(s *meta.Session, name string) string {
	return s.Model + "/" + strconv.Itoa(100/len(name))
}

type encoder struct{ perToken int }

var enc *encoder

// InitEncoder sets up the encoder, as one-api's InitTokenEncoders does at
// start-up.
func InitEncoder() { enc = &encoder{perToken: 4} }

// Tokens is the token count of s. It panics when InitEncoder has not run:
// confirmed, with the verdict noting the package state it read.
func Tokens(s string) int { return len(s) / enc.perToken }

// ring is built by newRing only (it is unexported): a ring with an empty
// buffer is a state no caller can make, so push is a target only through
// newRing, called with no variadic argument (push~newRing, holds, #186).
type ring struct {
	buf []int
	pos int
}

func newRing(sizes ...int) *ring {
	n := 4
	if len(sizes) > 0 && sizes[0] > 0 {
		n = sizes[0]
	}
	return &ring{buf: make([]int, n)}
}

func (r *ring) push(v int) {
	r.buf[r.pos%len(r.buf)] = v
	r.pos++
}

// Fill pushes n values into a fresh ring (holds).
func Fill(n int) int {
	r := newRing()
	for i := 0; i < n && i < 16; i++ {
		r.push(i)
	}
	return r.pos
}

// Budget fits a requested budget under a cap of at least 1024 and returns
// how many 1024-token chunks it is: min and max are exact, so the divisor
// is never zero (holds).
func Budget(req, capTokens int) int {
	c := max(capTokens, 1024)
	return 1_000_000 / (min(req, c)%c + c)
}

// Shrink takes a third of the smaller of two limits. Bug: both 0.
func Shrink(a, b int) int { return 300 / min(a, b, 100) }

type counter struct {
	done atomic.Bool
	n    atomic.Int64
}

// Drain counts n events, then reports the average over them. Bug: n of 0
// (the atomic counter is read back as written).
func Drain(n int) int64 {
	var c counter
	for i := 0; i < n && i < 8; i++ {
		c.n.Add(10)
	}
	c.done.Store(true)
	if !c.done.Load() {
		return -1
	}
	return 100 / c.n.Load()
}

// Kind is an enum with a stringer-style String (go generate's layout).
type Kind int

const _Kind_name = "TextImageTool"

var _Kind_index = [...]uint8{0, 4, 9, 13}

// String reads the names table: in range for 0..2, Kind(n) otherwise
// (holds: the bounds check covers the table).
func (i Kind) String() string {
	if i < 0 || i >= Kind(len(_Kind_index)-1) {
		return "Kind(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _Kind_name[_Kind_index[i]:_Kind_index[i+1]]
}

// Label2 is the name of a kind, read off by one. Bug: Kind 2 reads past
// the table.
func Label2(k Kind) string {
	if k < 0 || k > 2 {
		return ""
	}
	return _Kind_name[_Kind_index[k+1]:_Kind_index[k+2]]
}

// scratch is a buffer released by a deferred call, as gobwas/glob's
// Pattern.Match releases its matching state (a defer of a modelled
// function, once rejected: the function was not searched at all).
type scratch struct{ buf []int }

func release(s *scratch) { s.buf = s.buf[:0] }

// SumFirst adds the first n of xs. Bug: n is not checked against len(xs).
func SumFirst(xs []int, n int) int {
	s := &scratch{}
	if n > 0 {
		defer release(s)
	}
	total := 0
	for i := 0; i < n; i++ {
		total += xs[i]
	}
	return total
}

type session struct{ open []int }

func finish(s *session) { s.open = s.open[:len(s.open)-1] }

// Enter returns how many entries are open, closing the last one on the
// way out. Bug: with nothing open the deferred finish panics.
func Enter(s *session, push bool) int {
	if push {
		s.open = append(s.open, 1)
	}
	defer finish(s)
	return len(s.open)
}
