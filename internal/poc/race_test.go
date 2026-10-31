package poc

import (
	"strings"
	"testing"
)

// The replay controller's own map accesses race (its lock is hidden from
// the detector): such reports are not the program's.
func TestRaceReportSkipsController(t *testing.T) {
	controller := `==================
WARNING: DATA RACE
Read at 0x00c000282000 by goroutine 9:
  runtime.mapaccess1_fast64()
      /go/src/internal/runtime/maps/runtime_fast64.go:196 +0x0
  example.com/m.go2leanGate()
      /m/zz_go2lean_replay.go:133 +0x12c

Previous write at 0x00c000282000 by goroutine 8:
  runtime.mapassign_fast64()
      /go/src/internal/runtime/maps/runtime_fast64.go:20 +0x0
  example.com/m.go2leanDrive()
      /m/zz_go2lean_replay.go:250 +0x1
==================
`
	program := `==================
WARNING: DATA RACE
Write at 0x00c00009a008 by goroutine 10:
  example.com/m.PeekEarly.func1()
      /m/races.go:81 +0x3c

Previous read at 0x00c00009a008 by goroutine 9:
  example.com/m.PeekEarly()
      /m/races.go:85 +0x1
==================
`
	if got := raceReport(controller); got != "" {
		t.Fatalf("controller race reported: %s", got)
	}
	if got := raceReport(controller + program); !strings.Contains(got, "PeekEarly.func1") {
		t.Fatalf("program race not reported: %q", got)
	}
}

// A race whose access is made inside a harness helper, below a frame of the
// standard library (go2leanDeadline running a context's cancel), is the
// harness's, not the program's (#66; evaluation 7's k031 at the fix).
func TestRaceReportSkipsHelperBelowStdlib(t *testing.T) {
	deadline := `==================
WARNING: DATA RACE
Write at 0x00c0000a4010 by goroutine 10:
  sync/atomic.CompareAndSwapInt32()
      /go/src/runtime/race_amd64.s:361 +0xb
  sync.(*Mutex).Lock()
      /go/src/sync/mutex.go:46 +0x28
  context.(*cancelCtx).cancel()
      /go/src/context/context.go:547 +0x84
  context.WithCancel.func1()
      /go/src/context/context.go:242 +0x52
  github.com/quic-go/quic-go.go2leanDeadline.func1()
      /m/zz_go2lean_replay.go:530 +0x3c

Previous read at 0x00c0000a4010 by goroutine 11:
  context.(*cancelCtx).Done()
      /go/src/context/context.go:400 +0x1
  github.com/quic-go/quic-go.(*Transport).dial()
      /m/transport.go:260 +0x1
==================
`
	if got := raceReport(deadline); got != "" {
		t.Fatalf("helper race reported: %s", got)
	}
	program := `==================
WARNING: DATA RACE
Read at 0x00c00009a060 by goroutine 10:
  github.com/quic-go/quic-go.(*Transport).close()
      /m/transport.go:496 +0x151
  github.com/quic-go/quic-go.SeqConcTransportDialClose.func1()
      /m/go2lean_conc_test.go:31 +0x92

Previous write at 0x00c00009a060 by goroutine 11:
  github.com/quic-go/quic-go.(*Transport).dial.(*Transport).init.func1()
      /m/transport.go:387 +0x247
  github.com/quic-go/quic-go.TestGo2LeanPoC_cx1.func3()
      /m/zz_go2lean_poc_test.go:40 +0x209
==================
`
	if got := raceReport(program); !strings.Contains(got, "(*Transport).close") {
		t.Fatalf("program race not reported: %q", got)
	}
}

// A goroutine the replay controller starts runs the program's code above
// go2leanGoAny and go2leanInvoke: a race between two of them is the
// program's (evaluation 3's TestShutdownSuccess, #15).
func TestRaceReportControllerStartedGoroutines(t *testing.T) {
	started := `==================
WARNING: DATA RACE
Read at 0x00c00009e018 by goroutine 99:
  github.com/eapache/go-resiliency/batcher.TestShutdownSuccess.func1()
      /m/batcher_test.go:55 +0x59
  github.com/eapache/go-resiliency/batcher.(*Batcher).batch()
      /m/batcher.go:94 +0x381
  runtime.call16()
      /go/src/runtime/asm_amd64.s:775 +0x42
  reflect.Value.Call()
      /go/src/reflect/value.go:368 +0xb5
  github.com/eapache/go-resiliency/batcher.go2leanInvoke()
      /m/zz_go2lean_replay.go:383 +0x524
  github.com/eapache/go-resiliency/batcher.go2leanGoAny.func1()
      /m/zz_go2lean_replay.go:425 +0x136

Previous write at 0x00c00009e018 by goroutine 95:
  github.com/eapache/go-resiliency/batcher.TestShutdownSuccess.func1()
      /m/batcher_test.go:55 +0x70
  github.com/eapache/go-resiliency/batcher.(*Batcher).batch()
      /m/batcher.go:94 +0x381
  github.com/eapache/go-resiliency/batcher.go2leanInvoke()
      /m/zz_go2lean_replay.go:383 +0x5
==================
`
	if got := raceReport(started); !strings.Contains(got, "TestShutdownSuccess.func1") {
		t.Fatalf("race between controller-started goroutines not reported: %q", got)
	}
}
