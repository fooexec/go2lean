// Command zoryacrash panics when the first byte of its argument is 'K' and
// the third is 'Z'; a report that only sets the first byte does not panic.
package main

import "os"

func main() {
	a := []byte(os.Args[1])
	if len(a) > 2 && a[0] == 'K' {
		if a[2] == 'Z' {
			var p *int
			*p = 1
		}
	}
}
