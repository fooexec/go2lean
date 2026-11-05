package semantics

import (
	"bytes"
	"encoding/json"
	"strings"
)

// go2lean's own tests (#71): arrays longer than the dialect's bigArr (256
// elements) are kept as arrays updated in place; every operation on them
// must agree with Go's.

func testBigSliceFillRead() bool {
	n := 1000
	xs := make([]int, n)
	for i := range xs {
		xs[i] = i * 3
	}
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return len(xs) == n && cap(xs) == n && xs[999] == 2997 && sum == 3*n*(n-1)/2
}

func testBigArrayConstLen() bool {
	var a [5000]int32
	a[4999] = 7
	b := a
	b[0] = 1
	p := &a
	return a[4999] == 7 && b[4999] == 7 && a[0] == 0 && b[0] == 1 && len(p[10:]) == 4990 && cap(p[10:20]) == 4990
}

func testBigSliceAppendCopy() bool {
	var xs []int
	for i := 0; i < 600; i++ {
		xs = append(xs, i)
	}
	ys := make([]int, 700, 1000)
	n := copy(ys[50:], xs)
	ys = append(ys, 9, 8)
	zs := ys[:300:400]
	zs = append(zs, -1)
	return len(xs) == 600 && n == 600 && ys[50] == 0 && ys[649] == 599 && ys[650] == 0 &&
		len(ys) == 702 && ys[701] == 8 && ys[300] == -1 && zs[300] == -1
}

func testBigBytesString() bool {
	buf := make([]byte, 512)
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	s := string(buf[26:52])
	t := string(buf)
	return s == "abcdefghijklmnopqrstuvwxyz" && len(t) == 512 && strings.Count(t, "z") == 19 &&
		bytes.Equal(buf[:26], buf[26:52]) && bytes.IndexByte(buf, 'z') == 25
}

func testBigArrayEqual() bool {
	var a, b [300]int
	for i := range a {
		a[i] = i
		b[i] = i
	}
	eq := a == b
	b[299] = 0
	return eq && a != b
}

func testBigSliceReverse() bool {
	xs := make([]int, 400)
	for i := range xs {
		xs[i] = i
	}
	for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
		xs[i], xs[j] = xs[j], xs[i]
	}
	return xs[0] == 399 && xs[399] == 0 && xs[200] == 199 && xs[199] == 200
}

func testBigSliceJSON() bool {
	xs := make([]int, 300)
	for i := range xs {
		xs[i] = i % 10
	}
	out, err := json.Marshal(xs)
	return err == nil && len(out) == 2+300+299 && out[1] == '0' && out[len(out)-2] == '9'
}

type stripe struct {
	n    int
	tags map[string]int
}

func testBigSliceOfStructs() bool {
	ss := make([]stripe, 2048)
	for i := range ss {
		ss[i].n = i
		ss[i].tags = map[string]int{}
	}
	ss[2047].tags["k"] = 5
	p := &ss[100]
	p.n = -1
	return ss[2047].tags["k"] == 5 && ss[100].n == -1 && ss[2046].n == 2046 && len(ss[0].tags) == 0
}
