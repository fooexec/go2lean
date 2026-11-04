//go:build ignore

package main

import "math"

const (
	haveArchExp, haveArchExp2, haveArchLog, haveArchHypot = false, false, false, false
	haveArchFloor, haveArchCeil, haveArchTrunc, haveArchModf = false, false, false, false
	haveArchMax, haveArchMin                                 = false, false
)

func archExp(float64) float64        { panic("") }
func archExp2(float64) float64       { panic("") }
func archLog(float64) float64        { panic("") }
func archHypot(_, _ float64) float64 { panic("") }
func archFloor(float64) float64      { panic("") }
func archCeil(float64) float64       { panic("") }
func archTrunc(float64) float64      { panic("") }
func archModf(float64) (float64, float64) { panic("") }
func archMax(_, _ float64) float64   { panic("") }
func archMin(_, _ float64) float64   { panic("") }

var (
	IsNaN = math.IsNaN; IsInf = math.IsInf; Inf = math.Inf; NaN = math.NaN
	Float64bits = math.Float64bits; Float64frombits = math.Float64frombits
	Frexp = math.Frexp; Ldexp = math.Ldexp; Abs = math.Abs; Signbit = math.Signbit
	Copysign = math.Copysign; Sqrt = math.Sqrt
)

const (
	Ln2 = math.Ln2; Log2E = math.Log2E
	mask = 0x7FF; shift = 64 - 11 - 1; bias = 1023
	uvnan = 0x7FF8000000000001; uvone = 0x3FF0000000000000; Sqrt2 = math.Sqrt2; signMask = 1 << 63; fracMask = 1<<shift - 1
)
