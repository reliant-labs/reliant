// Package callrecords is the fixture for the typed per-method call records
// every generated mock carries (mock_calls.go): one named, typed field per
// parameter, read back through <Method>Calls().
//
// Each method below is a naming case the generator must resolve without a
// clash: parameters named like the mock's receiver, unnamed and blank ones,
// two names that export to the same field, a variadic, a contract method
// whose name IS another method's accessor name, a record type name the
// package already declares, and imports aliased like the accessor's locals.
package callrecords

import (
	"context"
	r "io"
	out "strings"
)

// Lease is a local struct type a record field holds.
type Lease struct {
	JobID string
	Epoch uint64
}

// MockServiceTakenCall is a package-level name the record type for
// Service.Taken would take; the generator must move its record aside.
type MockServiceTakenCall struct{}

// Service is mocked; every method is a call-record case.
type Service interface {
	// The motivating case: a test asserts the lease's epoch by name.
	Publish(ctx context.Context, lease Lease, version int) error
	// m is the mock's receiver: the body renames the parameter, the record
	// keeps the contract's name (field M).
	Rename(ctx context.Context, m string) error
	// Unnamed parameters take P<index>.
	Unnamed(context.Context, string, int) error
	// A blank parameter takes P<index>; a named p0 keeps P0 first.
	Blank(_ string, p0 int) error
	// x and X export to the same name; the later one moves aside.
	Clash(x int, X string) error
	// A variadic parameter is recorded as the slice it arrives as.
	Variadic(prefix string, rest ...string) error
	// An interface-typed parameter that may be nil must not panic the
	// accessor.
	Fail(err error)
	// No parameters: the record is empty, the accessor still counts.
	Ping() bool
	// Foo's natural accessor name is FooCalls, which the contract declares
	// itself; FooCalls keeps it.
	Foo(n int)
	FooCalls() int
	// The record type MockServiceTakenCall is declared above.
	Taken(id string) error
	// Field types spelling the qualifiers `r` and `out`, which are the
	// accessor body's natural local names.
	Aliased(src r.Reader, b *out.Builder) error
}
