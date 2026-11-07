// Migrated handler test fixture — already imports forge/pkg/tdd, so the
// lint rule must NOT fire even though a sibling `tests := []struct{...}`
// slice happens to live in the same file (legacy code path that still
// hand-rolls one test). Mirrors handlers/audit_log/handlers_test.go in
// cpnext, one of the two adopters of `tdd.RunRPCCases`.

package audit_log_test

import (
	"testing"

	_ "github.com/reliant-labs/forge/pkg/tdd"
)

func TestListAuditEvents_Generated(t *testing.T) {
	t.Parallel()

	// The hand-rolled shape is present here too (legacy holdout) but the
	// file already has the tdd import — the rule short-circuits.
	tests := []struct {
		name string
		call func() error
	}{
		{name: "noop", call: func() error { return nil }},
	}
	for _, tt := range tests {
		_ = tt
	}
}
