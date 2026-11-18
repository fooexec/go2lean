// Ground truth for floating point (#269): float and double values, their
// arithmetic, comparisons and conversions, as cJSON's cJSON_CreateNumber
// uses them.
#include <limits.h>
#include <stdint.h>

static const int32_t table[8] = {1, 2, 3, 4, 5, 6, 7, 8};

// An index from a double, unchecked: out of bounds once x reaches 16.
int32_t scaled(double x) {
    int32_t i = (int32_t)(x * 0.5);
    return table[i];
}

// The same, guarded (a NaN fails both comparisons): correct.
int32_t scaled_ok(double x) {
    if (!(x >= 0.0 && x < 16.0)) return 0;
    return table[(int32_t)(x * 0.5)];
}

// A float bucket, unchecked below zero.
int32_t bucket(float f) {
    int32_t b = (int32_t)(f / 4.0f);
    if (b > 7) b = 7;
    return table[b];
}

struct number { double value; int32_t valueint; };

// cJSON_CreateNumber's saturation: correct.
static void set_number(struct number *n, double num) {
    n->value = num;
    if (num >= INT_MAX) n->valueint = INT_MAX;
    else if (num <= (double)INT_MIN) n->valueint = INT_MIN;
    else n->valueint = (int32_t)num;
}

// An integer survives the trip through a double.
__attribute__((annotate("go2lean:spec")))
int spec_number(int32_t k) {
    struct number n;
    set_number(&n, (double)k);
    return n.valueint == k && n.value == (double)k;
}

// A planted bug: halving rounds toward zero, so the round trip fails on odd
// negative values.
__attribute__((annotate("go2lean:spec")))
int spec_halve(int32_t k) {
    double h = (double)(k / 2);
    return h * 2.0 == (double)k || k % 2 != 0 && k > 0;
}
