package ir

import "testing"

func TestBigConst(t *testing.T) {
	cases := []struct {
		bits  string
		width int
		want  bool
	}{
		{"1000", 64, false},
		{"65535", 64, false},
		{"65536", 64, true},
		{"4294967296", 64, true},
		{"4611686018427387904", 64, true},
		{"18446744073709551615", 64, false}, // -1
		{"18446744073709551614", 64, false}, // -2
		{"18446744073709486080", 64, true},  // -65536
		{"255", 8, false},
		{"4294967295", 32, false}, // -1
		{"2147483648", 32, true},  // -2^31
		{"0", 64, false},
	}
	for _, c := range cases {
		if got := bigConst(c.bits, c.width); got != c.want {
			t.Errorf("bigConst(%s, %d) = %v, want %v", c.bits, c.width, got, c.want)
		}
	}
}
