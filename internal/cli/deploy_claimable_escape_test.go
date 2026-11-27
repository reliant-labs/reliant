package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// On a Reliant machine the credential helper counts as signed in, so
// --claimable is refused; the refusal names how to deploy anonymously on
// purpose (obs 88c94bfa).
func TestClaimableEscapeNamesTheCredentialToDrop(t *testing.T) {
	t.Setenv("FORGE_CREDENTIAL_HELPER", "reliant auth forge-credential")
	t.Setenv(cloud.DefaultTokenEnv, "")
	if got := claimableEscape(cloud.SourceHelper, "", "prod"); !strings.Contains(got, "env -u FORGE_CREDENTIAL_HELPER ") || !strings.Contains(got, "env deploy prod --claimable") {
		t.Errorf("helper escape = %q", got)
	}
	// A login AND a helper: both are removed, or the helper refuses next.
	got := claimableEscape(cloud.SourceLogin, "", "prod")
	if !strings.Contains(got, "-u FORGE_CREDENTIAL_HELPER") || !strings.Contains(got, "FORGE_HOME=<empty dir>") || !strings.Contains(got, "keep that FORGE_HOME") {
		t.Errorf("login escape = %q", got)
	}
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	if got := claimableEscape(cloud.SourceEnv, "", "prod"); !strings.Contains(got, "-u "+cloud.DefaultTokenEnv) || strings.Contains(got, "FORGE_CREDENTIAL_HELPER") {
		t.Errorf("env escape = %q", got)
	}
	if got := claimableEscape(cloud.SourceFlag, "", "prod"); got != "" {
		t.Errorf("flag escape = %q, want none (drop --token)", got)
	}
	// An env that declares its own token_env: that variable, not the default.
	t.Setenv("ACME_CP_TOKEN", "tok")
	got = claimableEscape(cloud.SourceEnv, "ACME_CP_TOKEN", "prod")
	if !strings.Contains(got, "-u ACME_CP_TOKEN ") || strings.Contains(got, cloud.DefaultTokenEnv) {
		t.Errorf("custom token_env escape = %q, want ACME_CP_TOKEN unset", got)
	}
	// A set token variable is unset whatever source the refusal named.
	t.Setenv("FORGE_CREDENTIAL_HELPER", "h")
	if got := claimableEscape(cloud.SourceHelper, "ACME_CP_TOKEN", "prod"); !strings.Contains(got, "-u FORGE_CREDENTIAL_HELPER -u ACME_CP_TOKEN ") {
		t.Errorf("helper escape with a custom token var set = %q", got)
	}
}
