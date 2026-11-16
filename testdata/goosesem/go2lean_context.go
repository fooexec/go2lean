package semantics

import (
	"context"
	"errors"
	"time"
)

// go2lean's own tests: context.WithValue and ctx.Value, with the
// cancellation of a context under a value context (grpc's metadata
// carries its MD in a context value).

type ctxKey struct{}

type otherKey struct{}

func testContextValueFound() bool {
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	s, ok := ctx.Value(ctxKey{}).(string)
	return ok && s == "v"
}

func testContextValueMissing() bool {
	ctx := context.WithValue(context.Background(), ctxKey{}, 1)
	return ctx.Value(otherKey{}) == nil && context.Background().Value(ctxKey{}) == nil
}

func testContextValueNearest() bool {
	ctx := context.WithValue(context.Background(), ctxKey{}, 1)
	ctx = context.WithValue(ctx, otherKey{}, 2)
	ctx = context.WithValue(ctx, ctxKey{}, 3)
	a, _ := ctx.Value(ctxKey{}).(int)
	b, _ := ctx.Value(otherKey{}).(int)
	return a == 3 && b == 2
}

func testContextValueThroughCancel() bool {
	ctx := context.WithValue(context.Background(), ctxKey{}, 7)
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	v, ok := child.Value(ctxKey{}).(int)
	return ok && v == 7
}

func testContextCancelUnderValue() bool {
	parent, cancel := context.WithCancel(context.Background())
	ctx := context.WithValue(parent, ctxKey{}, 1)
	child, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	if ctx.Err() != nil || child.Err() != nil {
		return false
	}
	cancel()
	<-child.Done()
	return ctx.Err() != nil && child.Err() != nil
}

// context.Cause is ctx.Err() when no cause was set (retry-go reads it
// after every attempt, #256).
func testContextCause() bool {
	ctx, cancel := context.WithCancel(context.Background())
	before := context.Cause(ctx)
	cancel()
	return before == nil && context.Cause(ctx) == context.Canceled && context.Cause(context.Background()) == nil
}

// A context's error is context.Canceled or context.DeadlineExceeded, as
// == and errors.Is compare them (#256).
func testContextErrSentinels() bool {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tctx, tcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer tcancel()
	<-tctx.Done()
	return ctx.Err() == context.Canceled && errors.Is(ctx.Err(), context.Canceled) &&
		tctx.Err() == context.DeadlineExceeded && ctx.Err() != context.DeadlineExceeded
}
