package cli

// §17 step 7, the machine half: `forge ledger import --from-git` against a
// COPY of control-plane's real ledger.
//
// WHY A REAL CHECKOUT AND NOT A FIXTURE. The fixtures beside this file pin
// each rule in isolation, from records this test file wrote — so they can
// only ever be wrong in the same way the reader is. control-plane's ledger
// has the shapes nobody would think to fabricate: v1_0_0.json holding
// v1.0.0, two envs whose only entry came from a one-time conversion with an
// `actor` instead of a `user`, releases carrying gomod and npm artifacts
// beside the images, and 15 prod promotions whose order is the thing that
// decides what `forge env status prod` reports. It is the acceptance gate
// because it is the ledger this command was written for.
//
// IT IS OFF BY DEFAULT, and gated rather than skipped-on-absence: a test
// that silently passes when it cannot find its subject is worse than no
// test, because the gate it represents disappears without anyone noticing.
//
//	FORGE_LEDGER_IMPORT_ACCEPTANCE=/path/to/control-plane go test -run Acceptance ./internal/cli/
//
// NOTHING IS WRITTEN TO THE REAL ANYTHING. The checkout is cloned (the clone
// is read-only here regardless), and the ledger home is a t.TempDir(). The
// owner's ~/.forge/ledger is theirs to import, once, deliberately.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// acceptanceCheckoutEnv names the checkout to clone and import from.
const acceptanceCheckoutEnv = "FORGE_LEDGER_IMPORT_ACCEPTANCE"

// acceptanceRevEnv overrides the rev, for a checkout whose ledger commits
// are not on origin/main.
const acceptanceRevEnv = "FORGE_LEDGER_IMPORT_ACCEPTANCE_REV"

// TestLedgerImportAcceptance_RealCheckout is the §17 step 7 gate: the dry run
// counts the real numbers, --apply clears F3's refusal, and a second --apply
// is a no-op.
//
// One test rather than three, because the three are stages of one sequence
// and the second's subject is the first's output. Splitting them would mean
// cloning a large repository three times to re-derive the same state.
func TestLedgerImportAcceptance_RealCheckout(t *testing.T) {
	source := strings.TrimSpace(os.Getenv(acceptanceCheckoutEnv))
	if source == "" {
		t.Skipf("set %s=<path to a checkout holding .forge/promotions> to run the acceptance gate", acceptanceCheckoutEnv)
	}
	rev := strings.TrimSpace(os.Getenv(acceptanceRevEnv))
	if rev == "" {
		rev = defaultImportRev
	}

	clone := cloneForAcceptance(t, source)
	// INTO the clone, because a real render is not CWD-independent: KCL's
	// `file.read` resolves a relative path against the process working
	// directory, and control-plane's prod reads
	// deploy/openbao/bootstrap.sh that way. --project-dir alone points
	// forge's project lookup at the clone while leaving KCL reading the
	// test binary's directory, which fails the render — and selection
	// renders, because the ledger is chosen by declaration.
	//
	// This is why the test is not parallel: t.Chdir and t.Setenv are both
	// process-wide, and Go panics on the combination in a parallel test.
	t.Chdir(clone)
	// The ledger home is a temp dir. This is the line that keeps the
	// owner's real ledger out of a test run.
	useLedgerHome(t, t.TempDir())

	// ── Stage 1: the dry run counts what the repository holds ──────────
	dry, err := runImport(t, clone, "--from-git", "--rev", rev)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, dry)
	}
	t.Logf("DRY RUN\n%s", dry)
	wantCounts := expectedCounts(t, clone, rev)
	if !strings.Contains(dry, wantCounts) {
		t.Errorf("the dry run must report what the repository holds (%q), got:\n%s", wantCounts, dry)
	}
	if !strings.Contains(dry, "dry run") {
		t.Errorf("the dry run must say it is one, got:\n%s", dry)
	}
	// A dry run writes nothing, which is what makes §11.1 step 2 safe to
	// run against prod's real ledger before deciding anything.
	store := testStore(t, clone)
	if releases, _ := store.Releases(); len(releases) != 0 {
		t.Fatalf("the dry run recorded %d releases; it must record none", len(releases))
	}

	// ── Stage 2: --apply records it, and F3's refusal goes quiet ───────
	// The refusal is the reason this command exists and the reason the
	// control-plane forge pin cannot move past F3 without it, so it is
	// asserted before AND after.
	envs := acceptanceEnvs(t, clone, rev)
	for _, env := range envs {
		l, err := selectLedger(context.Background(), clone, env)
		if err != nil {
			t.Fatalf("select the ledger for %s: %v", env, err)
		}
		if err := checkLedgerImported(context.Background(), clone, env, l); err == nil {
			t.Fatalf("env %s: expected the unimported-checkout refusal before the import", env)
		}
	}

	applied, err := runImport(t, clone, "--from-git", "--rev", rev, "--apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, applied)
	}
	t.Logf("APPLY\n%s", applied)

	for _, env := range envs {
		l, err := selectLedger(context.Background(), clone, env)
		if err != nil {
			t.Fatalf("select the ledger for %s: %v", env, err)
		}
		if err := checkLedgerImported(context.Background(), clone, env, l); err != nil {
			t.Errorf("env %s: the refusal must clear after the import, got: %v", env, err)
		}
	}

	// prod's current binding is the LAST line of its log, so this is the
	// ordering assertion with teeth: a reversed import would name a
	// release prod stopped running months ago.
	wantCurrent := lastPromotedRelease(t, clone, rev, "prod")
	if wantCurrent != "" {
		current, ok, err := store.CurrentPromotion("prod")
		if err != nil || !ok {
			t.Fatalf("prod's current promotion: %v, %v", ok, err)
		}
		if current.Release != wantCurrent {
			t.Errorf("prod runs %q, want %q (the last line of prod.jsonl at %s)", current.Release, wantCurrent, rev)
		}
		t.Logf("prod's current release after the import: %s", current.Release)
	}

	// Every imported promotion names the blob its line came from, which is
	// what keeps the history auditable after §11.1 step 9 deletes the
	// files from the repository.
	from, err := store.ImportedFrom("prod")
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	prodPromotions, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read prod's promotions: %v", err)
	}
	for _, p := range prodPromotions {
		if !strings.HasPrefix(from[p.ID], "git:") {
			t.Errorf("promotion %s has imported_from %q, want a git: provenance", p.ID, from[p.ID])
		}
	}
	t.Logf("imported %d prod promotions, each with git provenance", len(prodPromotions))

	// ── Stage 3: a second --apply is a visible no-op ───────────────────
	again, err := runImport(t, clone, "--from-git", "--rev", rev, "--apply")
	if err != nil {
		t.Fatalf("second apply: %v\n%s", err, again)
	}
	t.Logf("SECOND APPLY\n%s", again)
	if !strings.Contains(again, "0 promotions, 0 releases") {
		t.Errorf("the second apply must record nothing, got:\n%s", again)
	}
	if !strings.Contains(again, "already held") {
		t.Errorf("the second apply must SAY it is a no-op, got:\n%s", again)
	}
	afterSecond, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read prod's promotions: %v", err)
	}
	if len(afterSecond) != len(prodPromotions) {
		t.Errorf("prod has %d promotions after two applies, want %d", len(afterSecond), len(prodPromotions))
	}
}

