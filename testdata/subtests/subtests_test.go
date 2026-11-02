package subtests

import "testing"

// The check is in a subtest: the model runs it as part of TestStack.
func TestStack(t *testing.T) {
	t.Run("push then pop", func(t *testing.T) {
		s := &stack{}
		s.push(1)
		if s.pop() != 1 {
			t.Error("pop")
		}
	})
	t.Run("pop empty", func(t *testing.T) {
		s := &stack{}
		s.pop()
	})
}

// A suite in grpc-go's style: its methods are the tests, found by
// reflection in the real run; each is a target of its own.
type suite struct{}

func (suite) TestPopTwice(t *testing.T) {
	s := &stack{}
	s.push(1)
	s.pop()
	s.pop()
}
