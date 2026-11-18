//go:build llvm15 || llvm16 || llvm17 || llvm18 || llvm19 || llvm20 || llvm21 || llvm22 || llvm23

package llvm

import "testing"

// ctypeMask matches glibc's C-locale classification table (the values a
// fresh __ctype_b_loc() dump gives).
func TestCtypeMask(t *testing.T) {
	for c, want := range map[int]uint16{
		'\t': 8195, ' ': 24577, '0': 55304, '@': 49156, 'A': 54536,
		'z': 50696, 0: 2, 127: 2, 200: 0,
	} {
		if got := ctypeMask(c); got != want {
			t.Errorf("ctypeMask(%d) = %d, want %d", c, got, want)
		}
	}
}
