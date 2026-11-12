package semantics

import "errors"

// go2lean's own tests (#209): recover() returns the panic's value, so a
// deferred handler can tell its own panics from others, as
// text/tabwriter's handlePanic does with osError.

type ioFailure struct{ err error }

var errShortWrite = errors.New("short write")

type tabWriter struct {
	fail bool
	n    int
}

func (w *tabWriter) write0() {
	if w.fail {
		panic(ioFailure{errShortWrite})
	}
	w.n++
}

func (w *tabWriter) handlePanic(err *error, op string) {
	if e := recover(); e != nil {
		if f, ok := e.(ioFailure); ok {
			*err = f.err
			return
		}
		panic("tabwriter: panic during " + op)
	}
}

func (w *tabWriter) Write() (err error) {
	defer w.handlePanic(&err, "Write")
	w.write0()
	return nil
}

func testRecoverValueOwnPanic() bool {
	w := &tabWriter{fail: true}
	err := w.Write()
	return err == errShortWrite
}

func testRecoverValueNoPanic() bool {
	w := &tabWriter{}
	return w.Write() == nil && w.n == 1
}

func recoveredString() (s string) {
	defer func() {
		if v, ok := recover().(string); ok {
			s = v
		}
	}()
	panic("boom")
}

func testRecoverValueString() bool { return recoveredString() == "boom" }

func runtimeErrorIsNotString() (isString bool, recovered bool) {
	defer func() {
		e := recover()
		recovered = e != nil
		_, isString = e.(string)
	}()
	var xs []int
	_ = xs[3]
	return
}

func testRecoverValueRuntimeError() bool {
	isString, recovered := runtimeErrorIsNotString()
	return recovered && !isString
}
