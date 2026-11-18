// Ground truth for inttoptr and out parameters (#269), as stb_image uses
// them: a buffer aligned by integer arithmetic on its address, a NULL made
// from an integer, and results written through int *.
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// stb_image's stbi__errpuc: an error returns NULL through an integer.
static unsigned char *fail(void) { return (unsigned char *)(size_t)0; }

// Copies buf into a 16-byte aligned scratch buffer and sums it, reading the
// 16 bytes after the copy: past the 15 bytes of slack.
int32_t sum_aligned_bad(const uint8_t *buf, size_t len) {
    if (len == 0 || len > 64) return 0;
    uint8_t *raw = malloc(len + 15);
    uint8_t *p = (uint8_t *)(((size_t)raw + 15) & ~(size_t)15);
    memcpy(p, buf, len);
    int32_t s = 0;
    for (size_t i = 0; i < len + 16; i++) s += p[i];
    free(raw);
    return s;
}

// The same, correct: the extra 15 bytes cover the alignment.
int32_t sum_aligned(const uint8_t *buf, size_t len) {
    if (len == 0 || len > 64) return 0;
    uint8_t *raw = malloc(len + 15);
    uint8_t *p = (uint8_t *)(((size_t)raw + 15) & ~(size_t)15);
    memcpy(p, buf, len);
    int32_t s = 0;
    for (size_t i = 0; i < len; i++) s += p[i];
    free(raw);
    return s;
}

// Out parameters: the caller passes the addresses of its variables, never
// NULL, so writing them is no undefined behaviour.
unsigned char *dims(const char *s, int *w, int *h) {
    *w = 0;
    *h = 0;
    if (s[0] == 0) return fail();
    *w = s[0];
    *h = s[1] ? s[1] : 1;
    return (unsigned char *)s;
}
