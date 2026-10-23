package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// gitInit makes dir a git repository with one commit, so the refusal reads
// the checkout AT HEAD — which is what it does in a real project, because
// the retired ledger was committed.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	// A README guarantees there is something to commit. Without it a
	// project with no retired ledger has an empty index, `git commit`
	// fails, and the helper SKIPS — which silently turned the
	// "no refusal without a retired ledger" test into a no-op.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("seed the repo: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "-A"},
		{"commit", "-q", "-m", "ledger"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v: %s", args, err, out)
		}
	}
}

// writeRetiredLedger plants the in-checkout ledger forge no longer reads:
// n promotions for env, and one release file per version.
func writeRetiredLedger(t *testing.T, dir, env string, promotions []release.Promotion, versions []string) {
	t.Helper()
	if len(promotions) > 0 {
		path := filepath.Join(dir, retiredPromotionsDirRel, env+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		var lines strings.Builder
		for _, p := range promotions {
			line, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			lines.Write(line)
			lines.WriteByte('\n')
		}
		if err := os.WriteFile(path, []byte(lines.String()), 0o600); err != nil {
			t.Fatalf("write the retired log: %v", err)
		}
	}
	for _, v := range versions {
		path := filepath.Join(dir, retiredReleasesDirRel, v+".json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// The retired files were statefile envelopes: {"release": {...}}.
		body := fmt.Sprintf(`{"release":{"release":%q,"git":{},"created_at":%q,"artifacts":{}}}`,
			v, time.Now().UTC().Format(time.RFC3339))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write the retired release: %v", err)
		}
	}
}

func retiredPromotion(env, version, id string) release.Promotion {
	return release.Promotion{
		ID:         id,
		Env:        env,
		Release:    version,
		Kind:       release.KindPromote,
		Resolved:   map[string]string{"api": "sha256:" + strings.Repeat("a", 64)},
		PromotedAt: time.Now().UTC().Truncate(time.Second),
	}
}

// TestRefusesWhenCheckoutHoldsAnUnimportedLedger is the central safety
// property of this change.
//
// control-plane's checkout holds committed promotions for prod. After the
// in-checkout backend is deleted, nothing reads them — so a `forge env
// deploy prod` that merely consulted the new, EMPTY ledger would conclude
// "never promoted" and fall back to deploying mutable tags instead of the
// digests those promotions froze. That is the single failure this whole
// design exists to prevent, and it would happen silently, on the most
// important env, during a release.
//
// So selection REFUSES, naming how many promotions and releases are
// unimported and the command that imports them.
//
// Mutation: delete the checkLedgerImported call in ledgerFor and this fails.
func TestRefusesWhenCheckoutHoldsAnUnimportedLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newLedgerTestProject(t, "cp-like")
	writeRetiredLedger(t, dir, "prod", []release.Promotion{
		retiredPromotion("prod", "v1.5.18", "p-1"),
		retiredPromotion("prod", "v1.6.0", "p-2"),
		retiredPromotion("prod", "v1.7.12", "p-3"),
	}, []string{"v1.5.18", "v1.6.0", "v1.7.12", "v1.0.0"})
	gitInit(t, dir)

	_, err := ledgerFor(context.Background(), dir, "prod")
	if !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("a checkout holding promotions the ledger lacks must REFUSE "+
			"(otherwise a versionless deploy ships mutable tags); got %v", err)
	}
	msg := err.Error()
	// The counts must be real, not a vague "some": an operator decides
	// whether to import based on how much history is at stake.
	if !strings.Contains(msg, "3 promotions") {
		t.Errorf("the refusal must name N promotions, got: %s", msg)
	}
	// 3, not 4: v1.0.0 is in the checkout but no prod promotion binds it,
	// so it is not prod's history (TestRefusalIsScopedToTheEnvsOwnHistory).
	if !strings.Contains(msg, "3 releases") {
		t.Errorf("the refusal must name the M releases prod's promotions bind, got: %s", msg)
	}
	if !strings.Contains(msg, "forge ledger import --from-git") {
		t.Errorf("the refusal must name the remedy, got: %s", msg)
	}
	if !strings.Contains(msg, "prod") {
		t.Errorf("the refusal must name the env, got: %s", msg)
	}
}

