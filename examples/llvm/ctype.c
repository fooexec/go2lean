// go2lean-llvm-flags: -memory -fuzz 10s
// Ground truth for glibc's ctype functions (#271): isdigit/isspace/tolower
// via __ctype_b_loc / __ctype_tolower_loc, which the model holds as
// synthetic tables so a parser that uses them is not stuck.
#include <ctype.h>
#include <stdint.h>

static int32_t digit_buf[4];

// Collects a run of digits into a 4-int buffer, unchecked: a fifth digit
// writes past it.
int32_t collect_bad(const char *s) {
    int32_t n = 0;
    while (isdigit((unsigned char)*s)) digit_buf[n++] = *s++ - '0';
    return n;
}

// The same, bounded: correct.
int32_t collect_ok(const char *s) {
    int32_t n = 0;
    while (n < 4 && isdigit((unsigned char)*s)) digit_buf[n++] = *s++ - '0';
    return n;
}

// Skips spaces, then lowercases the first non-space: exercises isspace and
// tolower, no memory error.
int32_t first_lower(const char *s) {
    while (isspace((unsigned char)*s)) s++;
    return tolower((unsigned char)*s);
}
