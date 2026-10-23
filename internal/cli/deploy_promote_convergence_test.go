package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploystate"
)

// Tests for what a control-plane-ledger env's CONTROL PLANE converges of a
// release deploy, and for the deploy refusing — before it records or ships
// anything — when the answer is "nothing". Shaped by control-plane prod's
// 2026-10-09 deploy: see the header of deploy_promote_unpublished.go.

// prodShapedEntities is control-plane prod, reduced: a forge.ControlPlane
// ledger over cluster workloads and a Firebase frontend, with NO hosted tier.
func prodShapedEntities() *KCLEntities {
	return &KCLEntities{
		ControlPlane: &ControlPlaneEntity{
			Type: "control_plane", Endpoint: "https://cp.example.com/", TokenEnv: "FORGE_TEST_CP_TOKEN",
		},
		Workloads: []WorkloadEntity{
			clusterWL("admin-server", "gke_prod", "control-plane-prod"),
			clusterWL("reliant-api-server", "gke_prod", "control-plane-prod"),
		},
		Frontends: []FrontendEntity{{Name: "reliant-web", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}}},
	}
}

func apiTier() hostedTier { return hostedTier{Name: "api", Kind: hostedTierWorkload} }

// THE PREDICATE, at selection: a forge.ControlPlane env whose workloads all run
// on its own clusters is Mixed (forge applies them) AND binds no tier to its
// control plane — ledger-only. One hosted workload makes it a two-half env.
func TestLedgerForEntities_ControlPlaneEnvWithOnlyClusterWorkloadsIsLedgerOnly(t *testing.T) {
	dir := newLedgerTestProject(t, "ledger-only-selection")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")

	prod, err := ledgerForEntities("prod", prodShapedEntities(), dir)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !prod.Hosted || !prod.Mixed || prod.hubUnits() {
		t.Fatalf("prod shape: hosted=%v mixed=%v hub=%v, want true/true/false", prod.Hosted, prod.Mixed, prod.hubUnits())
	}
	if len(prod.HostedTiers) != 0 || !prod.ledgerOnly() {
		t.Fatalf("a control-plane env with only cluster workloads binds nothing to its control plane: "+
			"tiers=%v ledgerOnly=%v", prod.HostedTiers, prod.ledgerOnly())
	}

	withHosted := prodShapedEntities()
	withHosted.Workloads = append(withHosted.Workloads, hostedWL("api"))
	mixed, err := ledgerForEntities("prod", withHosted, dir)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if mixed.ledgerOnly() || len(mixed.HostedTiers) != 1 || mixed.HostedTiers[0] != apiTier() {
		t.Fatalf("a hosted workload is a tier its control plane converges: tiers=%v ledgerOnly=%v",
			mixed.HostedTiers, mixed.ledgerOnly())
	}
}

// THE PREDICATE, in the deploy: a ledger-only env (prod's shape, with running
// workloads) is applied from here and that is the whole deploy. Its control
// plane converges none of it, so there is no server-side rollout to wait on and
// an empty one is NOT "unpublished".
//
// Before the fix this deploy went on to the hosted half: on prod, the
// unpublished refusal fired after the cluster apply and the Firebase deploy had
// both shipped, and the command exited 2 claiming "nothing shipped". Here the
// observable is that the hosted wait ran at all.
func TestDeployRelease_LedgerOnlyEnvAppliesAndDoesNotWaitOrRefuse(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	out, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true},
		Follow: waitByDefault(),
	})
	if err != nil {
		t.Fatalf("a ledger-only env's deploy must succeed once its apply has: %v\n%s", err, out)
	}
	if len(apply.calls) != 1 {
		t.Fatalf("the apply ran %d time(s), want 1 — it is the whole deploy", len(apply.calls))
	}
	if len(wait.calls) != 0 {
		t.Fatalf("a ledger-only env waited on a server-side rollout %d time(s); its control plane converges "+
			"nothing of it, so there is nothing to wait for", len(wait.calls))
	}
	for _, bad := range []string{"no published workloads", "nothing to converge", "RECORDED BUT NOT APPLIED"} {
		if strings.Contains(out, bad) {
			t.Errorf("a ledger-only deploy that applied must not say %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "binds no tier to its control plane") {
		t.Errorf("the plan must say the control plane converges nothing of this env:\n%s", out)
	}
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v2" {
		t.Fatalf("prod is on %s, want v2", cur.Release)
	}
}

