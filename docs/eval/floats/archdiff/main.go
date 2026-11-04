//go:build ignore

package main

import (
	"fmt"
	"math"
	"math/rand"
)

// Go's math (amd64 assembly for these) against the portable Go code it
// replaces on other machines: the bits of every result.
func main() {
	r := rand.New(rand.NewSource(1))
	edge := []float64{0, math.Copysign(0, -1), 1, -1, 0.5, 2, math.Inf(1), math.Inf(-1), math.NaN(),
		math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, 709.78, 709.79, -745.13, -745.14,
		1e-300, 1e300, 2.5, -2.5, 0.1, 1 << 52, 1<<52 + 0.5, 4503599627370497.5}
	gen := func(i int) float64 {
		switch i % 4 {
		case 0:
			return math.Float64frombits(r.Uint64())
		case 1:
			return (r.Float64()*2 - 1) * 750
		case 2:
			return r.NormFloat64() * 10
		default:
			return r.ExpFloat64() * math.Pow(10, float64(r.Intn(40)-20))
		}
	}
	type fn1 struct {
		name   string
		go_, p func(float64) float64
	}
	one := []fn1{
		{"Exp", math.Exp, Exp}, {"Log", math.Log, Log}, {"Floor", math.Floor, Floor},
		{"Ceil", math.Ceil, Ceil}, {"Trunc", math.Trunc, Trunc},
		{"Modf.int", func(x float64) float64 { i, _ := math.Modf(x); return i }, func(x float64) float64 { i, _ := Modf(x); return i }},
		{"Modf.frac", func(x float64) float64 { _, f := math.Modf(x); return f }, func(x float64) float64 { _, f := Modf(x); return f }},
	}
	two := []struct {
		name   string
		go_, p func(float64, float64) float64
	}{{"Hypot", math.Hypot, Hypot}, {"Max", math.Max, Max}, {"Min", math.Min, Min}}
	const N = 3000000
	same := func(a, b float64) bool { return math.Float64bits(a) == math.Float64bits(b) || (math.IsNaN(a) && math.IsNaN(b)) }
	for _, f := range one {
		diff, ulp1, worst := 0, 0, ""
		check := func(x float64) {
			a, b := f.go_(x), f.p(x)
			if !same(a, b) {
				diff++
				d := int64(math.Float64bits(a)) - int64(math.Float64bits(b))
				if d == 1 || d == -1 {
					ulp1++
				} else if worst == "" {
					worst = fmt.Sprintf("x=%v go=%v portable=%v", x, a, b)
				}
			}
		}
		for _, x := range edge {
			check(x)
		}
		for i := 0; i < N; i++ {
			check(gen(i))
		}
		fmt.Printf("%-10s inputs %d  different %d (1 ulp: %d) %s\n", f.name, N+len(edge), diff, ulp1, worst)
	}
	for _, f := range two {
		diff, ulp1, worst := 0, 0, ""
		for i := 0; i < N; i++ {
			x, y := gen(i), gen(i+1)
			if i < len(edge)*len(edge) {
				x, y = edge[i%len(edge)], edge[i/len(edge)]
			}
			a, b := f.go_(x, y), f.p(x, y)
			if !same(a, b) {
				diff++
				d := int64(math.Float64bits(a)) - int64(math.Float64bits(b))
				if d == 1 || d == -1 {
					ulp1++
				} else if worst == "" {
					worst = fmt.Sprintf("x=%v y=%v go=%v portable=%v", x, y, a, b)
				}
			}
		}
		fmt.Printf("%-10s inputs %d  different %d (1 ulp: %d) %s\n", f.name, N, diff, ulp1, worst)
	}
}
