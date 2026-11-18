#include <string.h>
#include "rec.h"
int rec_copy(rec *r, const uint8_t *src, size_t len) {
    r->len = (uint8_t)len;
    memcpy(r->data, src, len);
    return (int)len;
}
