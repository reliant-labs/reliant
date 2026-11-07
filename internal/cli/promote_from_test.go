package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// Tests for a release deploy's `--from` (control-plane
// docs/design/hosted-deploy-primitives.md §3.4, task F5).
//
// The hosted cases run the real hostedStore over the real cloud.Client
// against fakeDeployService, which models C6's server-side resolution: the
// version comes from the named source promotion, and a source that has moved
// past it is refused `source_moved`. So each case asserts what forge SENT and
// what it made of the answer, through the same bytes production carries.

// promoteFromFixture is a hosted ledger with releases v1..v3 cut, "staging"
// promoted along `stagingPromoted` and "prod" along `prodPromoted`, and the
// --from ledger seam pointed at it for BOTH environments — one control plane,
// which is what the same-control-plane guard requires.
func promoteFromFixture(t *testing.T, stagingPromoted, prodPromoted []string) (*fakeDeployService, *hostedStore) {
	t.Helper()
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid", "staging": "env-staging-uuid"})
	store, _ := newHostedTestStore(t, fake)
	ctx := context.Background()
	for i, v := range []string{"v1", "v2", "v3"} {
		r := ociRelease(v, map[string]string{"api": sha(string(rune('1' + i)))})
		r.CreatedAt = parseFixtureTime("2026-0" + string(rune('1'+i)) + "-01T00:00:00Z")
		if _, err := store.Cut(ctx, r); err != nil {
			t.Fatalf("cut %s: %v", v, err)
		}
	}
	seed := func(env string, versions []string) {
		for _, v := range versions {
			if _, err := store.Append(ctx, release.Promotion{Env: env, Release: v, Kind: release.KindPromote}, appendGuard{}); err != nil {
				t.Fatalf("seed promote %s → %s: %v", v, env, err)
			}
		}
	}
	seed("staging", stagingPromoted)
	seed("prod", prodPromoted)
	stubPromoteLedgers(t, map[string]promoteEnvLedger{
		"staging": {ControlPlane: sharedControlPlane, Ledger: envLedger{Bindings: store, Releases: store, Hosted: true}},
		"prod":    {ControlPlane: sharedControlPlane, Ledger: envLedger{Bindings: store, Releases: store, Hosted: true}},
	})
	return fake, store
}

// sharedControlPlane is the one declaration both environments in the fixture
// carry. Identical by value, which is what the guard checks.
var sharedControlPlane = &cloud.Declaration{Endpoint: "https://cp.example"}

// stubPromoteLedgers states each environment's ledger, so a --from test does
// not need two KCL trees and a control plane on disk to imply them.
func stubPromoteLedgers(t *testing.T, byEnv map[string]promoteEnvLedger) {
	t.Helper()
	prev := promoteEnvLedgerFor
	t.Cleanup(func() { promoteEnvLedgerFor = prev })
	promoteEnvLedgerFor = func(_ context.Context, _, env string) (promoteEnvLedger, error) {
		l, ok := byEnv[env]
		if !ok {
			t.Fatalf("the test did not state a ledger for env %q", env)
		}
		return l, nil
	}
}

