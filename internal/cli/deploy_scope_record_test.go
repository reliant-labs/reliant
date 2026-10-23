package cli

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// promoteCalls counts the Promote requests the fake control plane received.
func (f *fakeDeployService) promoteCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.bodies {
		if b.Path == "/"+procPromote {
			n++
		}
	}
	return n
}

// D5: `forge env deploy prod v2 --target api` records the scope on the
// promotion, and the plan says so before anything is written.
func TestPromote_ScopedDeployRecordsItsTargets(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	// A ledger-only env: its control plane records promotions and converges
	// none of them, so this machine's scoped apply is the whole truth.
	ledger := envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true}
	out, err := runHostedPromote(t, store, "v2", promoteOptions{Ledger: ledger, Targets: []string{"api"}})
	if err != nil {
		t.Fatalf("scoped promote: %v", err)
	}
	if got := fake.lastPromoteBody(t)["targets"]; !reflect.DeepEqual(got, []any{"api"}) {
		t.Fatalf("Promote sent targets %v, want [api]", got)
	}
	if !strings.Contains(out, "scope     api") {
		t.Errorf("the plan does not show the scope:\n%s", out)
	}
	current, _, err := store.Current(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if current.Release != "v2" || !reflect.DeepEqual(current.Targets, []string{"api"}) {
		t.Fatalf("current = %s %v, want v2 scoped to api", current.Release, current.Targets)
	}

	// The whole deploy that follows completes it: a real move, and the plan
	// says what it moves rather than "no move".
	out, err = runHostedPromote(t, store, "v2", promoteOptions{Ledger: ledger})
	if err != nil {
		t.Fatalf("whole promote: %v", err)
	}
	if !strings.Contains(out, "COMPLETES A SCOPED DEPLOY") || !strings.Contains(out, "scoped: only api moved to v2") {
		t.Errorf("the plan does not describe completing the scoped deploy:\n%s", out)
	}
	after, _, _ := store.Current(context.Background(), "prod")
	if after.ID == current.ID || after.Scoped() {
		t.Fatalf("the whole deploy must append an unscoped entry: %+v", after)
	}
}

// Where the control plane converges promotions, recording a scoped promotion
// to a NEW release would move everything it converges: refused before any
// write. A scoped re-apply of the bound release moves no binding and runs.
func TestPromote_ScopedDeployIsRefusedWhereTheControlPlaneWouldWidenIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ledger func(*hostedStore) envLedger
		names  string
	}{
		{"hosted tiers", func(s *hostedStore) envLedger {
			return envLedger{Bindings: s, Releases: s, Hosted: true}
		}, "its hosted tiers"},
		{"hub units", func(s *hostedStore) envLedger {
			return envLedger{Bindings: s, Releases: s, Hosted: true, Mixed: true,
				Units: unitSplit{Hub: []string{"app"}, Source: unitSplitDeclared}}
		}, "its hub converges (app)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, store := hostedPromoteFixture(t, "v1")
			before := fake.promoteCalls()
			_, err := runHostedPromote(t, store, "v2", promoteOptions{Ledger: tc.ledger(store), Targets: []string{"api"}})
			if err == nil || !strings.Contains(err.Error(), "NOTHING WAS RECORDED") || !strings.Contains(err.Error(), tc.names) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tc.names)
			}
			if fake.promoteCalls() != before {
				t.Fatal("a refused scoped deploy wrote a promotion")
			}
			if _, err := runHostedPromote(t, store, "v1", promoteOptions{Ledger: tc.ledger(store), Targets: []string{"api"}}); err != nil {
				t.Fatalf("a scoped re-apply of the bound release must run: %v", err)
			}
		})
	}
}

func TestDeployScopeTargets(t *testing.T) {
	ctx := context.Background()
	prev := renderEntitiesForScope
	t.Cleanup(func() { renderEntitiesForScope = prev })
	// A --target scope is recorded whether or not the declaration renders.
	renderEntitiesForScope = func(context.Context, string, string) (*KCLEntities, error) {
		return nil, errors.New("no declaration here")
	}
	got, err := resolveDeployScope(ctx, t.TempDir(), "prod", []string{"web", "api", "web"}, false)
	if err != nil || !reflect.DeepEqual(got.Targets, []string{"api", "web"}) {
		t.Fatalf("--target scope = %v, %v", got.Targets, err)
	}
	if got, _ := resolveDeployScope(ctx, t.TempDir(), "prod", nil, false); got.Targets != nil || !got.AppliesBundle {
		t.Fatalf("an unscoped deploy records no targets and applies the bundle, got %+v", got)
	}

	renderEntitiesForScope = func(context.Context, string, string) (*KCLEntities, error) {
		return &KCLEntities{Frontends: []FrontendEntity{
			{Name: "site", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}},
			{Name: "admin-web", Runtime: FrontendRuntime{Type: FrontendRuntimeBucket}},
			{Name: "storybook", Runtime: FrontendRuntime{Type: FrontendRuntimeBuildOnly}},
		}}, nil
	}
	got, err = resolveDeployScope(ctx, t.TempDir(), "prod", nil, true)
	if err != nil || !reflect.DeepEqual(got.Targets, []string{"admin-web", "site"}) {
		t.Fatalf("--frontends-only scope = %v, %v; want the shippable frontends by name", got.Targets, err)
	}
	renderEntitiesForScope = func(context.Context, string, string) (*KCLEntities, error) { return &KCLEntities{}, nil }
	if _, err := resolveDeployScope(ctx, t.TempDir(), "prod", nil, true); err == nil {
		t.Fatal("--frontends-only with no shippable frontend must be refused")
	}
}

// The cut records the range against the project's OTHER releases, and records
// none — saying why — when it cannot list them.
func TestCaptureReleaseCommits(t *testing.T) {
	commit := strings.Repeat("a", 40)
	rel := release.Release{Version: "v3", Project: "shop", Provenance: &release.Provenance{Commit: commit}}

	if got := captureReleaseCommits(context.Background(), t.TempDir(), release.Release{Version: "v3"}, nil, io.Discard); got != nil {
		t.Fatalf("a release with no commit records no range, got %+v", got)
	}

	var warn strings.Builder
	broken := &listingLedger{err: errors.New("control plane unreachable")}
	if got := captureReleaseCommits(context.Background(), t.TempDir(), rel, broken, &warn); got != nil {
		t.Fatalf("an unlistable ledger must record no range, got %+v", got)
	}
	if !strings.Contains(warn.String(), "records no commit range") || !strings.Contains(warn.String(), "unreachable") {
		t.Fatalf("the note = %q", warn.String())
	}
}

// listingLedger is a releaseLedger whose List fails.
type listingLedger struct {
	err error
}

func (l *listingLedger) Cut(context.Context, release.Release) (bool, error)    { return false, l.err }
func (l *listingLedger) Get(context.Context, string) (*release.Release, error) { return nil, l.err }
func (l *listingLedger) List(context.Context) ([]release.Release, error)       { return nil, l.err }
func (l *listingLedger) Location() string                                      { return "test ledger" }
