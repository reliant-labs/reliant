package deploy

import (
	"strings"
	"testing"
)

// TestCheckDatabaseIdentityNames: a Workload named like a ManagedDatabase in
// the same namespace would run as the database's ServiceAccount, and with it
// the database's Secrets and backup grants.
func TestCheckDatabaseIdentityNames(t *testing.T) {
	if err := CheckDatabaseIdentityNames([]string{"api", "worker"}, []string{"app", "orders"}); err != nil {
		t.Errorf("distinct names refused: %v", err)
	}
	if err := CheckDatabaseIdentityNames(nil, []string{"app"}); err != nil {
		t.Errorf("databases alone refused: %v", err)
	}
	err := CheckDatabaseIdentityNames([]string{"orders", "api", "app", "app"}, []string{"app", "orders"})
	if err == nil {
		t.Fatal("a workload named like a database was admitted: its pods would run as the database's ServiceAccount")
	}
	if msg := err.Error(); !strings.HasPrefix(msg, `"app", "orders": `) || !strings.Contains(msg, "ServiceAccount") {
		t.Errorf("error = %q; want every clashing name once, sorted, and why", msg)
	}
}