// THE HEADLINE: `--from staging` with no version promotes what staging runs,
// and sends the SOURCE PROMOTION ID so the server — not forge — resolves the
// version under the target's lock.
func TestPromoteFrom_SendsTheSourcePromotionID(t *testing.T) {
	fake, store := promoteFromFixture(t, []string{"v1", "v2"}, []string{"v1"})
	sourceCurrent, _, err := store.Current(context.Background(), "staging")
	if err != nil {
		t.Fatal(err)
	}

	out, err := runHostedPromote(t, store, "", promoteOptions{From: promoteFromOptions{Env: "staging"}})
	if err != nil {
		t.Fatalf("promote --from staging: %v", err)
	}
	body := fake.lastPromoteBody(t)
	if got := body["fromPromotionId"]; got != sourceCurrent.ID {
		t.Fatalf("fromPromotionId = %v, want staging's current promotion %q (body %v)", got, sourceCurrent.ID, body)
	}
	if got := body["fromEnvironmentId"]; got != "env-staging-uuid" {
		t.Errorf("fromEnvironmentId = %v, want staging's id", got)
	}
	// AND NO VERSION (§3.4: "--from without a version sends
	// from_promotion_id and no version"). Sending one would make forge
	// assert a release it read outside the target's lock — the stale read
	// --from exists to eliminate — and would leave the server free to
	// trust the client's value instead of resolving it.
	if sent, present := body["version"]; present {
		t.Errorf("version %q was sent: the server must resolve it from fromPromotionId (body %v)", sent, body)
	}
	// The version the source runs, not the one prod was on.
	cur, _, _ := store.Current(context.Background(), "prod")
	if cur.Release != "v2" {
		t.Fatalf("prod is on %s, want staging's v2", cur.Release)
	}
	// FROMENV READS BACK AS A NAME, not the id it travels as (§3.4's
	// read-back fix). An id here is unreadable in `forge env status --history`.
	if cur.FromEnv != "staging" {
		t.Errorf("FromEnv = %q, want the NAME %q", cur.FromEnv, "staging")
	}
	if cur.FromPromotionID != sourceCurrent.ID {
		t.Errorf("FromPromotionID = %q, want %q — it is what makes the chain reconstructible", cur.FromPromotionID, sourceCurrent.ID)
	}
	if !strings.Contains(out, "v2") {
		t.Errorf("the report must name the release resolved from the source:\n%s", out)
	}
}

// A STALE --from-promotion IS A CONFLICT, NOT A SILENT UPGRADE. CI captures
// the id when QA signs off; if staging moves on before the promote runs, the
// pipeline must fail red rather than ship the newer build.
func TestPromoteFrom_StalePromotionIsSourceMoved(t *testing.T) {
	fake, store := promoteFromFixture(t, []string{"v1"}, []string{"v1"})
	captured, _, _ := store.Current(context.Background(), "staging")
	// Staging moves on after the id was captured.
	if _, err := store.Append(context.Background(),
		release.Promotion{Env: "staging", Release: "v2", Kind: release.KindPromote}, appendGuard{}); err != nil {
		t.Fatal(err)
	}

	out, err := runHostedPromote(t, store, "",
		promoteOptions{From: promoteFromOptions{Env: "staging", PromotionID: captured.ID}})
	if body := fake.lastPromoteBody(t); body["fromPromotionId"] != captured.ID {
		t.Fatalf("--from-promotion must send the CAPTURED id, not what staging runs now: %v", body)
	}
	var refused *promoteRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want a *promoteRefusedError, got %T: %v", err, err)
	}
	if refused.Reason != reasonSourceMoved {
		t.Errorf("reason = %q, want %q", refused.Reason, reasonSourceMoved)
	}
	// source_moved is a CONFLICT: someone else moved the source, so a
	// retry would ship something nobody approved.
	if got := exitCodeForError(err); got != exitConflict {
		t.Errorf("exit code = %d, want %d", got, exitConflict)
	}
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v1" {
		t.Errorf("a refused promote wrote: prod is on %s", cur.Release)
	}
	for _, want := range []string{"REFUSED", reasonSourceMoved, "NOTHING WRITTEN"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refused report must say %q, got:\n%s", want, out)
		}
	}
}

// An explicit version BESIDE --from states both what the caller wants and
// where it saw it. A disagreement means one of the two is about something
// else, so it is refused rather than silently resolved either way.
func TestPromoteFrom_ExplicitVersionMustMatchTheSource(t *testing.T) {
	_, store := promoteFromFixture(t, []string{"v1", "v2"}, []string{"v1"})

	// staging is on v2; asking for v3 "as seen on staging" is a lie.
	_, err := runHostedPromote(t, store, "v3", promoteOptions{From: promoteFromOptions{Env: "staging"}})
	if got := exitCodeForError(err); got != exitConflict {
		t.Fatalf("a version that disagrees with the source must exit %d, got %d (%v)", exitConflict, got, err)
	}
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v1" {
		t.Fatalf("a refused promote wrote: prod is on %s", cur.Release)
	}

	// The same version the source runs is admitted: provenance, verified.
	fake, store2 := promoteFromFixture(t, []string{"v1", "v2"}, []string{"v1"})
	if _, err := runHostedPromote(t, store2, "v2", promoteOptions{From: promoteFromOptions{Env: "staging"}}); err != nil {
		t.Fatalf("an explicit version that MATCHES the source must be admitted: %v", err)
	}
	// AN EXPLICIT VERSION *IS* SENT, unlike the no-version case. The
	// caller asserted two things — the release AND where they saw it —
	// and the server's must-agree check is what turns a disagreement into
	// a refusal. Dropping the version here would silently discard the
	// second assertion, so "promote v1.4.0, which I saw on staging" would
	// quietly become "promote whatever staging has".
	if got := fake.lastPromoteBody(t)["version"]; got != "v2" {
		t.Errorf("version = %v, want the caller's explicit v2 to be sent for the server's must-agree check", got)
	}
	cur, _, _ := store2.Current(context.Background(), "prod")
	if cur.Release != "v2" || cur.FromEnv != "staging" {
		t.Fatalf("want prod on v2 from staging, got %+v", cur)
	}
}

