package semantics

import "context"

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
