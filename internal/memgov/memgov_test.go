package memgov

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A child of the test binary that holds GO2LEAN_MEMGOV_HOG megabytes for
// GO2LEAN_MEMGOV_HOLD milliseconds, then exits 0.
func TestMain(m *testing.M) {
	if mb := os.Getenv("GO2LEAN_MEMGOV_HOG"); mb != "" {
		n, _ := strconv.Atoi(mb)
		ms, _ := strconv.Atoi(os.Getenv("GO2LEAN_MEMGOV_HOLD"))
		buf := make([]byte, n<<20)
		for i := 0; i < len(buf); i += 4096 {
			buf[i] = 1
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		os.Exit(int(buf[0]) - 1)
	}
	os.Exit(m.Run())
}

func hog(mb, ms int) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "GO2LEAN_MEMGOV_HOG="+strconv.Itoa(mb), "GO2LEAN_MEMGOV_HOLD="+strconv.Itoa(ms))
	return cmd
}

func needProc(t *testing.T) {
	if procRSS(os.Getpid()) == 0 {
		t.Skip("no /proc")
	}
}

// withLimit runs f with the limit set to go2lean's own memory plus mb.
func withLimit(t *testing.T, mb int, f func()) {
	needProc(t)
	self := int(procRSS(os.Getpid()) >> 20)
	SetLimit(self + mb)
	interval = 20 * time.Millisecond
	defer func() { SetLimit(-1); interval = 200 * time.Millisecond }()
	f()
}

func TestRunUnderLimit(t *testing.T) {
	withLimit(t, 400, func() {
		if err := Run(hog(50, 300)); err != nil {
			t.Fatalf("a process under the limit: %v", err)
		}
	})
}

func TestKillOverLimit(t *testing.T) {
	withLimit(t, 100, func() {
		err := Run(hog(300, 3000))
		o, ok := IsOutOfMemory(err)
		if !ok {
			t.Fatalf("a process over the limit: got %v, want out of memory", err)
		}
		if o.RSS == 0 || o.Limit == 0 {
			t.Fatalf("out of memory without sizes: %+v", o)
		}
	})
}

// Of two processes that together go over the limit, the larger is killed
// and the other finishes.
func TestKillLargest(t *testing.T) {
	withLimit(t, 150, func() {
		var wg sync.WaitGroup
		var small, big error
		wg.Add(2)
		go func() { defer wg.Done(); small = Run(hog(60, 5000)) }()
		time.Sleep(time.Second) // the small one is admitted and has its memory
		go func() { defer wg.Done(); big = Run(hog(120, 5000)) }()
		wg.Wait()
		if small != nil {
			t.Errorf("small process: %v", small)
		}
		if _, ok := IsOutOfMemory(big); !ok {
			t.Errorf("large process: got %v, want out of memory", big)
		}
	})
}

func TestNoLimit(t *testing.T) {
	SetLimit(-1)
	if err := Run(hog(10, 10)); err != nil {
		t.Fatal(err)
	}
}

func TestAvailable(t *testing.T) {
	needProc(t)
	if available() == 0 {
		t.Fatal("no memory size found")
	}
}
