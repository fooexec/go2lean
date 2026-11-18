// Ground truth for ptrtoint (#269): C's pointer difference, which clang
// lowers at -O0 to two ptrtoint and a sub, as cJSON's parser does with
// input_end - input_pointer.
#include <stddef.h>
#include <stdint.h>

// Counts the bytes before the first NUL, scanning at most len bytes, with
// the remaining length computed as a pointer difference: correct.
int32_t span(const char *buf, size_t len) {
    const char *end = buf + len;
    const char *p = buf;
    while ((size_t)(end - p) > 0 && *p) p++;
    return (int32_t)(p - buf);
}

// Off by one: reads one byte past the end when there is no NUL.
int32_t span_bad(const char *buf, size_t len) {
    const char *end = buf + len;
    const char *p = buf;
    while ((end - p) >= 0 && *p) p++;
    return (int32_t)(p - buf);
}
