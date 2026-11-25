package llvmhunt

import "testing"

func TestSanitizerReport(t *testing.T) {
	for in, want := range map[string]string{
		"==1==ERROR: AddressSanitizer: heap-buffer-overflow on address": "==1==ERROR: AddressSanitizer: heap-buffer-overflow on address",
		"x.c:3:5: runtime error: signed integer overflow":                 "x.c:3:5: runtime error: signed integer overflow",
		// FRR's own assertion, then libFuzzer's report of the abort.
		"2026/11/25 lib/stream.c:355: stream_get(): assertion (0) failed\n==1== ERROR: libFuzzer: deadly signal": "2026/11/25 lib/stream.c:355: stream_get(): assertion (0) failed",
		"fuzz: x.c:9: main: Assertion `n < 4' failed.": "fuzz: x.c:9: main: Assertion `n < 4' failed.",
		// A sanitizer report wins over an assertion before it.
		"assertion (0) failed\n==1==ERROR: AddressSanitizer: SEGV": "==1==ERROR: AddressSanitizer: SEGV",
		"==1== ERROR: libFuzzer: deadly signal":                     "",
	} {
		if got := sanitizerReport(in); got != want {
			t.Errorf("sanitizerReport(%q) = %q, want %q", in, got, want)
		}
	}
}