// Under `--from-promotion X` where X is not what the source runs now, the
// plan's target is the source's CURRENT release — forge cannot read X's, since
// a promotion is reachable through this ledger only as an environment's
// current entry. So the plan SAYS so, and names the refusal that is coming,
// rather than previewing a target the write would never bind.
func TestPromoteFrom_StalePromotionIsLabelledInThePlan(t *testing.T) {
	fake, store := promoteFromFixture(t, []string{"v1"}, []string{"v1"})
	captured, _, _ := store.Current(context.Background(), "staging")
	if _, err := store.Append(context.Background(),
		release.Promotion{Env: "staging", Release: "v2", Kind: release.KindPromote}, appendGuard{}); err != nil {
		t.Fatal(err)
	}
	before := fake.callCount(procPromote)

	out, err := runHostedPromote(t, store, "",
		promoteOptions{DryRun: true, JSON: true, From: promoteFromOptions{Env: "staging", PromotionID: captured.ID}})
	if err != nil {
		t.Fatalf("--plan must still render: %v", err)
	}
	if n := fake.callCount(procPromote); n != before {
		t.Fatalf("--plan called Promote %d time(s)", n-before)
	}
	var doc promotePlan
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, out)
	}
	for _, want := range []string{captured.ID, reasonSourceMoved, "v2"} {
		if !strings.Contains(doc.SourceNote, want) {
			t.Errorf("source_note must mention %q, got %q", want, doc.SourceNote)
		}
	}

	// A --from-promotion that IS current is unremarkable and must carry
	// no warning: a note on every --from-promotion would be noise, and
	// noise is how a real warning gets skipped.
	_, store2 := promoteFromFixture(t, []string{"v1", "v2"}, []string{"v1"})
	fresh, _, _ := store2.Current(context.Background(), "staging")
	out, err = runHostedPromote(t, store2, "",
		promoteOptions{DryRun: true, JSON: true, From: promoteFromOptions{Env: "staging", PromotionID: fresh.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var clean promotePlan
	if jerr := json.Unmarshal([]byte(out), &clean); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, out)
	}
	if clean.SourceNote != "" {
		t.Errorf("a current --from-promotion must carry no warning, got %q", clean.SourceNote)
	}
}

