// Package bench has twelve functions that panic only on inputs a random or
// coverage-guided search reaches with difficulty.
package bench

// B01: a multiplicative hash that matches one value.
func B01(x uint32) int {
	t := [4]int{1, 2, 3, 4}
	if x*2654435761 == 0xdeadbeef {
		return t[int(x&7)+4]
	}
	return t[x&3]
}

// B02: an 8-bit checksum over the payload must match the trailer byte.
func B02(b []byte) int {
	if len(b) < 3 {
		return 0
	}
	n := len(b) - 1
	var sum byte
	for i := 0; i < n; i++ {
		sum = sum*31 + b[i] + 7
	}
	if sum == b[n] && b[0] == 0x7f {
		return int(b[n+1])
	}
	return 1
}

// B03: a length field that must equal the rest of the packet.
func B03(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	n := int(b[0])<<8 | int(b[1])
	if n == len(b)-2 && b[2] == 0xa5 {
		return int(b[n+2])
	}
	return 0
}

// B04: an overflowing size computation.
func B04(n int32, b []byte) int {
	if n < 0 {
		return 0
	}
	m := n * 65536
	if int(m) < len(b) && n > 100 {
		return int(b[n])
	}
	return 1
}

// B05: four bytes spell a magic number.
func B05(b []byte) int {
	if len(b) < 8 {
		return 0
	}
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	if v == 0x1badc0de {
		return int(b[int(b[4])+8])
	}
	return 1
}

// B06: a rotate-and-xor mix equals a constant.
func B06(x uint32, y uint32) int {
	t := [3]int{1, 2, 3}
	m := (x<<13 | x>>19) ^ y
	if m == 0x12345678 && x&1 == 1 {
		return t[y%7+3]
	}
	return 0
}

// B07: two linear equations on two inputs.
func B07(a, b uint16) int {
	t := [2]int{1, 2}
	if a+b == 0x1234 && a-b == 0x0f00 {
		return t[a&7+2]
	}
	return 0
}

// B08: the sum of the bytes equals a target.
func B08(b []byte) int {
	if len(b) != 6 {
		return 0
	}
	s := 0
	for _, c := range b {
		s += int(c)
	}
	if s == 1000 {
		return int(b[s])
	}
	return 0
}

// B09: a state machine over the first bytes.
func B09(b []byte) int {
	if len(b) < 5 {
		return 0
	}
	if b[0] == 'G' && b[1] == 'E' && b[2] == 'T' && b[3] == ' ' && b[4] == '/' {
		return int(b[len(b)])
	}
	return 0
}

// B10: three fields xor to a constant.
func B10(a, b, c uint32) int {
	t := [2]int{1, 2}
	if a^b^c == 0xcafebabe && a > b && b > c {
		return t[int(a%5)+2]
	}
	return 0
}

// B11: a division whose divisor is a product of three bytes minus a constant.
func B11(a, b, c uint8) int {
	d := int(a)*int(b)*int(c) - 90000
	if a > 10 && b > 10 {
		return 1000 / d
	}
	return 0
}

// B12: a bounds check that is off by one on a computed offset.
func B12(b []byte, k uint8) int {
	if len(b) < 16 {
		return 0
	}
	off := int(k)>>4 + int(k&15)*2
	if off <= len(b) {
		return int(b[off])
	}
	return 0
}