// TestRefusalGoesQuietAfterImport is the other half, and the reason the
// comparison is by IDENTITY rather than by count: once the records are in
// the selected ledger, the refusal stops — with no flag to remember and no
// state beyond the ledger itself.
//
// This is what makes the refusal a transition guard rather than a permanent
// wall, and it pins the contract the import must honour: it PRESERVES each
// promotion's id. An import that minted fresh ids would leave every record
// looking absent and the refusal would never clear.
func TestRefusalGoesQuietAfterImport(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newLedgerTestProject(t, "imported-project")
	promotions := []release.Promotion{
		retiredPromotion("prod", "v1.5.18", "p-1"),
		retiredPromotion("prod", "v1.6.0", "p-2"),
	}
	writeRetiredLedger(t, dir, "prod", promotions, []string{"v1.5.18", "v1.6.0"})
	gitInit(t, dir)

	// Refuses before the import.
	if _, err := ledgerFor(context.Background(), dir, "prod"); !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("precondition: must refuse before the import, got %v", err)
	}

	// Simulate what `forge ledger import --from-git` does: record the same
	// records, with their ids preserved, in the selected ledger. Opened
	// through selectLedger — the seam that skips the check — which is the
	// seam the real import needs for exactly this reason.
	l, err := selectLedger(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("selectLedger: %v", err)
	}
	store := testStore(t, dir)
	for _, p := range promotions {
		if err := store.ImportPromotion(p); err != nil {
			t.Fatalf("import %s: %v", p.ID, err)
		}
	}
	for _, v := range []string{"v1.5.18", "v1.6.0"} {
		if _, err := l.Releases.Cut(context.Background(), release.Release{
			Version:   v,
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			Artifacts: map[string]release.Artifact{
				"ghcr.io/acme/api": {
					Kind:    release.KindOCI,
					Mode:    release.ModeShared,
					Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("b", 64)},
				},
			},
		}); err != nil {
			t.Fatalf("import release %s: %v", v, err)
		}
	}

	// And now it is quiet.
	if _, err := ledgerFor(context.Background(), dir, "prod"); err != nil {
		t.Fatalf("after the import the refusal must go quiet by construction, got %v", err)
	}
}

// TestPartialImportReportsOnlyTheRemainder: a resumed or re-run import must
// converge rather than refuse forever, so the count shrinks as records land.
func TestPartialImportReportsOnlyTheRemainder(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newLedgerTestProject(t, "partial-project")
	promotions := []release.Promotion{
		retiredPromotion("prod", "v1.0.0", "p-1"),
		retiredPromotion("prod", "v2.0.0", "p-2"),
		retiredPromotion("prod", "v3.0.0", "p-3"),
	}
	writeRetiredLedger(t, dir, "prod", promotions, nil)
	gitInit(t, dir)

	store := testStore(t, dir)
	if err := store.ImportPromotion(promotions[0]); err != nil {
		t.Fatalf("import: %v", err)
	}

	_, err := ledgerFor(context.Background(), dir, "prod")
	if !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("two promotions are still unimported, so it must still refuse; got %v", err)
	}
	if !strings.Contains(err.Error(), "2 promotions") {
		t.Errorf("a partial import must report only the REMAINDER, got: %s", err)
	}
}

// TestRefusalIsScopedToTheEnvsOwnHistory: the refusal guards ONE env's
// history, so an env the checkout never promoted must not refuse over
// releases cut for another env.
//
// This is control-plane's CI exactly. Its checkout holds prod's promotions
// and 40 releases; every push runs `forge env deploy dev-k8s` (and e2e) on a
// fresh runner whose machine ledger is empty. dev-k8s was never promoted
// through the retired ledger, so reading the empty ledger loses nothing —
// there is no frozen pin set for it to forget. Counting the checkout's
// releases project-wide made those envs refuse anyway, and no import could
// clear it: the import runs on the owner's machine, the runner starts empty.
//
// The env that DOES own history still refuses, and its count names only the
// releases its own promotions bind.
//
// Mutation: count every checkout release (not just the ones this env's
// promotions bind) and the dev-k8s half fails.
func TestRefusalIsScopedToTheEnvsOwnHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newLedgerTestProject(t, "cp-ci-like")
	writeRetiredLedger(t, dir, "prod", []release.Promotion{
		retiredPromotion("prod", "v1.6.0", "p-1"),
		retiredPromotion("prod", "v1.7.12", "p-2"),
	}, []string{"v1.0.0", "v1.6.0", "v1.7.12", "v1.7.13"})
	gitInit(t, dir)

	if _, err := ledgerFor(context.Background(), dir, "dev-k8s"); err != nil {
		t.Fatalf("an env the checkout never promoted has no history to lose, so it must not refuse; got %v", err)
	}

	_, err := ledgerFor(context.Background(), dir, "prod")
	if !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("prod's promotions are unimported, so prod must still refuse; got %v", err)
	}
	if !strings.Contains(err.Error(), "2 promotions") || !strings.Contains(err.Error(), "2 releases") {
		t.Errorf("prod's refusal must count its own promotions and the releases they bind, got: %s", err)
	}
}

// TestRefusalCountsAReleaseItsPromotionsBindButTheLedgerLacks: the promotions
// alone are not the whole history. A ledger that holds prod's promotions but
// not the release they bind would resolve `forge env deploy prod v1.7.12`
// to "no such release", so a missing BOUND release still refuses.
func TestRefusalCountsAReleaseItsPromotionsBindButTheLedgerLacks(t *testing.T) {
	dir := newLedgerTestProject(t, "half-imported")
	promotions := []release.Promotion{retiredPromotion("prod", "v1.7.12", "p-1")}
	writeRetiredLedger(t, dir, "prod", promotions, []string{"v1.7.12"})
	gitInit(t, dir)

	if err := testStore(t, dir).ImportPromotion(promotions[0]); err != nil {
		t.Fatalf("import: %v", err)
	}

	_, err := ledgerFor(context.Background(), dir, "prod")
	if !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("the promotion is in but the release it binds is not, so it must still refuse; got %v", err)
	}
	if !strings.Contains(err.Error(), "0 promotions") || !strings.Contains(err.Error(), "1 releases") {
		t.Errorf("the refusal must name the missing bound release, got: %s", err)
	}
}

