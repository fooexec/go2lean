package semantics

import "sync"

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
