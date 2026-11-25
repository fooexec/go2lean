package llvmhunt

import (
	"strings"
	"testing"
	"time"

	"github.com/fooexec/go2lean/internal/contract"
)

func TestResourceVerdict(t *testing.T) {
	for _, c := range []struct {
		base    string
		exit    int
		elapsed time.Duration
		status  contract.VerdictStatus
		reason  string
	}{
		{"timeout-ab", 0, 7 * time.Millisecond, contract.VerdictSpurious, "not_reproduced"},
		{"oom-ab", 0, time.Second, contract.VerdictSpurious, "not_reproduced"},
		{"timeout-ab", 0, 9 * time.Second, contract.VerdictInconclusive, "fuzzer_timeout"},
		{"timeout-ab", 1, time.Millisecond, contract.VerdictInconclusive, "fuzzer_timeout"},
		{"oom-ab", 1, time.Millisecond, contract.VerdictInconclusive, "fuzzer_out_of_memory"},
	} {
		s, d := resourceVerdict(c.base, c.exit, c.elapsed)
		if s != c.status || !strings.Contains(d.Reason, c.reason) {
			t.Errorf("%s exit %d in %v: %s %q, want %s %q", c.base, c.exit, c.elapsed, s, d.Reason, c.status, c.reason)
		}
	}
}
