package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A DSN nothing listens on: these refusals must come before any connection.
const unreachableDSN = "postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"

// A project that has not written its first migration has nothing to apply,
// which is success — the same answer the scaffolded binary gives.
func TestRunMigrateUp_EmptyDirectoryIsNothingToApply(t *testing.T) {
	var out bytes.Buffer
	if err := runMigrateUp(&out, unreachableDSN, t.TempDir()); err != nil {
		t.Fatalf("migrate up over an empty directory: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to apply") {
		t.Errorf("output = %q; want it to say there is nothing to apply", out.String())
	}
}

func TestRunMigrateForce_RefusesANonVersion(t *testing.T) {
	for _, arg := range []string{"latest", "-2", "1.5"} {
		err := runMigrateForce(&bytes.Buffer{}, unreachableDSN, t.TempDir(), arg)
		if err == nil || !strings.Contains(err.Error(), "is not a migration version") {
			t.Errorf("force %q: err = %v; want a refusal naming the argument", arg, err)
		}
	}
}
