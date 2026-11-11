// Package identcollision is the regression fixture for contract parameters
// named after an identifier a GENERATED method body uses.
//
// An interface method has no body, so every name below is legal here. The
// mock (mock_gen.go) and the observability decorator (middleware_gen.go) DO
// have bodies, and those bodies spell their receiver, their own locals,
// package qualifiers and predeclared identifiers in the same scope as the
// parameters. Each method below names its parameters after one of those.
package identcollision

import (
	"context"
	"time"
)

// Result is a local struct type: the mock's zero value for it is Result{}.
type Result struct{ N int }

// Service is wrapped by the decorator and mocked; every method is a
// collision the generators must survive.
type Service interface {
	// m is the mock's receiver; contractkit qualifies its MockNotSet error.
	Apply(ctx context.Context, m string, contractkit int) error
	// o is the decorator's receiver; r0, r1 and err are its capture locals;
	// context is the package its closure signature and context.Background()
	// name.
	Lookup(o string, r0 int, r1 bool, err string, context string) (string, int, error)
	// The decorator's closure declares its own `var e error`. A parameter e
	// of type error is shadowed by it and still compiles, so the wrapper
	// used to forward nil instead of the caller's error.
	Report(e error) (string, int, error)
	// time is the package of the parameter's own type, which the mock's zero
	// value and the decorator's closure result also name.
	Wait(ctx context.Context, time time.Duration) (time.Duration, error)
	// Predeclared identifiers the generated bodies spell: new
	// (`*new(time.Time)`), nil, and error.
	Stamp(new string, nil int, error string) (time.Time, error)
	// A local type the mock's zero value names.
	Fetch(ctx context.Context, Result string) (Result, error)
	// Names an instrumentation wrapper reaches for: a ctx that is not a
	// context, start, span, and the decorator's inner/chain fields and
	// observe package.
	Trace(ctx string, start time.Time, span string, inner int, chain int, observe bool) error
	// A contract name equal to the positional name a blank parameter gets.
	Mixed(_ context.Context, p0 string) error
	// Named results, which no generated body assigns.
	Named(ctx context.Context, m string) (r0 string, o int, err error)
	// No results: the decorator's void form.
	Fire(m int, o int)
	// Non-error results only: the decorator's capture-without-error form.
	Pair(e int, r0 string) (r1 int, s string)
}
