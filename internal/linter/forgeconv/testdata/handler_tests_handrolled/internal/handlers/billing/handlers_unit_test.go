// Hand-rolled handler test fixture — the `tests := []struct{name, call}`
// shape the lint rule must catch. Mirrors the cpnext-style files
// (handlers/billing/handlers_unit_test.go, daemon/handlers_unit_test.go,
// org/handlers_unit_test.go) that ported by hand instead of landing on
// `tdd.RunRPCCases`.

package billing_test

import (
	"context"
	"testing"
)

func TestHandlers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_ = ctx

	tests := []struct {
		name string
		call func() error
	}{
		{name: "GetBill", call: func() error { return nil }},
		{name: "ListBills", call: func() error { return nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Logf("call %s: %v", tt.name, err)
			}
		})
	}
}