// THE ORDER: a deploy the convergence check refuses performs NO frontend
// dispatch and records NO promotion.
//
// The env binds a hosted workload its control plane has never published, and
// --frontends-only keeps this deploy from publishing it — so recording the
// release would converge nothing. Before the fix the refusal ran AFTER
// followPromote: the promotion was written, the client-side apply (which is
// where the Firebase dispatch lives) shipped the frontend, and only then did
// the deploy refuse.
func TestDeployRelease_ConvergenceRefusalRecordsNothingAndDispatchesNoFrontend(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	fake, store := hostedPromoteFixture(t, "v1")
	before := fake.callCount(procPromote)
	_, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true, HostedTiers: []hostedTier{apiTier()}},
		Follow: &promoteFollowOptions{clientDeploy: deployOptions{frontendsOnly: true}},
	})
	if err == nil {
		t.Fatal("a deploy whose control plane would converge nothing must be refused")
	}
	for _, want := range []string{"api (workload)", "--frontends-only", "NOTHING WAS RECORDED"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q:\n%v", want, err)
		}
	}
	if n := fake.callCount(procPromote); n != before {
		t.Fatalf("the refused deploy RECORDED a promotion (%d Promote call(s))", n-before)
	}
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v1" {
		t.Fatalf("the refused deploy moved prod to %s", cur.Release)
	}
	if len(apply.calls) != 0 {
		t.Fatalf("the refused deploy ran the client-side apply — and with it the frontend dispatch — %d time(s)", len(apply.calls))
	}
	if len(wait.calls) != 0 {
		t.Fatalf("the refused deploy waited %d time(s)", len(wait.calls))
	}
}

// The same verdict under --plan, and the plan document carries it: --plan is a
// read, so it must surface the refusal a real deploy would meet.
func TestDeployRelease_PlanCarriesTheConvergenceVerdict(t *testing.T) {
	var apply capturedClientDeploy
	apply.install(t)

	t.Run("first publish is named", func(t *testing.T) {
		_, store := hostedPromoteFixture(t, "v1")
		web := hostedTier{Name: "web", Kind: hostedTierFrontend}
		out, err := runHostedPromote(t, store, "v2", promoteOptions{
			DryRun: true, JSON: true,
			Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true, HostedTiers: []hostedTier{apiTier(), web}},
			Follow: waitByDefault(),
		})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		var doc struct {
			Converges *promotePlanConverges `json:"converges"`
		}
		if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
			t.Fatalf("decode: %v\n%s", jerr, out)
		}
		if doc.Converges == nil || len(doc.Converges.FirstPublish) != 2 || len(doc.Converges.Unpublished) != 0 {
			t.Fatalf("both tiers are unpublished and this deploy publishes both: %+v", doc.Converges)
		}
	})

	t.Run("refusal under --plan", func(t *testing.T) {
		fake, store := hostedPromoteFixture(t, "v1")
		before := fake.callCount(procPromote)
		_, err := runHostedPromote(t, store, "v2", promoteOptions{
			DryRun: true,
			Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true, HostedTiers: []hostedTier{apiTier()}},
			Follow: &promoteFollowOptions{clientDeploy: deployOptions{dryRun: true}},
		})
		if err == nil || !strings.Contains(err.Error(), "api (workload)") {
			t.Fatalf("--plan must show the refusal naming the unpublished tier, got %v", err)
		}
		if fake.callCount(procPromote) != before {
			t.Fatal("--plan wrote a promotion")
		}
	})
}

// convergenceVerdict over plain values: when to refuse, and what the plan says.
func TestConvergenceVerdict(t *testing.T) {
	web := hostedTier{Name: "web", Kind: hostedTierFrontend}
	both := []hostedTier{apiTier(), web}
	cases := []struct {
		name       string
		publishing []hostedTier
		published  []string
		readErr    error
		refuse     bool
		first      int
		unpub      int
	}{
		{"publishes everything, nothing published yet", both, nil, nil, false, 2, 0},
		{"publishes nothing, nothing published", nil, nil, nil, true, 0, 2},
		{"publishes nothing, one already published", nil, []string{"api"}, nil, false, 0, 1},
		{"publishes the frontend only, nothing published", []hostedTier{web}, nil, nil, false, 1, 1},
		// Evidence missing is never a refusal.
		{"control plane unreadable", nil, nil, errors.New("boom"), false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv, err := convergenceVerdict("prod", &promotePlanConverges{ControlPlane: "https://cp", Tiers: both},
				tc.publishing, "--dry-run applies nothing", tc.published, tc.readErr)
			if (err != nil) != tc.refuse {
				t.Fatalf("refuse = %v, want %v (%v)", err != nil, tc.refuse, err)
			}
			if len(conv.FirstPublish) != tc.first || len(conv.Unpublished) != tc.unpub {
				t.Fatalf("first/unpublished = %v/%v, want %d/%d", conv.FirstPublish, conv.Unpublished, tc.first, tc.unpub)
			}
			if tc.readErr != nil && conv.PublishedUnread == "" {
				t.Error("an unreadable control plane must be said, not read as 'nothing published'")
			}
		})
	}
}

