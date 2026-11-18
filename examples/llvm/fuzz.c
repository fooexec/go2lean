// go2lean-llvm-flags: -fuzz 10s
// Ground truth for libFuzzer (#270): code the model does not run. strtol is
// not modelled, so its result is havoc: the model's violation rests on a
// guess, its replay does not reproduce, and the fuzzer, which runs the real
// strtol, finds the input.
#include <stdint.h>
#include <stdlib.h>

static const int32_t weight[10] = {6, 2, 5, 5, 4, 5, 6, 3, 7, 6};

// A digit's weight, with no check below zero: "-1" reads before the table.
int32_t weight_of(const char *s) {
    long n = strtol(s, NULL, 10);
    if (n > 9) return -1;
    return weight[n];
}

// The same, checked: the fuzzer finds nothing.
int32_t weight_of_ok(const char *s) {
    long n = strtol(s, NULL, 10);
    if (n < 0 || n > 9) return -1;
    return weight[n];
}
