// go2lean-llvm-flags: -memory -fuzz 10s
// Ground truth for strtod and errno (#271): a JSON-style number parser that
// uses strtod, as parson and cJSON do. The model reads the digits from its
// own memory and sets endptr, so the parser runs in the model instead of
// getting stuck on the libc call.
#include <stdint.h>
#include <stdlib.h>

static const int32_t bucket[4] = {10, 20, 30, 40};

// Parses a number and indexes a 4-entry table by its integer part,
// unchecked: a value of 4 or more reads past the table.
int32_t classify_bad(const char *s) {
    char *end;
    double v = strtod(s, &end);
    if (end == s) return -1;         // not a number
    return bucket[(int32_t)v];
}

// The same, bounded: correct.
int32_t classify_ok(const char *s) {
    char *end;
    double v = strtod(s, &end);
    if (end == s || v < 0 || v >= 4) return -1;
    return bucket[(int32_t)v];
}

// strtod advances endptr past the number; the remaining text is parsed on.
int32_t count_numbers(const char *s) {
    int32_t n = 0;
    char *end;
    while (*s) {
        double v = strtod(s, &end);
        if (end == s) { s++; continue; }
        (void)v; n++; s = end;
    }
    return n;
}
