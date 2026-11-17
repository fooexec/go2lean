// Ground truth for specs in C (#270): a function marked
// __attribute__((annotate("go2lean:spec"))) returns non-zero when its
// property holds. A round trip that a planted bug breaks, and a bound
// that holds.
#include <stdint.h>

#define SPEC __attribute__((annotate("go2lean:spec")))

// Writes n in decimal into out (at least 4 bytes); returns the length.
// Bug: from 100 up it drops the hundreds digit.
int32_t format_u8(uint8_t n, char *out) {
    int32_t i = 0;
    if (n >= 100) n %= 100;  /* the bug: should write n / 100 first */
    if (n >= 10) out[i++] = (char)('0' + n / 10);
    out[i++] = (char)('0' + n % 10);
    out[i] = 0;
    return i;
}

// Reads a decimal of up to three digits.
int32_t parse_u8(const char *s) {
    int32_t v = 0;
    for (int32_t i = 0; i < 3 && s[i] >= '0' && s[i] <= '9'; i++) v = v * 10 + (s[i] - '0');
    return v;
}

int32_t clamp(int32_t x) {
    if (x < 0) return 0;
    if (x > 100) return 100;
    return x;
}

SPEC int32_t spec_round_trip(uint8_t n) {
    char buf[4];
    format_u8(n, buf);
    return parse_u8(buf) == n;
}

SPEC int32_t spec_clamp(int32_t x) {
    int32_t c = clamp(x);
    return 0 <= c && c <= 100 && (x < 0 || x > 100 || c == x);
}
