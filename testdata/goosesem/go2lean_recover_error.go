package semantics

import (
	"errors"
	"fmt"
	"runtime"
)

// go2lean's own tests: a recovered error that is not a runtime.Error
// (raymond's parser panics with fmt.Errorf and its errRecover re-panics
// only runtime errors).

func reRecoverErr(f func()) (err error) {
	defer func() {
		if e := recover(); e != nil {
			switch v := e.(type) {
			case runtime.Error:
				panic(e)
			case error:
				err = v
			default:
				panic(e)
			}
		}
	}()
	f()
	return nil
}

func testRecoverFmtErrorf() bool {
	// The message is not modelled: the value must take case error.
	err := reRecoverErr(func() { panic(fmt.Errorf("parse error on line %d", 1)) })
	return err != nil
}

func testRecoverErrorsNew() bool {
	err := reRecoverErr(func() { panic(errors.New("bad")) })
	other := errors.New("bad")
	return err != nil && err != other
}
