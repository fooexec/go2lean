// go2lean-llvm-flags: -memory -specs-only -fuzz 10s -lib examples/llvm/multi/copy.c,examples/llvm/multi/rec.c -cflags -Iexamples/llvm/multi
// Ground truth for a library of several files (#271): the spec includes
// the header only; go2lean links rec.c and copy.c.
#include <stdlib.h>
#include <string.h>
#include "rec.h"

__attribute__((annotate("go2lean:spec")))
int spec_parse(const uint8_t *buf, size_t len) {
    rec *r = malloc(sizeof *r);
    (void)rec_parse(r, buf, len);
    free(r);
    return 1;
}

__attribute__((annotate("go2lean:spec")))
int spec_parse_bad(const uint8_t *buf, size_t len) {
    rec *r = malloc(sizeof *r);
    (void)rec_parse_bad(r, buf, len);
    free(r);
    return 1;
}
