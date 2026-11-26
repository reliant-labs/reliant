package cli

import (
	"strings"
	"testing"
)

// db_query passes the CLI's guards through unchanged: a real env's
// confirmation and reason reach `db query` as its own flags.
func TestDBQueryToolArgs(t *testing.T) {
	got := strings.Join(dbQueryArgs(dbQueryInput{envInput: envInput{Env: "prod"}, SQL: "select 1", Workload: "api", MaxRows: 5, ConfirmEnv: "prod", Reason: "ISS-1"}), " ")
	want := "db query prod select 1 --json --workload api --max-rows 5 --confirm-env prod --reason ISS-1"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}
