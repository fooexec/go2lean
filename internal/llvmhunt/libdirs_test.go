package llvmhunt

import (
	"strings"
	"testing"
)

func TestLibDirs(t *testing.T) {
	o := Options{LDFlags: []string{"-L/a/lib", "-lfrr", "-Wl,-rpath,/b/lib", "-Wl,-rpath=/c/lib", "-Wl,--as-needed"}}
	if got := strings.Join(o.libDirs(), " "); got != "/a/lib /b/lib /c/lib" {
		t.Errorf("libDirs: %s", got)
	}
}