// cloneForAcceptance makes a throwaway copy of source.
//
// --no-hardlinks, so nothing this test does can reach the owner's object
// store, and --no-checkout is NOT used: the import resolves its project
// through forge.yaml, which has to be on disk. The clone's working tree is
// never written.
func cloneForAcceptance(t *testing.T, source string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "checkout")
	cmd := exec.Command("git", "clone", "--no-hardlinks", "--quiet", source, clone)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone %s: %v: %s", source, err, out)
	}
	// A clone's origin/<branch> tracks the source's LOCAL branches, not
	// the source's own remote-tracking refs. So origin/main here is the
	// source checkout's `main`, which may be a commit or two behind its
	// upstream. That is fine, and is why expectedCounts derives the
	// numbers from the same rev rather than hardcoding them: the
	// assertion is about the import agreeing with the repository it read,
	// not about a particular release count.
	if _, err := os.Stat(filepath.Join(clone, "forge.yaml")); err != nil {
		t.Fatalf("%s is not a forge project: %v", clone, err)
	}
	return clone
}

// expectedCounts is the "N release(s), M promotion(s) across K
// environment(s)" line the dry run must print, derived from git rather than
// hardcoded.
//
// Counted with git, not written into the test, because the ledger grows: a
// hardcoded 40/17/3 would turn every future release into a failing
// acceptance test, and someone would then "fix" it by editing the number —
// which is the one change that makes the assertion vacuous.
func expectedCounts(t *testing.T, clone, rev string) string {
	t.Helper()
	releases := len(acceptanceReleaseFiles(t, clone, rev))
	envs := acceptanceEnvs(t, clone, rev)
	promotions := 0
	for _, env := range envs {
		promotions += len(acceptancePromotionLines(t, clone, rev, env))
	}
	t.Logf("the repository holds %d release file(s), %d promotion(s), %d env(s): %s",
		releases, promotions, len(envs), strings.Join(envs, ", "))
	return pluralCounts(releases, promotions, len(envs))
}

func pluralCounts(releases, promotions, envs int) string {
	return fmt.Sprintf("%d release(s), %d promotion(s) across %d environment(s)", releases, promotions, envs)
}

// acceptanceReleaseFiles lists .forge/releases/*.json at rev.
func acceptanceReleaseFiles(t *testing.T, clone, rev string) []string {
	t.Helper()
	entries, err := gitTreeEntries(context.Background(), clone, rev, retiredReleasesDirRel)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.name, ".json") {
			out = append(out, e.name)
		}
	}
	return out
}

// acceptanceEnvs is every env with a promotion log at rev, sorted.
func acceptanceEnvs(t *testing.T, clone, rev string) []string {
	t.Helper()
	entries, err := gitTreeEntries(context.Background(), clone, rev, retiredPromotionsDirRel)
	if err != nil {
		t.Fatalf("%s holds no %s at %s — this checkout has nothing to import, so it cannot be the acceptance subject",
			clone, retiredPromotionsDirRel, rev)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.name, ".jsonl") {
			out = append(out, strings.TrimSuffix(e.name, ".jsonl"))
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s at %s holds no promotion logs", retiredPromotionsDirRel, rev)
	}
	return out
}

// acceptancePromotionLines is one env's raw log lines at rev.
func acceptancePromotionLines(t *testing.T, clone, rev, env string) []string {
	t.Helper()
	out, err := gitLedgerCmd(context.Background(), clone, "show", rev+":"+retiredPromotionsDirRel+"/"+env+".jsonl")
	if err != nil {
		t.Fatalf("read %s's log at %s: %v", env, rev, err)
	}
	return ledgerLines(string(out))
}

// lastPromotedRelease is the release named by the LAST line of env's log —
// §11.1 step 7's `tail -1 prod.jsonl` check, as an assertion.
func lastPromotedRelease(t *testing.T, clone, rev, env string) string {
	t.Helper()
	lines := acceptancePromotionLines(t, clone, rev, env)
	if len(lines) == 0 {
		return ""
	}
	var last struct {
		Release string `json:"release"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode %s's last line: %v", env, err)
	}
	return last.Release
}
