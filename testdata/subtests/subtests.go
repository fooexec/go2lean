// Package subtests has tests whose checks live in a t.Run subtest and in
// a suite's method (#48). Bug: pop on an empty stack panics.
package subtests

type stack struct{ items []int }

func (s *stack) push(v int) { s.items = append(s.items, v) }

func (s *stack) pop() int {
	v := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v
}
