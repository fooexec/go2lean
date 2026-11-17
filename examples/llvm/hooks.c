// Ground truth for the LLVM line (#269): malloc and free reached through a
// table of function pointers, as cJSON's hooks do; a use after free when n is 5.
#include <stdlib.h>
#include <stdint.h>
typedef struct { void *(*allocate)(size_t); void (*deallocate)(void *); } hooks;
static hooks global_hooks = { malloc, free };
int32_t via_hooks(int32_t n) {
    if (n < 0 || n > 8) return 0;
    int32_t *p = global_hooks.allocate(4 * (size_t)n + 4);
    if (!p) return 0;
    p[n] = 1;
    int32_t v = p[n];
    global_hooks.deallocate(p);
    return v + (n == 5 ? p[0] : 0);
}
