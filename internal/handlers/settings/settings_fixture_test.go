package settings

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// newTestService builds the service over deps through the generated harness,
// for the tests that run against a real database or a hand-written repo.
// Logger and Config come from the harness; a ControlPlane or
// ExchangeClaudeCode the test leaves nil is a stand-in that fails loudly if
// reached (the auto-stub, or WithPlaceholderDeps' panicking func).
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if repo, ok := deps.Repo.(*db.Repo); ok && deps.Accounts == nil {
		deps.Accounts = db.TestAccounts(t, repo)
	}
	if deps.Accounts == nil {
		deps.Accounts = &MockAccounts{}
	}
	return NewTestSettings(t, WithSettingsDeps(deps), WithPlaceholderDeps())
}
