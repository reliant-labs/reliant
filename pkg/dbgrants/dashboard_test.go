package dbgrants

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDashboardNames(t *testing.T) {
	if DashboardRoleName != "forge_dash" || DashboardSecretSuffix != "-forge-dash" {
		t.Errorf("role %q suffix %q", DashboardRoleName, DashboardSecretSuffix)
	}
	if got := DashboardSecretName("order-db"); got != "order-db-forge-dash" {
		t.Errorf("DashboardSecretName = %q", got)
	}
	if DashboardSecretName("x") == SecretName("x") {
		t.Error("the two roles share a Secret")
	}
}

// The read-only script converges forge_ro alone: nothing about it depends on
// whether a role can log in, which only ever mattered to the retired dashboard
// script.
func TestReadOnlyScriptIsNotLoginGated(t *testing.T) {
	script, err := Render([]Table{{Schema: "public", Name: "t", Columns: []Column{{Name: "id"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"rolcanlogin", "role_login", DashboardRoleName} {
		if strings.Contains(script, banned) {
			t.Errorf("the read-only script mentions %q:\n%s", banned, script)
		}
	}
}

type stateErr string

func (e stateErr) Error() string    { return "pg error " + string(e) }
func (e stateErr) SQLState() string { return string(e) }

func TestSQLStateReadsWrappedDriverErrors(t *testing.T) {
	if got := sqlState(fmt.Errorf("exec: %w", stateErr("42501"))); got != "42501" {
		t.Errorf("sqlState = %q, want 42501", got)
	}
	if got := sqlState(errors.New("plain")); got != "" {
		t.Errorf("sqlState of a plain error = %q, want empty", got)
	}
}

func TestEnsureRoleRefusesAnUnrenderableName(t *testing.T) {
	if _, err := EnsureRole(t.Context(), nil, "x\ny"); err == nil {
		t.Fatal("EnsureRole accepted a name with a newline")
	}
}