// A --target on an env with hosted tiers is refused by the hosted publish —
// which, on a mixed env, runs after the cluster half has applied. The
// preflight refuses it before anything is recorded.
func TestDeployRelease_TargetWithHostedTiersRefusedBeforeRecord(t *testing.T) {
	var apply capturedClientDeploy
	apply.install(t)

	fake, store := hostedPromoteFixture(t, "v1")
	before := fake.callCount(procPromote)
	_, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true, HostedTiers: []hostedTier{apiTier()}},
		Follow: &promoteFollowOptions{clientDeploy: deployOptions{targets: []string{"worker"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "--target") || !strings.Contains(err.Error(), "NOTHING WAS RECORDED") {
		t.Fatalf("want a pre-record --target refusal, got %v", err)
	}
	if fake.callCount(procPromote) != before || len(apply.calls) != 0 {
		t.Fatalf("refused, yet Promote ran %d time(s) and the apply %d time(s)", fake.callCount(procPromote)-before, len(apply.calls))
	}
}

// ─── Truthful reporting of a partly shipped deploy ───────────────────────────

// shippingApply stands in for the client-side apply: it records two stages on
// the deploy's ship log — the cluster apply, then the Firebase host — and
// fails the second when failFirebase is set. It is the shape of control-plane
// prod's 2026-10-09 16:24 deploy, whose cluster rolled and whose Firebase step
// then failed for want of the `firebase` binary.
func installShippingApply(t *testing.T, failFirebase bool) {
	t.Helper()
	prev := runPromoteClientDeploy
	promoteClientDeployStubbed = true
	runPromoteClientDeploy = func(_ context.Context, _ string, opts deployOptions) error {
		ship := opts.shipLog()
		if ship == nil {
			return nil // the preflight-only pass ships nothing
		}
		if err := ship.run([]string{"[k8s-cluster] cluster=gke_prod ns=control-plane-prod: admin-server"},
			func() error { return nil }); err != nil {
			return err
		}
		return ship.run([]string{"[firebase] site=reliant-prod: reliant-web"}, func() error {
			if failFirebase {
				return errors.New(`firebase deploy: exec: "firebase": executable file not found in $PATH`)
			}
			return nil
		})
	}
	t.Cleanup(func() {
		runPromoteClientDeploy = prev
		promoteClientDeployStubbed = false
	})
}

// A deploy that fails AFTER part of it shipped lists what shipped, in the text
// and in the document, and never says "nothing shipped". The ledger's apply
// record says so too, so a later plan does not read the release as never
// applied.
func TestDeployRelease_PartlyShippedDeployReportsWhatShipped(t *testing.T) {
	installShippingApply(t, true)
	dir, ledger := fileLedgerDeployFixture(t)

	out, err := deployToProd(t, dir, ledger, "v2")
	if err == nil {
		t.Fatal("the Firebase step failed; the deploy must fail")
	}
	for _, want := range []string{
		"RECORDED AND PARTLY SHIPPED",
		"shipped   [k8s-cluster] cluster=gke_prod ns=control-plane-prod: admin-server",
		"PARTLY    [firebase] site=reliant-prod: reliant-web",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report must contain %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"nothing shipped", "SHIPS NOTHING", "RECORDED BUT NOT APPLIED"} {
		if strings.Contains(out, bad) {
			t.Errorf("a deploy whose cluster apply shipped must not say %q:\n%s", bad, out)
		}
	}

	cur, _, _ := testStore(t, dir).CurrentPromotion("prod")
	applies, aerr := testStore(t, dir).Applies("prod")
	if aerr != nil || len(applies) != 1 || applies[0].Apply.PromotionID != cur.ID {
		t.Fatalf("want one apply record for the promotion, got %d (%v)", len(applies), aerr)
	}
	if summary := applies[0].Outcome.Summary; !strings.HasPrefix(summary, "PARTLY APPLIED") || !strings.Contains(summary, "[k8s-cluster]") {
		t.Errorf("the ledger's apply record must say what shipped before the failure, got %q", summary)
	}
}

// The same deploy succeeding: the footer reports what shipped instead of the
// pre-V3 "SHIPS NOTHING … until you run the deploy below", and the document's
// ships_nothing is false.
func TestDeployRelease_SuccessfulDeployReportsWhatShipped(t *testing.T) {
	installShippingApply(t, false)
	dir, ledger := fileLedgerDeployFixture(t)

	out, err := deployToProd(t, dir, ledger, "v2")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !strings.Contains(out, "shipped   [firebase] site=reliant-prod: reliant-web") {
		t.Errorf("the footer must list what shipped:\n%s", out)
	}
	for _, bad := range []string{"SHIPS NOTHING", "Deploy:   forge env deploy prod"} {
		if strings.Contains(out, bad) {
			t.Errorf("a deploy that shipped must not print %q:\n%s", bad, out)
		}
	}

	opts := promoteOptions{ProjectDir: dir, Git: allCommitsPresent(), Follow: waitByDefault(), Ledger: ledger, JSON: true}
	opts.Run.None = true
	var jerr error
	doc := captureStdout(t, func() { jerr = runPromote(context.Background(), "v1", "prod", opts) })
	if jerr != nil {
		t.Fatalf("deploy --json: %v", jerr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("decode: %v\n%s", err, doc)
	}
	if parsed["ships_nothing"] != false {
		t.Errorf("ships_nothing = %v after a deploy that shipped, want false", parsed["ships_nothing"])
	}
	if shipped, _ := parsed["shipped"].([]any); len(shipped) != 2 {
		t.Errorf("shipped = %v, want both stages", parsed["shipped"])
	}
}

// ─── Other refusals that depend on nothing the apply does ────────────────────

// A pinned env refuses the deploy BEFORE the binding moves. The pin used to be
// read by the apply, after the promotion had been recorded.
func TestDeployRelease_PinnedEnvRefusedBeforeRecord(t *testing.T) {
	var apply capturedClientDeploy
	apply.install(t)
	dir, ledger := fileLedgerDeployFixture(t)
	store := deploystate.NewLocal(dir)
	if err := store.SetPolicy(context.Background(), "prod", deploystate.PolicyPinned); err != nil {
		t.Fatal(err)
	}
	before, _, _ := testStore(t, dir).CurrentPromotion("prod")

	_, err := deployToProd(t, dir, ledger, "v2")
	if err == nil || !strings.Contains(err.Error(), "NOTHING WAS RECORDED") {
		t.Fatalf("a pinned env must refuse before recording, got %v", err)
	}
	after, _, _ := testStore(t, dir).CurrentPromotion("prod")
	if after.ID != before.ID {
		t.Fatalf("the pinned env's binding moved: %q → %q", before.ID, after.ID)
	}
	if len(apply.calls) != 0 || len(apply.preflights) != 0 {
		t.Fatalf("refused, yet the apply ran %d time(s) and the cluster preflight %d", len(apply.calls), len(apply.preflights))
	}
}

// The frontend dispatch's own refusals run in the preflight: a Firebase
// frontend with no `firebase` CLI installed is refused before the cluster half
// ships, which is the half-shipped 16:24 prod deploy.
func TestPreflightFrontendDeploys_MissingFirebaseCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	web := filepath.Join(dir, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	entities := &KCLEntities{Frontends: []FrontendEntity{{
		Name: "reliant-web", Path: web, Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase},
	}}}
	err := preflightFrontendDeploys(context.Background(), entities, dir)
	if err == nil || !strings.Contains(err.Error(), "reliant-web") || !strings.Contains(err.Error(), "firebase") {
		t.Fatalf("want a refusal naming the frontend and the missing CLI, got %v", err)
	}

	// A host frontend ships nowhere from a deploy and needs no CLI.
	entities.Frontends[0].Runtime.Type = FrontendRuntimeHost
	if err := preflightFrontendDeploys(context.Background(), entities, dir); err != nil {
		t.Fatalf("a frontend this deploy does not ship must not be gated: %v", err)
	}
}

// The ship log itself: completed stages are shipped, a failed one is partly
// shipped, and the ledger annotation names both ahead of the error.
func TestShipLog(t *testing.T) {
	var nilLog *shipLog
	if err := nilLog.run([]string{"x"}, func() error { return nil }); err != nil || nilLog.changedAnything() {
		t.Fatal("a nil log must be inert")
	}
	l := &shipLog{}
	_ = l.run([]string{"cluster"}, func() error { return nil })
	boom := errors.New("boom")
	if err := l.run([]string{"firebase"}, func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("run must return fn's error, got %v", err)
	}
	if got := l.annotate(boom).Error(); got != "PARTLY APPLIED (shipped cluster; partly applied firebase): boom" {
		t.Fatalf("annotate = %q", got)
	}
	if !errors.Is(l.annotate(boom), boom) {
		t.Fatal("annotate must wrap, so exit codes survive")
	}
	if (&shipLog{}).annotate(boom) != boom {
		t.Fatal("a deploy that shipped nothing records its error unchanged")
	}
}

// ─── The frontend preflight is a finding for a plan, a refusal for a deploy ───

// firebaseFrontendProject is a project dir with one Firebase frontend and no
// `firebase` on PATH — control-plane prod in a sandbox.
func firebaseFrontendProject(t *testing.T) (string, *KCLEntities) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	web := filepath.Join(dir, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, &KCLEntities{Frontends: []FrontendEntity{{
		Name: "reliant-web", Path: web, Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase},
	}}}
}

