package constraint

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A search killed for a trial past the wall clock limit is a stopped
// search, reported as such, not an error; other errors are not.
func TestSearchStopped(t *testing.T) {
	err := fmt.Errorf("random: %w", &trialTimeout{budget: 10 * time.Second})
	msg, ok := searchStopped(err)
	if !ok || !strings.Contains(msg, "one trial ran past 3 times the 10s budget") {
		t.Fatalf("searchStopped = %q, %v", msg, ok)
	}
	if StoppedForMemory("random: stopped, " + msg) {
		t.Fatal("a timeout read as out of memory")
	}
	if _, ok := searchStopped(fmt.Errorf("go2lean-search: exit status 1")); ok {
		t.Fatal("a failed search read as stopped")
	}
}
