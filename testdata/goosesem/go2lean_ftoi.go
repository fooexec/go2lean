package semantics

import "math"

// go2lean's own tests (#207): float-to-integer conversions out of range and
// of NaN, which Go leaves to the machine, as amd64 converts them (the model
// converts as the machine the replay runs on; CI's is amd64).

func toI64(x float64) int64   { return int64(x) }
func toI32(x float64) int32   { return int32(x) }
func toI16(x float64) int16   { return int16(x) }
func toI8(x float64) int8     { return int8(x) }
func toU64(x float64) uint64  { return uint64(x) }
func toU32(x float64) uint32  { return uint32(x) }
func toU16(x float64) uint16  { return uint16(x) }
func toU8(x float64) uint8    { return uint8(x) }
func f32ToU64(x float32) uint64 { return uint64(x) }

func testFtoiOutOfRange() bool {
	nan := math.NaN()
	return toI64(1e19) == math.MinInt64 && toI64(nan) == math.MinInt64 &&
		toI32(5e9) == math.MinInt32 && toI32(-3e9) == math.MinInt32 &&
		toI16(2e9) == -27648 && toI8(70000) == 112 && toI16(-3e9) == 0 &&
		toU64(1e19) == 10000000000000000000 && toU64(-1e19) == 9223372036854775808 &&
		toU64(nan) == 9223372036854775808 && toU64(-3e9) == 18446744070709551616-0 &&
		toU32(4.2e9) == 4200000000 && toU32(-3e9) == 1294967296 && toU32(5e9) == 705032704 &&
		toU16(-70000) == 61072 && toU8(-300) == 212 && toU16(4.2e9) == 0 &&
		f32ToU64(1e19) == 9999999980506447872
}

func testFtoiInRange() bool {
	return toI64(-2.9) == -2 && toU8(255.9) == 255 && toI32(-0.5) == 0 && toU64(9.3e18) == 9300000000000000000
}
