package cli

import (
	"strings"
	"testing"
)

func TestSeedClearRequiresExplicitConfirmation(t *testing.T) {
	cmd := newDBSeedCommand()
	cmd.SetArgs([]string{"clear", "--table", "assignments"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "pass --confirm") || !strings.Contains(err.Error(), "application rows") {
		t.Fatalf("clear without --confirm: %v; want explicit warning before any DB access", err)
	}
}
