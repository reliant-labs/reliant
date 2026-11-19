package tdd

// MockOption configures a Forge-generated Func-field mock.
//
// Forge's mock_gen.go produces structs of the form
//
//	type MockService struct {
//	    DoXFunc func(...) (...)
//	    DoYFunc func(...) (...)
//	}
//
// MockOption[T] is a functional option that mutates such a struct. Use
// it with [NewMock] to build a mock at the call site:
//
//	m := tdd.NewMock(
//	    func(m *email.MockService) { m.SendFunc = func(...) error { return nil } },
//	)
//
// MockOption is a plain function alias so callers can write inline
// closures without naming the option type, which keeps test files
// short.
type MockOption[T any] func(*T)

// NewMock constructs a zero-valued T and applies each option.
//
// T is typically a Forge-generated MockService. The returned pointer is
// safe to assign into a Deps struct that expects the contract interface
// — Forge mocks have a *MockService method-set that satisfies their
// matching Service interface.
func NewMock[T any](opts ...MockOption[T]) *T {
	var m T
	for _, opt := range opts {
		opt(&m)
	}
	return &m
}