// The text plan renders the warning too — most operators never pass --json,
// and a warning only a machine sees is not a warning.
func TestPromoteFrom_StalePromotionWarningIsInTheTextPlan(t *testing.T) {
	_, store := promoteFromFixture(t, []string{"v1"}, []string{"v1"})
	captured, _, _ := store.Current(context.Background(), "staging")
	if _, err := store.Append(context.Background(),
		release.Promotion{Env: "staging", Release: "v2", Kind: release.KindPromote}, appendGuard{}); err != nil {
		t.Fatal(err)
	}

	out, err := runHostedPromote(t, store, "",
		promoteOptions{DryRun: true, From: promoteFromOptions{Env: "staging", PromotionID: captured.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, reasonSourceMoved) {
		t.Errorf("the text plan must warn that the promote will be refused:\n%s", out)
	}
}

// A file ledger has no server to resolve a release from a source promotion,
// so it refuses the directive rather than writing the plan's preview as if it
// had been verified. `--from` is already refused earlier for a file-ledger env
// (the same-control-plane guard); this is the backstop that keeps that the
// only way in.
func TestFileLedger_RefusesAVersionItWasNotGiven(t *testing.T) {
	store := testBindings(t, newLedgerTestProject(t, "scratch"))
	p := release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote,
		Resolved: map[string]string{"api": sha("a")}}

	_, err := store.Append(context.Background(), p, appendGuard{ResolveVersionFromSource: true})
	if err == nil || !strings.Contains(err.Error(), "promote by version") {
		t.Fatalf("want a refusal naming the way forward, got %v", err)
	}
	if history, _ := storeHistory(t, store, "prod"); len(history) != 0 {
		t.Fatalf("the refused append wrote %d entries", len(history))
	}
}

// THE SAME-CONTROL-PLANE GUARD, and it fires BEFORE any RPC: a release label
// does not name the same bytes across two ledgers, and the source_moved check
// needs the source promotion to be a row in the TARGET's control plane.
func TestPromoteFrom_CrossLedgerIsRefusedBeforeAnyRPC(t *testing.T) {
	// The org is the CREDENTIAL's, so each env names its credential by token_env
	// and the stubbed resolver maps that to an org.
	prevResolver := orgResolver
	orgResolver = func(_ context.Context, _ string, d *cloud.Declaration) (string, error) {
		return d.TokenEnv, nil
	}
	t.Cleanup(func() { orgResolver = prevResolver })
	hosted := func(store *hostedStore, endpoint, org string) promoteEnvLedger {
		return promoteEnvLedger{
			ControlPlane: &cloud.Declaration{Endpoint: endpoint, TokenEnv: org},
			Ledger:       envLedger{Bindings: store, Releases: store, Hosted: true},
		}
	}
	cases := map[string]func(*hostedStore) map[string]promoteEnvLedger{
		"a different endpoint": func(s *hostedStore) map[string]promoteEnvLedger {
			return map[string]promoteEnvLedger{
				"staging": hosted(s, "https://other.example", "acme"),
				"prod":    hosted(s, "https://cp.example", "acme"),
			}
		},
		"a different organization": func(s *hostedStore) map[string]promoteEnvLedger {
			return map[string]promoteEnvLedger{
				"staging": hosted(s, "https://cp.example", "other-org"),
				"prod":    hosted(s, "https://cp.example", "acme"),
			}
		},
		"a file-ledger source": func(s *hostedStore) map[string]promoteEnvLedger {
			return map[string]promoteEnvLedger{
				"staging": {Ledger: testLedger(t, t.TempDir())},
				"prod":    hosted(s, "https://cp.example", "acme"),
			}
		},
		"a file-ledger target": func(s *hostedStore) map[string]promoteEnvLedger {
			return map[string]promoteEnvLedger{
				"staging": hosted(s, "https://cp.example", "acme"),
				"prod":    {Ledger: testLedger(t, t.TempDir())},
			}
		},
	}
	for name, ledgers := range cases {
		t.Run(name, func(t *testing.T) {
			fake, store := promoteFromFixture(t, []string{"v1", "v2"}, []string{"v1"})
			stubPromoteLedgers(t, ledgers(store))
			before := fake.callCount(procPromote)

			_, err := runHostedPromote(t, store, "", promoteOptions{From: promoteFromOptions{Env: "staging"}})
			if !errors.Is(err, errPromoteFromCrossLedger) {
				t.Fatalf("want a cross-ledger refusal, got %v", err)
			}
			if !strings.Contains(err.Error(), "deploy by version instead") {
				t.Errorf("the refusal must name the way forward, got:\n%v", err)
			}
			// BEFORE ANY RPC: a promote that cannot verify its source
			// must not move the target's pointer first.
			if n := fake.callCount(procPromote); n != before {
				t.Fatalf("the guard fired AFTER writing: Promote called %d time(s)", n-before)
			}
		})
	}
}

// The input rules, each refused before the plan: a promotion id with no
// environment cannot be checked against anything, and an environment cannot
// be promoted from itself.
func TestPromoteFrom_InvalidFlagCombinations(t *testing.T) {
	cases := map[string]struct {
		from promoteFromOptions
		want string
	}{
		"--from-promotion without --from": {promoteFromOptions{PromotionID: "p-1"}, "needs --from"},
		// The message must name the FLAG. release.Promotion.Validate
		// refuses a self-promotion too, with its own sentence, so
		// asserting only "from itself" would pass even if this
		// pre-plan check were deleted — and the caller would be told
		// about a ledger entry rather than the flag they passed.
		"--from naming the target": {promoteFromOptions{Env: "prod"}, "--from prod is also the target"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake, store := promoteFromFixture(t, []string{"v1"}, []string{"v1"})
			before := fake.callCount(procPromote)
			_, err := runHostedPromote(t, store, "", promoteOptions{From: tc.from})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal saying %q, got %v", tc.want, err)
			}
			if n := fake.callCount(procPromote); n != before {
				t.Fatalf("refused AFTER writing: Promote called %d time(s)", n-before)
			}
		})
	}
}

