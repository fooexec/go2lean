package floats

// go2lean:spec Clamp
// SpecClampInRange: "Clamp keeps x in [0, 1]".
func SpecClampInRange(x float64, ret float64) bool {
	return ret >= 0 && ret <= 1
}