// TestNoRefusalWithoutARetiredLedger pins that the refusal is narrow: a
// project that never used the in-checkout backend (every new project, and
// forge's own e2e fixtures) must be unaffected.
func TestNoRefusalWithoutARetiredLedger(t *testing.T) {
	dir := newLedgerTestProject(t, "clean-project")
	gitInit(t, dir)

	l, err := ledgerFor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("a project with no retired ledger must not refuse, got %v", err)
	}
	if l.Hosted {
		t.Fatal("no declaration means the machine ledger")
	}
	// And it is usable: the refusal must not have left the store unopened.
	if _, bound, err := l.Bindings.Current(context.Background(), "prod"); err != nil || bound {
		t.Fatalf("a fresh ledger reads as never-promoted, got (bound=%v, %v)", bound, err)
	}
}

// TestRefusalReadsHeadNotTheWorkingTree: the retired ledger was COMMITTED, so
// deleting the files without committing must not make the refusal vanish.
// Otherwise `rm -rf .forge/promotions` would look like a fix while the
// history — and the hazard — stayed in the repository.
func TestRefusalReadsHeadNotTheWorkingTree(t *testing.T) {
	dir := newLedgerTestProject(t, "head-project")
	writeRetiredLedger(t, dir, "prod", []release.Promotion{
		retiredPromotion("prod", "v1.0.0", "p-1"),
	}, nil)
	gitInit(t, dir)

	// Remove it from the WORKING TREE only; HEAD still has it.
	if err := os.RemoveAll(filepath.Join(dir, retiredPromotionsDirRel)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := ledgerFor(context.Background(), dir, "prod"); !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("the ledger is still in HEAD, so deleting the file must not silence the refusal; got %v", err)
	}
}

// TestRefusalAppliesToAHostedEnvToo is §11.3 proper: an env whose ledger
// moved to a control plane refuses on the same rule. The hazard is identical
// whichever store was selected, which is why the check lives in selection
// rather than in one backend.
func TestRefusalAppliesToAHostedEnvToo(t *testing.T) {
	dir := newLedgerTestProject(t, "hosted-project")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
	writeRetiredLedger(t, dir, "prod", []release.Promotion{
		retiredPromotion("prod", "v1.0.0", "p-1"),
		retiredPromotion("prod", "v2.0.0", "p-2"),
	}, nil)
	gitInit(t, dir)

	// A hosted ledger whose ListPromotions returns nothing: the control
	// plane has never heard of this env's history.
	l, err := ledgerForEntities("prod", &KCLEntities{
		ControlPlane: &ControlPlaneEntity{
			Type: "control_plane", Endpoint: "https://cp.example.com/", TokenEnv: "FORGE_TEST_CP_TOKEN",
		},
		Databases: []DatabaseEntity{{Name: "orders", Runtime: RuntimeHosted}},
	}, dir)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !l.Hosted {
		t.Fatal("precondition: this env must select the hosted ledger")
	}

	// Checked directly: reaching a real control plane is not this test's
	// subject, and the empty-history case is what §11.3 describes.
	err = checkLedgerImported(context.Background(), dir, "prod", envLedger{
		Bindings: emptyBindings{location: "https://cp.example.com"},
		Releases: emptyReleases{location: "https://cp.example.com"},
		Hosted:   true,
	})
	if !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("§11.3: an env whose ledger is on the control plane while the checkout holds "+
			"unimported promotions must refuse; got %v", err)
	}
	if !strings.Contains(err.Error(), "2 promotions") {
		t.Errorf("the refusal must name N, got: %s", err)
	}
}

// emptyBindings and emptyReleases are a ledger that holds nothing — the
// state of a control plane before the import runs.
type emptyBindings struct{ location string }

func (e emptyBindings) Current(context.Context, string) (release.Promotion, bool, error) {
	return release.Promotion{}, false, nil
}

func (e emptyBindings) Append(context.Context, release.Promotion, appendGuard) (release.Promotion, error) {
	return release.Promotion{}, errors.New("not used")
}

func (e emptyBindings) Location() string { return e.location }

func (e emptyBindings) HistoryPage(context.Context, string, historyQuery) (historyPage, error) {
	return historyPage{}, nil
}

type emptyReleases struct{ location string }

func (e emptyReleases) Cut(context.Context, release.Release) (bool, error) { return false, nil }
func (e emptyReleases) Get(context.Context, string) (*release.Release, error) {
	return nil, nil
}
func (e emptyReleases) List(context.Context) ([]release.Release, error) { return nil, nil }
func (e emptyReleases) Location() string                                { return e.location }
