package requirestringer

import "fmt"

// A value type whose only exported methods satisfy standard-library
// interfaces (fmt.Stringer, error). These are rendering/marshalling
// conventions on a data record, not a behavioral seam anyone would mock,
// so they must not force a contract.go.

// Verdict is a classification enum.
type Verdict int

// String satisfies fmt.Stringer.
func (v Verdict) String() string { return "verdict" }

// Finding is a plain result record.
type Finding struct {
	Path string
	Line int
}

// String satisfies fmt.Stringer.
func (f Finding) String() string { return fmt.Sprintf("%s:%d", f.Path, f.Line) }

// MismatchError is a typed error.
type MismatchError struct{ Want, Got string }

// Error satisfies the error interface.
func (e *MismatchError) Error() string { return fmt.Sprintf("want %s, got %s", e.Want, e.Got) }

// MarshalJSON / MarshalText are the same class of convention.
func (f Finding) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// ExportedFunc is a plain function — never counted by the rule.
func ExportedFunc() {}
