package lint

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// forge:identity-subject is read by the column-markers lane: a bad argument is
// the lane's own finding (generate refuses the same comment), and a subject
// column the read-only role cannot read is its own rule, which warns first.
func TestColumnMarkersLaneReadsIdentitySubject(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_identity.up.sql", `
COMMENT ON COLUMN users.external_id IS 'forge:identity-subject — the Zitadel user id';
COMMENT ON COLUMN members.login IS 'forge:identity-subject=email';
COMMENT ON COLUMN members.sub IS 'forge:identity-subject=uid';
COMMENT ON COLUMN accounts.owner IS 'forge:identity-subject forge:secret';
COMMENT ON COLUMN sessions.token_subject IS 'forge:identity-subject';
COMMENT ON COLUMN grants.token_subject IS 'forge:identity-subject forge:readable — the subject a token was issued to, not the token';
COMMENT ON COLUMN audit.actor_token IS 'forge:identity-subject=bogus forge:secret';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	type want struct{ rule, problem string }
	wants := map[int]want{
		4: {ruleColumnMarkers, "forge:identity-subject=uid names no attribute"},
		5: {ruleIdentitySubjectUnreadable, "also declared forge:secret"},
		6: {ruleIdentitySubjectUnreadable, "token_subject's name looks like a credential"},
		// A bad argument is reported once, as the bad argument; the
		// declaration it fails to make cannot also contradict forge:secret.
		8: {ruleColumnMarkers, "forge:identity-subject=bogus names no attribute"},
	}
	if len(findings) != len(wants) {
		t.Fatalf("want %d findings, got %d: %+v", len(wants), len(findings), findings)
	}
	for _, f := range findings {
		w, ok := wants[f.Line]
		if !ok {
			t.Errorf("unexpected finding on line %d: %+v", f.Line, f)
			continue
		}
		if f.rule() != w.rule || !strings.Contains(f.Problem, w.problem) {
			t.Errorf("line %d: [%s] %q, want [%s] containing %q", f.Line, f.rule(), f.Problem, w.rule, w.problem)
		}
	}
}

// The unreadable-subject rule is new: a project pinned below its GatesFrom
// sees a warning, one pinned at it is gated.
func TestIdentitySubjectUnreadableWarnsFirst(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_identity.up.sql", `
COMMENT ON COLUMN accounts.owner IS 'forge:identity-subject forge:secret';
`)
	fs, err := collectColumnMarkersJSON(&config.ProjectConfig{Database: config.DatabaseConfig{MigrationsDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Rule != ruleIdentitySubjectUnreadable || fs[0].Severity != lintSevError || fs[0].FixHint == "" {
		t.Fatalf("findings = %+v, want one error-severity %s with a fix hint", fs, ruleIdentitySubjectUnreadable)
	}
	if !strings.Contains(fs[0].Message, "accounts.owner") {
		t.Errorf("message %q does not name the column", fs[0].Message)
	}
	ro, ok := lintRollouts[ruleIdentitySubjectUnreadable]
	if !ok {
		t.Fatalf("%s has no rollout row", ruleIdentitySubjectUnreadable)
	}
	got, gated := gradeProjectLane(pinnedCfg(ro.IntroducedIn, nil), fs, true)
	if gated || len(got) != 1 || got[0].Severity != lintSevWarning {
		t.Errorf("pinned to %s: got %+v gated=%v, want a warning", ro.IntroducedIn, got, gated)
	}
	got, gated = gradeProjectLane(pinnedCfg(ro.GatesFrom, nil), fs, true)
	if !gated || len(got) != 1 || got[0].Severity != lintSevError {
		t.Errorf("pinned to %s: got %+v gated=%v, want it gated", ro.GatesFrom, got, gated)
	}
}
