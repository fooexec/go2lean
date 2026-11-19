package mini

func ShlVar(length uint8) int64 { return 1 << (length * 8) }

func Mul8(l uint8) uint8 { return l * 8 }

func AndNot(a, m int64) int64 { return a &^ m }

func Or(a, b int64) int64 { return a | b }

func Div2(w int64) int64 { return w / 2 }

func Cmp(c, e, h int64) int64 {
	if c <= e-h && c < 1<<62-h {
		return c + h
	}
	return c
}

func Full(length uint8, largest, truncated int64) int64 {
	expected := largest + 1
	win := int64(1 << (length * 8))
	hwin := win / 2
	mask := win - 1
	candidate := (expected & ^mask) | truncated
	if candidate <= expected-hwin && candidate < 1<<62-win {
		return candidate + win
	}
	if candidate > expected+hwin && candidate >= win {
		return candidate - win
	}
	return candidate
}

func IfSimple(c, e int64) int64 {
	if c <= e {
		return c + 1
	}
	return c
}

func IfAnd(c, e, h int64) int64 {
	if c <= e && c < h {
		return c + 1
	}
	return c
}

func IfConst(c, h int64) int64 {
	if c < 1<<62-h {
		return c + 1
	}
	return c
}

func TwoIfs(c, e int64) int64 {
	if c <= e {
		return c + 1
	}
	if c > e+5 {
		return c - 1
	}
	return c
}

func ThreeIfs(c, e, h int64) int64 {
	if c <= e {
		return c + h
	}
	if c > e+5 {
		return c - h
	}
	if c == 0 {
		return 7
	}
	return c
}

func ConstSmall(c, h int64) int64 {
	if c < 1000-h {
		return c + 1
	}
	return c
}

func Const32(c, h int64) int64 {
	if c < 1<<32-h {
		return c + 1
	}
	return c
}

func Const40(c, h int64) int64 {
	if c < 1<<40-h {
		return c + 1
	}
	return c
}

func Const50(c, h int64) int64 {
	if c < 1<<50-h {
		return c + 1
	}
	return c
}

func Const62Add(c int64) int64 { return c + 1<<62 }
