// Package refinedeep is a dependency of examples/refine whose answer is
// computed five calls deep, past the depth (4) that go2lean -deps
// translates by default: its last call is havoc until the refinement loop
// translates it (#85). Each level has a loop, so none is a small helper
// (those are translated at any depth).
package refinedeep

// Level1 is the entry point: the last decimal digit of x.
func Level1(x uint8) int {
	s := 0
	for j := uint8(0); j < x%4; j++ {
		if j%2 == 0 {
			s++
		} else {
			s--
		}
	}
	if s > 0 {
		s = 0
	}
	return Level2(x) + s*0
}

func Level2(x uint8) int {
	s := 0
	for j := uint8(0); j < x%4; j++ {
		if j%2 == 0 {
			s++
		} else {
			s--
		}
	}
	if s > 0 {
		s = 0
	}
	return Level3(x) + s*0
}

func Level3(x uint8) int {
	s := 0
	for j := uint8(0); j < x%4; j++ {
		if j%2 == 0 {
			s++
		} else {
			s--
		}
	}
	if s > 0 {
		s = 0
	}
	return Level4(x) + s*0
}

func Level4(x uint8) int {
	s := 0
	for j := uint8(0); j < x%4; j++ {
		if j%2 == 0 {
			s++
		} else {
			s--
		}
	}
	if s > 0 {
		s = 0
	}
	return Level5(x) + s*0
}

// Level5 is never negative and never above 9.
func Level5(x uint8) int {
	s := 0
	for j := uint8(0); j < x%4; j++ {
		if j%2 == 0 {
			s++
		} else {
			s--
		}
	}
	if s > 0 {
		s = 0
	}
	return int(x%10) + s*0
}
