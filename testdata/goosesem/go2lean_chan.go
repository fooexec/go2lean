package semantics

import "sync"

// go2lean's own tests (#58): channels, select and sync, adapted from what
// Go's runtime/chan_test.go, select and sync tests check (BSD licence, The
// Go Authors), made small, bounded and true under every schedule. At most
// two goroutines besides the caller: with three, the exhaustive engine has
// more schedules than it runs and samples them.

func testChanBufferedFIFO() bool {
	c := make(chan int, 3)
	c <- 1
	c <- 2
	c <- 3
	if len(c) != 3 || cap(c) != 3 {
		return false
	}
	a, b, d := <-c, <-c, <-c
	return a == 1 && b == 2 && d == 3 && len(c) == 0
}

func testChanLenCapZero() bool {
	var n chan int
	u := make(chan int)
	return len(n) == 0 && cap(n) == 0 && len(u) == 0 && cap(u) == 0
}

// From TestChan: a receive from a closed channel does not block; it
// yields the buffered values, then the zero value and false.
func testChanClosedRecv() bool {
	c := make(chan int, 2)
	c <- 7
	close(c)
	v1, ok1 := <-c
	v2, ok2 := <-c
	v3, ok3 := <-c
	return v1 == 7 && ok1 && v2 == 0 && !ok2 && v3 == 0 && !ok3
}

func testChanRangeClosed() bool {
	c := make(chan int, 3)
	c <- 1
	c <- 2
	c <- 3
	close(c)
	sum := 0
	for v := range c {
		sum += v
	}
	return sum == 6
}

// From TestChan: values sent on an unbuffered channel arrive in order.
func testChanUnbufferedHandoff() bool {
	c := make(chan int)
	go func() {
		for i := 1; i <= 3; i++ {
			c <- i
		}
		close(c)
	}()
	want := 1
	for v := range c {
		if v != want {
			return false
		}
		want++
	}
	return want == 4
}

func testChanPingPong() bool {
	ping, pong := make(chan int), make(chan int)
	go func() {
		for v := range ping {
			pong <- v + 1
		}
		close(pong)
	}()
	v := 0
	for i := 0; i < 3; i++ {
		ping <- v
		v = <-pong
	}
	close(ping)
	_, ok := <-pong
	return v == 3 && !ok
}

// From TestChan: closing a channel wakes every receiver blocked on it.
func testChanCloseWakesAll() bool {
	c := make(chan int)
	res := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, ok := <-c
			res <- ok
		}()
	}
	close(c)
	for i := 0; i < 2; i++ {
		if <-res {
			return false
		}
	}
	return true
}

func testSelectDefault() bool {
	c := make(chan int, 1)
	recv := false
	select {
	case <-c:
		recv = true
	default:
	}
	c <- 1
	sent := true
	select {
	case c <- 2:
	default:
		sent = false
	}
	return !recv && !sent && <-c == 1
}

func testSelectReady() bool {
	c := make(chan int, 1)
	c <- 5
	select {
	case v := <-c:
		return v == 5
	default:
		return false
	}
}

// A nil channel is never ready in a select.
func testSelectNilChan() bool {
	var c chan int
	select {
	case <-c:
		return false
	case c <- 1:
		return false
	default:
		return true
	}
}

// With two cases ready, select takes one of them and leaves the other.
func testSelectOneOfReady() bool {
	a, b := make(chan int, 1), make(chan int, 1)
	a <- 1
	b <- 2
	var got int
	select {
	case got = <-a:
	case got = <-b:
	}
	switch got {
	case 1:
		return len(a) == 0 && len(b) == 1
	case 2:
		return len(a) == 1 && len(b) == 0
	}
	return false
}

// From TestSelfSelect: a goroutine that both sends and receives on one
// channel in a select never receives its own value.
func testSelectSelf() bool {
	c := make(chan int)
	var wg sync.WaitGroup
	bad := make(chan bool, 4)
	for p := 0; p < 2; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < 2; i++ {
				select {
				case c <- p:
				case v := <-c:
					if v == p {
						bad <- true
					}
				}
			}
		}(p)
	}
	wg.Wait()
	return len(bad) == 0
}

func testWaitGroupWaits() bool {
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			n++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return n == 2
}

func testMutexExclusion() bool {
	var mu sync.Mutex
	var wg sync.WaitGroup
	n := 0
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2; i++ {
				mu.Lock()
				v := n
				n = v + 1
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return n == 4
}

func testOnceRunsOnce() bool {
	var once sync.Once
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			once.Do(func() {
				mu.Lock()
				n++
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	return n == 1
}
