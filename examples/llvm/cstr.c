// Ground truth for the LLVM line's C string functions (#269): strlen on a
// buffer without its terminator, strcpy into a buffer too small, and
// correct uses of strcmp, strncmp, strchr and tolower.
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <stdint.h>

// strlen of n bytes with no terminator when flag is set.
int32_t unterminated(int32_t flag, int32_t n) {
    if (n < 1 || n > 8) return 0;
    char *p = malloc((size_t)n);
    if (!p) return 0;
    memset(p, 'a', (size_t)n);
    if (!flag) p[n - 1] = 0;
    int32_t l = (int32_t)strlen(p);
    free(p);
    return l;
}

// strcpy of a string of n letters into 4 bytes.
int32_t copy(int32_t n) {
    if (n < 0 || n > 8) return 0;
    char src[9];
    memset(src, 'b', (size_t)n);
    src[n] = 0;
    char *dst = malloc(4);
    if (!dst) return 0;
    strcpy(dst, src);
    int32_t c = dst[0];
    free(dst);
    return c;
}

// Correct: compares and searches a constant key.
int32_t lookup(const char *key) {
    if (strcmp(key, "name") == 0) return 1;
    if (strncmp(key, "na", 2) == 0) return 2;
    const char *dot = strchr(key, '.');
    if (dot && tolower(dot[1]) == 'x') return 3;
    return 0;
}