// stubPreflightFrontend replaces the client-side apply with one that runs the
// frontend step of runDeploy's preflight-only pass against entities, which is
// the only part of the real apply these tests are about. It returns the count
// of real (non-preflight) applies.
func stubPreflightFrontend(t *testing.T, dir string, entities *KCLEntities) *int {
	t.Helper()
	applies := new(int)
	prev := runPromoteClientDeploy
	runPromoteClientDeploy = func(ctx context.Context, _ string, opts deployOptions) error {
		if !opts.preflightOnly {
			*applies++
			return nil
		}
		return opts.preflightFrontend(ctx, entities, dir)
	}
	t.Cleanup(func() { runPromoteClientDeploy = prev })
	return applies
}

func TestDeployOptions_PreflightFrontend(t *testing.T) {
	dir, entities := firebaseFrontendProject(t)

	// Strict (a real deploy): refused, naming the frontend and the CLI.
	err := deployOptions{}.preflightFrontend(context.Background(), entities, dir)
	if err == nil || !strings.Contains(err.Error(), "reliant-web") || !strings.Contains(err.Error(), "--skip-frontend") {
		t.Fatalf("strict preflight must refuse with the fix, got %v", err)
	}

	// Collecting (a plan): no error, one named finding.
	var findings []frontendPreflightFinding
	if err := (deployOptions{frontendFindings: &findings}).preflightFrontend(context.Background(), entities, dir); err != nil {
		t.Fatalf("a collecting preflight must not fail: %v", err)
	}
	if len(findings) != 1 || findings[0].Check != "frontend_firebase_cli" ||
		len(findings[0].Frontends) != 1 || findings[0].Frontends[0] != "reliant-web" {
		t.Fatalf("want one frontend_firebase_cli finding naming reliant-web, got %+v", findings)
	}

	// --skip-frontend waives it in both modes.
	findings = nil
	if err := (deployOptions{skipFrontend: true}).preflightFrontend(context.Background(), entities, dir); err != nil {
		t.Fatalf("--skip-frontend must waive the strict preflight: %v", err)
	}
	if err := (deployOptions{skipFrontend: true, frontendFindings: &findings}).preflightFrontend(context.Background(), entities, dir); err != nil || len(findings) != 0 {
		t.Fatalf("--skip-frontend must waive the finding too: err=%v findings=%+v", err, findings)
	}
}

