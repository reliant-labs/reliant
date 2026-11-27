package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Every rpc that requires a caller in a project nobody can sign in to is one
// advisory finding at its proto line, naming the rpc and the two ways out.
func TestAuthNoSignInFindingsAreAdvisoryAndNameTheFix(t *testing.T) {
	t.Parallel()
	got := authNoSignInFindingsFor([]authNoSignInRPC{
		{Service: "NotesService", Name: "ListNotes", File: "proto/services/notes/v1/notes.proto", Line: 12},
	})
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Rule != ruleAuthRequiredNoSignIn || f.Severity != finding.SeverityWarning ||
		f.File != "proto/services/notes/v1/notes.proto" || f.Line != 12 {
		t.Errorf("finding = %+v", f)
	}
	for _, want := range []string{"NotesService.ListNotes", "401"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message does not mention %q: %s", want, f.Message)
		}
	}
	for _, want := range []string{"auth_required: false", "forge:lint-disable-next-line " + ruleAuthRequiredNoSignIn} {
		if !strings.Contains(f.Remediation, want) {
			t.Errorf("remediation does not mention %q: %s", want, f.Remediation)
		}
	}
}

// A project that declares a sign-in, or has no frontend, is never judged: its
// rpcs can be satisfied. The lane decides that before reading any descriptor.
func TestAuthNoSignInSkipsProjectsThatCanAuthenticate(t *testing.T) {
	t.Parallel()
	for name, files := range map[string][]string{
		"identity declared": {"frontends/web/next.config.ts", "deploy/kcl/dev/main.k", "deploy/kcl/identity.k"},
		"no frontend":       {"deploy/kcl/dev/main.k"},
	} {
		root := t.TempDir()
		for _, rel := range files {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := collectAuthNoSignInFindings(root)
		if err != nil || len(got) != 0 {
			t.Errorf("%s: findings = %+v, err = %v; want none", name, got, err)
		}
	}
}
