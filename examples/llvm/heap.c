// Ground truth for the LLVM line's heap (#269): malloc, calloc, realloc
// and free, a use after free, a double free, and their correct uses.
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

// A use after free when flag is set.
int32_t uaf(int32_t flag) {
    int32_t *p = malloc(4 * sizeof(int32_t));
    if (!p) return 0;
    p[0] = 7;
    if (flag) free(p);
    int32_t v = p[0];
    if (!flag) free(p);
    return v;
}

// A double free when flag is 3.
int32_t dfree(int32_t flag) {
    char *p = calloc(8, 1);
    if (!p) return 0;
    free(p);
    if (flag == 3) free(p);
    return 1;
}

// Grows a buffer and writes past the old end: correct.
int32_t grow(int32_t n) {
    if (n < 0 || n > 16) return 0;
    char *p = malloc(4);
    if (!p) return 0;
    memset(p, 1, 4);
    char *q = realloc(p, 4 + (size_t)n);
    if (!q) { free(p); return 0; }
    int32_t s = q[0];
    if (n > 0) q[4 + n - 1] = 2;
    free(q);
    return s;
}
