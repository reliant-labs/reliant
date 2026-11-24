package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// `forge secret get NAME --env prod` says secrets are not read back and what
// to run instead, rather than failing on "unknown flag: --env".
func TestSecretGetExplainsTheDesign(t *testing.T) {
	cmd := newSecretCmd()
	cmd.SetArgs([]string{"get", "API_KEY", "--env", "prod"})
	err := cmd.Execute()
	if err == nil || exitCodeForError(err) != exitWrong || !strings.Contains(err.Error(), "never prints a secret") || !strings.Contains(err.Error(), "secret list --env") {
		t.Fatalf("secret get = %v", err)
	}
}

// A hosted store's inert key names the workflow path too.
func TestSecretListInertNamesWorkflowAccessWhenHosted(t *testing.T) {
	var out strings.Builder
	writeSecretList(&out, secretListReport{Env: "prod", Provider: "hosted", Inert: []string{"PAYMENTS_KEY"}, Secrets: []secretListEntry{}})
	if !strings.Contains(out.String(), "forge secret allow PAYMENTS_KEY --env prod --project <id> --workflow <name>") {
		t.Fatalf("inert block:\n%s", out.String())
	}
}

// A machine token refused token administration (control-plane#3209 gives it
// the person_session_required reason too) is told so, not "a CLI login".
func TestPersonSessionRefusalNamesAMachineToken(t *testing.T) {
	r := &personSessionRefusal{action: "mint", err: &cloud.Error{Endpoint: "https://cp.example", Message: "machine credentials cannot manage machine credentials"}}
	if msg := r.Error(); !strings.Contains(msg, "machine token") || strings.Contains(msg, "CLI login (") {
		t.Fatalf("refusal = %q", msg)
	}
	r.err.Message = "a CLI login cannot manage access tokens"
	if msg := r.Error(); !strings.Contains(msg, "CLI login") {
		t.Fatalf("refusal = %q", msg)
	}
}
