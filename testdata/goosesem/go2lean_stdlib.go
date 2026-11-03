package semantics

import (
	"bufio"
	"bytes"
	"container/list"
	"sort"
	"strings"
)

// go2lean's own tests (#69): standard-library packages translated from
// their source with -deps.

func testListPushRemove() bool {
	l := list.New()
	a := l.PushBack(1)
	l.PushBack(2)
	l.PushFront(0)
	l.Remove(a)
	sum, n := 0, 0
	for e := l.Front(); e != nil; e = e.Next() {
		sum += e.Value.(int)
		n++
	}
	return l.Len() == 2 && n == 2 && sum == 2 && l.Front().Value.(int) == 0
}

func testListMoveToFront() bool {
	l := list.New()
	l.PushBack("a")
	b := l.PushBack("b")
	l.MoveToFront(b)
	return l.Front().Value.(string) == "b" && l.Back().Value.(string) == "a"
}

func testBufioWriterFlush() bool {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	w.WriteString("hello, ")
	w.WriteString("world")
	before := buf.Len()
	w.Flush()
	return before == 0 && buf.String() == "hello, world"
}

func testBufioReaderReadString() bool {
	r := bufio.NewReader(strings.NewReader("one\ntwo\n"))
	a, err1 := r.ReadString('\n')
	b, err2 := r.ReadString('\n')
	_, err3 := r.ReadString('\n')
	return a == "one\n" && b == "two\n" && err1 == nil && err2 == nil && err3 != nil
}

// sort's binary searches, translated from source (#46).
func testSortSearch() bool {
	xs := []int{1, 3, 5, 7, 9}
	i := sort.Search(len(xs), func(i int) bool { return xs[i] >= 6 })
	words := []string{"apple", "kiwi", "pear"}
	return i == 3 && sort.SearchInts(xs, 9) == 4 && sort.SearchInts(xs, 10) == 5 &&
		sort.SearchStrings(words, "kiwi") == 1 && sort.SearchStrings(words, "a") == 0
}
