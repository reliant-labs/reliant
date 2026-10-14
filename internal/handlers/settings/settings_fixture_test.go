package settings

import "testing"

// newTestService builds the service over deps through the generated harness,
// for the tests that run against a real database or a hand-written repo.
// Logger and Config come from the harness; a ControlPlane or
// ExchangeClaudeCode the test leaves nil is a stand-in that fails loudly if
// reached (the auto-stub, or WithPlaceholderDeps' panicking func).
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	return NewTestSettings(t, WithSettingsDeps(deps), WithPlaceholderDeps())
}