// Every failing check is reported, not only the first.
func TestCheckFrontendDeploys_ReportsEveryFinding(t *testing.T) {
	dir, entities := firebaseFrontendProject(t)
	if err := os.WriteFile(filepath.Join(entities.Frontends[0].Path, ".env.local"), []byte("NEXT_PUBLIC_MOCK_API=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checks := map[string]bool{}
	for _, f := range checkFrontendDeploys(context.Background(), entities, dir) {
		checks[f.Check] = true
	}
	if !checks["frontend_env_files"] || !checks["frontend_firebase_cli"] {
		t.Fatalf("want both the dotenv gate and the missing CLI reported, got %v", checks)
	}
}

// --plan / --plan-only / --dry-run print the whole plan and name the missing
// CLI as its own finding; a real deploy of the same env still refuses.
func TestDeployRelease_FrontendPreflight_PlanReportsDeployRefuses(t *testing.T) {
	cases := []struct {
		name       string
		opts       func(*promoteOptions)
		follow     func(*promoteFollowOptions)
		wantRefuse bool
	}{
		{name: "--plan", opts: func(o *promoteOptions) { o.DryRun = true }},
		{name: "--plan-only", opts: func(o *promoteOptions) {
			o.Confirm = &deployConfirm{PlanOnly: true}
		}},
		{name: "--dry-run", follow: func(f *promoteFollowOptions) { f.clientDeploy.dryRun = true }},
		{name: "real deploy", wantRefuse: true, opts: func(o *promoteOptions) {
			o.Confirm = &deployConfirm{Yes: true}
		}},
		{name: "real deploy with --skip-frontend", opts: func(o *promoteOptions) {
			o.Confirm = &deployConfirm{Yes: true}
		}, follow: func(f *promoteFollowOptions) { f.clientDeploy.skipFrontend = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, entities := firebaseFrontendProject(t)
			applies := stubPreflightFrontend(t, dir, entities)
			store, releases := selfManagedFixture()

			follow := &promoteFollowOptions{}
			if tc.follow != nil {
				tc.follow(follow)
			}
			opts := promoteOptions{
				ProjectDir: dir, Git: allCommitsPresent(), Follow: follow,
				Ledger: envLedger{Bindings: store, Releases: releases},
			}
			opts.Run.None = true
			if tc.opts != nil {
				tc.opts(&opts)
			}
			var err error
			out := captureStdout(t, func() { err = runPromote(context.Background(), "v2", "prod", opts) })

			if tc.wantRefuse {
				if *applies != 0 {
					t.Fatalf("a refused deploy reached the apply %d time(s)", *applies)
				}
				if err == nil || !strings.Contains(err.Error(), "NOTHING WAS RECORDED") || !strings.Contains(err.Error(), "firebase") {
					t.Fatalf("a real deploy must still refuse before recording, got %v", err)
				}
				if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v1" {
					t.Fatalf("the refused deploy moved the binding to %s", cur.Release)
				}
				return
			}
			if err != nil {
				t.Fatalf("must not refuse: %v\n%s", err, out)
			}
			if !strings.Contains(out, "PLAN") && !strings.Contains(out, "direction") {
				// --plan-only prints through the confirmation gate.
				t.Fatalf("the plan must still be printed:\n%s", out)
			}
			if (tc.name == "--plan" || tc.name == "--plan-only") && *applies != 0 {
				t.Fatalf("%s reached the real apply %d time(s)", tc.name, *applies)
			}
			skipped := strings.Contains(tc.name, "skip-frontend")
			named := strings.Contains(out, "FRONTEND PREFLIGHT  frontend_firebase_cli")
			if skipped == named {
				t.Fatalf("finding named = %v with --skip-frontend = %v:\n%s", named, skipped, out)
			}
		})
	}
}

// --json carries the finding as its own field beside the plan.
func TestDeployRelease_FrontendPreflight_JSONField(t *testing.T) {
	dir, entities := firebaseFrontendProject(t)
	stubPreflightFrontend(t, dir, entities)
	store, releases := selfManagedFixture()
	opts := promoteOptions{
		ProjectDir: dir, Git: allCommitsPresent(), Follow: &promoteFollowOptions{}, JSON: true, DryRun: true,
		Ledger: envLedger{Bindings: store, Releases: releases},
	}
	opts.Run.None = true
	var err error
	doc := captureStdout(t, func() { err = runPromote(context.Background(), "v2", "prod", opts) })
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}
	var parsed struct {
		Direction         string                     `json:"direction"`
		FrontendPreflight []frontendPreflightFinding `json:"frontend_preflight"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("decode: %v\n%s", err, doc)
	}
	if parsed.Direction == "" || len(parsed.FrontendPreflight) != 1 || parsed.FrontendPreflight[0].Check != "frontend_firebase_cli" {
		t.Fatalf("want the plan plus one named finding, got %+v\n%s", parsed, doc)
	}
}
