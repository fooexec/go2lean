package constraint

import "testing"

// A hunt raises the fuel only for runs that are long, not hung (#45).
func TestFuelShort(t *testing.T) {
	for _, c := range []struct {
		res  searchResult
		want bool
	}{
		{searchResult{Trials: 2000, OutOfFuel: 500}, true},
		{searchResult{Trials: 2000, OutOfFuel: 499}, false},
		{searchResult{Trials: 2000, OutOfFuel: 1323, LoopsChecked: 20, LoopsForever: 1}, false},
		{searchResult{Trials: 10, OutOfFuel: 10}, false},
	} {
		if got := fuelShort(&c.res); got != c.want {
			t.Errorf("fuelShort(%d of %d out, %d loop) = %v, want %v", c.res.OutOfFuel, c.res.Trials, c.res.LoopsForever, got, c.want)
		}
	}
}
