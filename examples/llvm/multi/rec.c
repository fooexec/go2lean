#include <string.h>
#include "rec.h"
int rec_copy(rec *r, const uint8_t *src, size_t len);
// Checks the record's length against its buffer: correct.
int rec_parse(rec *r, const uint8_t *buf, size_t n) {
    if (n < 2 || buf[1] > n - 2 || buf[1] > sizeof r->data) return -1;
    r->tag = buf[0];
    return rec_copy(r, buf + 2, buf[1]);
}
// Checks the length against the buffer only, not against data[8]: a write
// past the record (the kind of bug a CVE is filed for).
int rec_parse_bad(rec *r, const uint8_t *buf, size_t n) {
    if (n < 2 || buf[1] > n - 2) return -1;
    r->tag = buf[0];
    return rec_copy(r, buf + 2, buf[1]);
}