// A source that has never been promoted has nothing to promote FROM, and
// saying so beats sending an empty version and letting the server guess.
func TestPromoteFrom_UnpromotedSourceIsRefused(t *testing.T) {
	fake, store := promoteFromFixture(t, nil, []string{"v1"})
	before := fake.callCount(procPromote)

	_, err := runHostedPromote(t, store, "", promoteOptions{From: promoteFromOptions{Env: "staging"}})
	if err == nil || !strings.Contains(err.Error(), "never been promoted") {
		t.Fatalf("want a refusal naming the unpromoted source, got %v", err)
	}
	if n := fake.callCount(procPromote); n != before {
		t.Fatalf("refused AFTER writing: Promote called %d time(s)", n-before)
	}
}

// --plan with --from previews the release the source runs and writes nothing:
// the preview a human reads before approving the promote.
func TestPromoteFrom_PlanResolvesTheSourceAndWritesNothing(t *testing.T) {
	fake, store := promoteFromFixture(t, []string{"v1", "v2"}, []string{"v1"})
	before := fake.callCount(procPromote)

	out, err := runHostedPromote(t, store, "",
		promoteOptions{DryRun: true, From: promoteFromOptions{Env: "staging"}})
	if err != nil {
		t.Fatalf("--plan --from: %v", err)
	}
	if n := fake.callCount(procPromote); n != before {
		t.Fatalf("--plan called Promote %d time(s)", n-before)
	}
	if !strings.Contains(out, "v2") {
		t.Errorf("the plan must name staging's release v2:\n%s", out)
	}
}

// sameControlPlane's own rules, stated directly: the guard is pure, so the
// comparison is pinned without a ledger or an RPC behind it. (The org half is
// sameOrganization, which asks each credential — see hosted_org_test.go.)
func TestSameControlPlane(t *testing.T) {
	decl := func(endpoint, _ string) *cloud.Declaration {
		return &cloud.Declaration{Endpoint: endpoint}
	}
	cases := []struct {
		name           string
		source, target *cloud.Declaration
		wantRefused    bool
	}{
		{"identical", decl("https://cp", "acme"), decl("https://cp", "acme"), false},
		{"a trailing slash is not a difference", decl("https://cp/", "acme"), decl("https://cp", "acme"), false},
		{"different endpoints", decl("https://a", "acme"), decl("https://b", "acme"), true},
		{"file-ledger source", nil, decl("https://cp", "acme"), true},
		{"file-ledger target", decl("https://cp", "acme"), nil, true},
		{"both file ledgers", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sameControlPlane("staging", "prod", tc.source, tc.target)
			if tc.wantRefused != (err != nil) {
				t.Fatalf("refused=%v (%v), want refused=%v", err != nil, err, tc.wantRefused)
			}
			if tc.wantRefused && !errors.Is(err, errPromoteFromCrossLedger) {
				t.Errorf("a cross-ledger refusal must be recognisable as one, got %v", err)
			}
		})
	}
}

// No --from is the overwhelmingly common promote, and it must stay a pure
// pass-through: no ledger resolved, no provenance invented.
func TestResolvePromoteFrom_WithoutTheFlagIsAPassThrough(t *testing.T) {
	stubPromoteLedgers(t, map[string]promoteEnvLedger{}) // any lookup fails the test
	got, err := resolvePromoteFrom(context.Background(), "v1", "prod", t.TempDir(), promoteFromOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != (promoteSource{Version: "v1"}) {
		t.Fatalf("resolvePromoteFrom = %+v, want only the version", got)
	}
}
