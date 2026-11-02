// Package memgov keeps the search processes a run starts under a memory
// limit (#45): a run on a large package degrades (one property's search
// stops, reported as out of memory) instead of the whole run being killed
// by the operating system.
//
// Run starts a process and samples the resident memory of every process
// started this way (and of go2lean itself) from /proc. While the total is
// over the limit, the largest of them is killed and its Run returns an
// *OutOfMemory error. A new process waits to start while the total is
// over the admission mark (three quarters of the limit) and another one
// is running, so that parallel searches do not all grow at once.
//
// Where /proc is missing (not Linux), nothing is sampled and Run is
// cmd.Run.
package memgov

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OutOfMemory is the error of a process the governor killed.
type OutOfMemory struct {
	RSS, Limit uint64 // bytes: the process's resident memory when killed, the limit
}

func (e *OutOfMemory) Error() string {
	return fmt.Sprintf("out of memory (it used %s; the run's limit is %s)", MB(e.RSS), MB(e.Limit))
}

// MB renders a byte count in megabytes.
func MB(b uint64) string { return fmt.Sprintf("%d MB", b>>20) }

// IsOutOfMemory reports whether err is (or wraps) an *OutOfMemory.
func IsOutOfMemory(err error) (*OutOfMemory, bool) {
	var o *OutOfMemory
	ok := errors.As(err, &o)
	return o, ok
}

type proc struct {
	p      *os.Process
	rss    uint64
	killed bool
}

var (
	mu       sync.Mutex
	cond     = sync.NewCond(&mu)
	limit    uint64
	limitSet bool
	procs    = map[*proc]bool{}
	interval = 200 * time.Millisecond
	stopped  int
	rssOf    = procRSS // replaced in tests
)

// SetLimit sets the limit in megabytes: 0 is the default (four fifths of
// the memory the container may use, the smaller of its cgroup limit and
// the machine's memory), a negative value none.
func SetLimit(mb int) {
	mu.Lock()
	defer mu.Unlock()
	setLimit(mb)
}

func setLimit(mb int) {
	switch {
	case mb < 0:
		limit = 0
	case mb == 0:
		limit = available() / 5 * 4
	default:
		limit = uint64(mb) << 20
	}
	limitSet = true
}

// Limit is the limit in bytes (0: none).
func Limit() uint64 {
	mu.Lock()
	defer mu.Unlock()
	if !limitSet {
		setLimit(0)
	}
	return limit
}

// Stopped is how many processes the governor has killed.
func Stopped() int {
	mu.Lock()
	defer mu.Unlock()
	return stopped
}

// Run runs cmd under the limit: like cmd.Run, but it may wait to start,
// and it returns an *OutOfMemory error when the governor killed it.
func Run(cmd *exec.Cmd) error {
	lim := Limit()
	if lim == 0 || rssOf(os.Getpid()) == 0 {
		return cmd.Run()
	}
	mu.Lock()
	for len(procs) > 0 && total() > lim/4*3 {
		cond.Wait()
	}
	if err := cmd.Start(); err != nil {
		mu.Unlock()
		return err
	}
	p := &proc{p: cmd.Process}
	procs[p] = true
	mu.Unlock()

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				sample(lim)
			}
		}
	}()
	err := cmd.Wait()
	close(done)

	mu.Lock()
	delete(procs, p)
	killed, rss := p.killed, p.rss
	cond.Broadcast()
	mu.Unlock()
	if killed {
		return &OutOfMemory{RSS: rss, Limit: lim}
	}
	return err
}

// total is go2lean's resident memory and its live processes'; the caller
// holds mu. A process already killed no longer counts: its memory is on
// its way out.
func total() uint64 {
	t := rssOf(os.Getpid())
	for p := range procs {
		if !p.killed {
			t += p.rss
		}
	}
	return t
}

// sample refreshes every process's resident memory and, while the total is
// over the limit, kills the largest live one.
func sample(lim uint64) {
	mu.Lock()
	defer mu.Unlock()
	for p := range procs {
		if !p.killed {
			if r := rssOf(p.p.Pid); r > 0 {
				p.rss = r
			}
		}
	}
	for total() > lim {
		var big *proc
		for p := range procs {
			if !p.killed && (big == nil || p.rss > big.rss) {
				big = p
			}
		}
		if big == nil {
			return
		}
		big.killed = true
		stopped++
		// Through the process handle, not the pid: the pid may be reused
		// once the process has been waited for.
		big.p.Kill()
	}
	cond.Broadcast()
}

// procRSS is a process's resident memory in bytes (0 when unknown).
func procRSS(pid int) uint64 {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}

// available is the memory the container may use: the smaller of the
// machine's memory and its cgroup's limit (v2 or v1), 0 when unknown.
func available() uint64 {
	best := meminfoTotal()
	for _, path := range cgroupLimitFiles() {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || v == 0 { // "max": no limit
			continue
		}
		if best == 0 || v < best {
			best = v
		}
	}
	return best
}

func meminfoTotal() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
			if err == nil {
				return kb << 10
			}
		}
	}
	return 0
}

// cgroupLimitFiles lists the files that may hold this process's cgroup
// memory limit: its own cgroup's and the root's, for v2 and v1.
func cgroupLimitFiles() []string {
	files := []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return files
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.SplitN(l, ":", 3)
		if len(parts) != 3 || parts[2] == "/" {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			files = append(files, "/sys/fs/cgroup"+parts[2]+"/memory.max")
		case strings.Contains(","+parts[1]+",", ",memory,"):
			files = append(files, "/sys/fs/cgroup/memory"+parts[2]+"/memory.limit_in_bytes")
		}
	}
	return files
}
