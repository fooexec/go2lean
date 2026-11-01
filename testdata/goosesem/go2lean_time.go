package semantics

import "time"

// go2lean's own tests (#59): the clock never goes back within a run, and
// the Time methods agree with it, as in Go.

func testTimeNowMonotone() bool {
	a := time.Now()
	b := time.Now()
	return !b.Before(a) && b.Sub(a) >= 0 && time.Since(a) >= 0
}

func testTimeAddSub() bool {
	t := time.Now()
	u := t.Add(3 * time.Second)
	return u.After(t) && t.Before(u) && u.Sub(t) == 3*time.Second &&
		!t.Equal(u) && u.Add(-3*time.Second).Equal(t) && t.Compare(u) == -1
}

func testTimeUntil() bool {
	t := time.Now().Add(time.Hour)
	return time.Until(t) > 0
}
