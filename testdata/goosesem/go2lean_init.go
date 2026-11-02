package semantics

import (
	"sync"
	"time"
)

// go2lean's own tests (#55): package variables set by a call with constant
// arguments, and an alias of one, as sarama's Kafka versions are.

type kversion struct{ v [4]uint }

func newKVersion(a, b, c, d uint) kversion { return kversion{[4]uint{a, b, c, d}} }

func (x kversion) atLeast(o kversion) bool {
	for i := range x.v {
		if x.v[i] > o.v[i] {
			return true
		} else if x.v[i] < o.v[i] {
			return false
		}
	}
	return true
}

var (
	kv0820    = newKVersion(0, 8, 2, 0)
	kv0110    = newKVersion(0, 11, 0, 0)
	kvMinimum = kv0820
)

func testInitCallVersions() bool {
	return !kvMinimum.atLeast(kv0110) && kv0110.atLeast(kvMinimum) && kv0110.atLeast(kv0110)
}

// A pointer sentinel with an array field set to constants (grpc's bdpPing,
// evaluation 7's k001): every load reads the same constants (#71).
type pingT struct{ data [4]byte }

var pingConst = &pingT{data: [4]byte{2, 4, 16, 9}}

func testInitSentinelArray() bool {
	d := pingConst.data
	return d == pingConst.data && d[2] == 16 && pingConst.data[3] == 9
}

// A registry its initializer builds, shared with a goroutine (#55): the
// goroutine writes into the map the initializer made, and the caller sees
// both the initializer's entry and the goroutine's.
type semRegistry struct {
	mu sync.Mutex
	m  map[string]int
}

var semReg = &semRegistry{m: map[string]int{"init": 1}}

func testInitCellShared() bool {
	done := make(chan bool)
	go func() {
		semReg.mu.Lock()
		semReg.m["go"] = 2
		semReg.mu.Unlock()
		done <- true
	}()
	<-done
	semReg.mu.Lock()
	defer semReg.mu.Unlock()
	return semReg.m["init"] == 1 && semReg.m["go"] == 2
}

// A table a func init() makes and fills, shared with a goroutine (#55).
var semTable map[string]int

func init() {
	semTable = make(map[string]int)
	semTable["init"] = 1
}

func testInitFuncCellShared() bool {
	done := make(chan bool)
	go func() {
		semTable["go"] = 2
		done <- true
	}()
	<-done
	return semTable["init"] == 1 && semTable["go"] == 2
}

// A package variable holding a struct with a mutex, set by its initializer
// to a literal with a built field, and locked and written by goroutines,
// as go-metrics' arbiter is (#129).
type initArbiter struct {
	sync.Mutex
	started bool
	items   map[uint64]bool
}

var initArb = initArbiter{items: map[uint64]bool{}}

func initArbRegister(k uint64) bool {
	initArb.Lock()
	defer initArb.Unlock()
	initArb.items[k] = true
	first := !initArb.started
	initArb.started = true
	return first
}

// Two goroutines register: exactly one is first, and both are in the map.
func testInitStructCellShared() bool {
	var wg sync.WaitGroup
	var a, b bool
	wg.Add(2)
	go func() { defer wg.Done(); a = initArbRegister(1) }()
	go func() { defer wg.Done(); b = initArbRegister(2) }()
	wg.Wait()
	initArb.Lock()
	n := len(initArb.items)
	initArb.Unlock()
	return a != b && n == 2
}

// A package variable whose initializer starts a ticker, and a goroutine
// that then waits on the ticker for good, as go-metrics' arbiter ticks
// its meters (#129). Not a leak: the ticker feeds it.
type initTicker struct {
	sync.Mutex
	started bool
	t       *time.Ticker
	n       uint64
}

var initTick = initTicker{t: time.NewTicker(time.Hour)}

func initTickStart() {
	initTick.Lock()
	defer initTick.Unlock()
	if !initTick.started {
		initTick.started = true
		go func() {
			for range initTick.t.C {
				initTick.Lock()
				initTick.n++
				initTick.Unlock()
			}
		}()
	}
}

func testInitTickerDaemon() bool {
	initTickStart()
	initTickStart()
	initTick.Lock()
	started := initTick.started
	initTick.Unlock()
	return started
}
