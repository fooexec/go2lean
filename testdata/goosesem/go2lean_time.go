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

// Formatting in UTC (#262): fasthttp's AppendHTTPDate formats the server
// date in its package initializer.

func testTimeFormatRFC1123() bool {
	t := time.Unix(1700000000, 0).In(time.UTC)
	b := t.AppendFormat([]byte("x"), time.RFC1123)
	return string(b) == "xTue, 14 Nov 2023 22:13:20 UTC" && len(b) == 30
}

func testTimeFormatLayouts() bool {
	t := time.Date(2009, time.February, 3, 4, 5, 6, 789000000, time.UTC)
	return t.Format(time.RFC3339) == "2009-02-03T04:05:06Z" &&
		t.Format(time.RFC3339Nano) == "2009-02-03T04:05:06.789Z" &&
		t.Format("2006-01-02 15:04:05.000 Mon Jan") == "2009-02-03 04:05:06.789 Tue Feb" &&
		t.Format(time.Kitchen) == "4:05AM"
}

func testTimeFormatNowLength() bool {
	b := time.Now().In(time.UTC).AppendFormat(nil, time.RFC1123)
	return len(b) == 29 && string(b[len(b)-3:]) == "UTC"
}
