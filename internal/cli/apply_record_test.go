package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// fileLedgerDeployFixture is a real machine ledger holding two cut releases for
// prod and a recorded bundle for each, so a deploy can write its promotion AND
// the apply record that joins to it.
func fileLedgerDeployFixture(t *testing.T) (dir string, ledger envLedger) {
	t.Helper()
	dir = newLedgerTestProject(t, "apply-record")
	ledger = testLedger(t, dir)
	for i, v := range []string{"v1", "v2"} {
		c := string(rune('1' + i))
		if err := testCutRelease(t, dir, rel(v, "2026-01-0"+c+"T00:00:00Z", "", false, map[string]string{"api": sha(c)})); err != nil {
			t.Fatalf("cut %s: %v", v, err)
		}
		if _, _, err := testRecordStore(t, dir).RecordBundle(context.Background(), release.BundleRecord{
			Env: "prod", Release: v, Digest: sha("bundle-" + v), Reference: "ghcr.io/acme/b@" + sha("bundle-"+v),
			ConfigDigest: sha("cfg-" + v), Shape: testShape(),
		}, testBundleBlobs()); err != nil {
			t.Fatalf("record bundle %s: %v", v, err)
		}
	}
	return dir, ledger
}

func deployToProd(t *testing.T, dir string, ledger envLedger, version string) (string, error) {
	t.Helper()
	opts := promoteOptions{ProjectDir: dir, Git: allCommitsPresent(), Follow: waitByDefault(), Ledger: ledger}
	opts.Run.None = true
	var err error
	out := captureStdout(t, func() { err = runPromote(context.Background(), version, "prod", opts) })
	return out, err
}

// A failed apply must not leave the ledger telling a reader the env is on the
// new release with nothing to say otherwise. The promotion stays (the CAS and
// roll-forward recovery need it); the apply record carries the truth.
func TestFailedApply_RecordsAFailedOutcomeAndSaysSo(t *testing.T) {
	dir, ledger := fileLedgerDeployFixture(t)
	apply := capturedClientDeploy{err: &exitCodeError{code: exitWrong, msg: `The StorageClass "workspace-ssd" is invalid: parameters: Forbidden`}}
	apply.install(t)

	out, err := deployToProd(t, dir, ledger, "v2")
	if err == nil {
		t.Fatal("a failed apply must fail the deploy")
	}
	if !strings.Contains(out, "RECORDED BUT NOT APPLIED") || !strings.Contains(out, "workspace-ssd") {
		t.Errorf("the output must say the release is recorded but NOT applied, and why:\n%s", out)
	}
	if strings.Contains(out, `Promoted env "prod"`) {
		t.Errorf("a failed apply must not print the success line:\n%s", out)
	}

	cur, _, _ := testStore(t, dir).CurrentPromotion("prod")
	applies, aerr := testStore(t, dir).Applies("prod")
	if aerr != nil || len(applies) != 1 {
		t.Fatalf("want one apply record, got %d (%v)", len(applies), aerr)
	}
	a := applies[0]
	if a.Apply.PromotionID != cur.ID {
		t.Errorf("the apply must join the promotion it realizes: %q vs %q", a.Apply.PromotionID, cur.ID)
	}
	if got := release.DeriveApplyState(a.Apply, a.Outcome, time.Now()); got != release.ApplyStateFail {
		t.Fatalf("apply state = %s, want failed", got)
	}
	if line := latestApplyLine(dir, "prod", cur.ID, time.Now()); !strings.Contains(line, "failed") || !strings.Contains(line, "not (fully) running") {
		t.Errorf("env status must say the apply failed, got %q", line)
	}
}

// Re-applying the SAME release after a failed apply must run the apply again,
// not be refused because the env is "already on" it, and must end up recorded
// as succeeded.
func TestFailedApply_ReApplyOfTheSameReleaseIsNotRefused(t *testing.T) {
	dir, ledger := fileLedgerDeployFixture(t)
	failing := capturedClientDeploy{err: &exitCodeError{code: exitWrong, msg: "boom"}}
	failing.install(t)
	if _, err := deployToProd(t, dir, ledger, "v2"); err == nil {
		t.Fatal("first apply should fail")
	}

	ok := capturedClientDeploy{}
	ok.install(t)
	out, err := deployToProd(t, dir, ledger, "v2")
	if err != nil {
		t.Fatalf("re-applying the same release must not be refused: %v\n%s", err, out)
	}
	if len(ok.calls) != 1 {
		t.Fatalf("the re-apply must run the apply, ran it %d time(s)", len(ok.calls))
	}
	applies, _ := testStore(t, dir).Applies("prod")
	if len(applies) != 2 {
		t.Fatalf("want 2 apply records (failed, then succeeded), got %d", len(applies))
	}
	last := applies[len(applies)-1]
	if got := release.DeriveApplyState(last.Apply, last.Outcome, time.Now()); got != release.ApplyStateOK {
		t.Errorf("the re-apply must be recorded as succeeded, got %s", got)
	}
	if promos, _ := testStore(t, dir).Promotions("prod"); len(promos) != 1 {
		t.Errorf("a same-release re-apply must not append a second promotion, got %d", len(promos))
	}
}

func TestSuccessfulApply_PrintsThePromotedLineAndRecordsSuccess(t *testing.T) {
	dir, ledger := fileLedgerDeployFixture(t)
	ok := capturedClientDeploy{}
	ok.install(t)
	out, err := deployToProd(t, dir, ledger, "v2")
	if err != nil || !strings.Contains(out, `Promoted env "prod"`) {
		t.Fatalf("a good apply keeps the success line (err=%v):\n%s", err, out)
	}
	applies, _ := testStore(t, dir).Applies("prod")
	if len(applies) != 1 || applies[0].Outcome == nil || applies[0].Outcome.Status != release.ApplySucceeded {
		t.Fatalf("want one succeeded apply record, got %+v", applies)
	}
}
