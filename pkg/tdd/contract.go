package tdd

import (
	"errors"
	"reflect"
	"testing"
)

// ContractCase is a table-driven test row for a single contract-method
// invocation. Each row supplies a Call closure that invokes one method on
// the contract implementation. The closure returns (any, error) so a
// single ContractCase type can drive any method shape — multi-return
// methods adapt by packing into a struct, single-return methods return
// the value directly.
type ContractCase struct {
	// Name identifies the row; passed straight to t.Run.
	Name string

	// Call invokes one method on the contract implementation. The
	// returned value is compared against Want using reflect.DeepEqual
	// when WantErr is nil.
	Call func() (any, error)

	// Want is the expected return value (compared with reflect.DeepEqual).
	// Ignored when WantErr is set or Check is non-nil.
	Want any

	// WantErr, if non-nil, asserts that Call returned an error matching
	// it via errors.Is. Use a sentinel error or a wrapped error.
	WantErr error

	// Check is an alternative to Want — it runs on the returned value
	// after a successful call and lets the test assert custom predicates.
	Check func(t *testing.T, got any)

	// Setup runs before Call. Use it to wire mocks or seed state for
	// this row. Cleanup should be registered via t.Cleanup.
	Setup func(t *testing.T)
}

// TableContract runs a slice of [ContractCase] rows. Each row becomes a
// t.Run subtest. The helper invokes Setup, runs Call, and:
//   - if WantErr is set, asserts errors.Is(err, WantErr),
//   - else if Check is set, runs it on the returned value,
//   - else compares the returned value to Want via reflect.DeepEqual.
//
// The impl parameter is unused at runtime — it exists so the call site
// reads naturally (TableContract(t, svc, cases)) and so the Go type
// system carries the implementation type into the closure capture, which
// is the most ergonomic shape we found for an "any method on T" table.
func TableContract[T any](t *testing.T, impl T, cases []ContractCase) {
	t.Helper()
	_ = impl // captured by closures in cases[].Call
	for _, tc := range cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Setup != nil {
				tc.Setup(t)
			}
			got, err := tc.Call()

			if tc.WantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tc.WantErr)
				}
				if !errors.Is(err, tc.WantErr) {
					t.Fatalf("expected error %v, got %v", tc.WantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.Check != nil {
				tc.Check(t, got)
				return
			}
			if tc.Want != nil && !reflect.DeepEqual(got, tc.Want) {
				t.Fatalf("got %#v, want %#v", got, tc.Want)
			}
		})
	}
}
