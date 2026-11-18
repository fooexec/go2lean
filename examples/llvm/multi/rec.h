#include <stddef.h>
#include <stdint.h>
// A record reader in two files (#271): the length check is in one file,
// the copy in the other.
typedef struct { uint8_t tag; uint8_t len; uint8_t data[8]; } rec;
int rec_parse(rec *r, const uint8_t *buf, size_t n);
int rec_parse_bad(rec *r, const uint8_t *buf, size_t n);
