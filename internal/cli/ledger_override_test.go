package cli

// FORGE_LEDGER=machine keeps a run off a DECLARED control plane.
//
// Every test here points the env's declaration at a TRIPWIRE server that
// fails the test the moment it is reached, and supplies a credential for it,
// so nothing but ledger selection stands between a command and that endpoint.
// "Never contacted" is therefore asserted, not inferred from a write that
// happened to land somewhere local.
//
// The regression these pin: control-plane's scripts/test-kata-prepull.sh set
// FORGE_LEDGER_HOME to a temp dir and ran `forge ledger import --apply`,
// believing the run private. prod declares forge.ControlPlane, so the import
// went to the production ledger and was stopped only by a plan conflict.
//
// None of these is parallel: they set process environment, and
// ledger_override.go reads FORGE_LEDGER from it by design (an inherited
// variable is the point of the mechanism).

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
)

// controlPlaneTripwire is a control plane that must never be called. It
// returns its URL and a hit counter; every request also fails the test.
func controlPlaneTripwire(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	hits := new(atomic.Int64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		t.Errorf("the declared control plane was contacted: %s %s", r.Method, r.URL.Path)
		http.Error(w, "tripwire: this control plane must not be reached", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits
}

// declareProdControlPlane makes dir a named project whose prod env declares a
// control plane at endpoint, with a credential for it in the environment.
//
// The ledger HOME is deliberately not redirected through the ledgerHome seam
// the other ledger tests use: these tests set $FORGE_LEDGER_HOME, because the
// variable is what a script sets and what the regression was about.
func declareProdControlPlane(t *testing.T, dir, endpoint string) {
	t.Helper()
	// A real forge.yaml so the command resolves dir as its project, plus
	// the name seam, which is what keys the machine ledger (see
	// newImportProject for why both).
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: tripwire-project\n"), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	writeProjectName(t, dir, "tripwire-project")
	declareEnvDir(t, dir, "prod")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_tripwire")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fmt.Sprintf(
		`{"output":{"control_plane":{"type":"control_plane","endpoint":%q,"token_env":"FORGE_TEST_CP_TOKEN"}}}`, endpoint)))
}

// sourceFileLedger is a machine-ledger directory holding one release and one
// prod promotion, written by the store itself.
func sourceFileLedger(t *testing.T) string {
	t.Helper()
	store, err := ledgerfile.Open(t.TempDir(), "source-project")
	if err != nil {
		t.Fatalf("open the source ledger: %v", err)
	}
	if _, err := store.CutRelease(importableRelease("v1.0.0")); err != nil {
		t.Fatalf("seed a release: %v", err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.ImportPromotionFrom(importablePromotion("id-1", "prod", "v1.0.0", at), "git:x@y"); err != nil {
		t.Fatalf("seed a promotion: %v", err)
	}
	return store.Dir()
}

// captureLedgerOverrideNotices redirects the override notice into a buffer
// and forgets which envs were already announced, so this test sees its own.
func captureLedgerOverrideNotices(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := ledgerOverrideNotices
	ledgerOverrideNotices = &buf
	forget := func() {
		ledgerOverrideNoticed.Range(func(k, _ any) bool { ledgerOverrideNoticed.Delete(k); return true })
	}
	forget()
	t.Cleanup(func() { ledgerOverrideNotices = prev; forget() })
	return &buf
}

// THE regression. With FORGE_LEDGER=machine, `ledger import --apply` for an
// env that declares a control plane records in the machine ledger under
// $FORGE_LEDGER_HOME and never reaches the control plane — neither the
// plan's reads nor the write.
func TestLedgerImport_MachineOverrideNeverContactsTheDeclaredControlPlane(t *testing.T) {
	endpoint, hits := controlPlaneTripwire(t)
	dir := t.TempDir()
	declareProdControlPlane(t, dir, endpoint)
	home := t.TempDir()
	t.Setenv(ledgerfile.DefaultHomeEnv, home)
	t.Setenv("FORGE_LEDGER", "machine")
	notices := captureLedgerOverrideNotices(t)

	out, err := runImport(t, dir, "--from-file-ledger", sourceFileLedger(t), "--apply")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the declared control plane received %d request(s); FORGE_LEDGER=machine must keep the import local", n)
	}

	store, err := openMachineLedgerIn(home, dir)
	if err != nil {
		t.Fatalf("open the machine ledger under $FORGE_LEDGER_HOME: %v", err)
	}
	got, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read promotions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "id-1" {
		t.Fatalf("machine ledger promotions = %+v, want the one imported (id-1)", got)
	}
	if !strings.Contains(out, "override:") || !strings.Contains(out, endpoint) {
		t.Errorf("the plan must say the override kept prod off %s, got:\n%s", endpoint, out)
	}
	if strings.Contains(out, "HOSTED") {
		t.Errorf("an overridden import must not describe a hosted target, got:\n%s", out)
	}
	if !strings.Contains(notices.String(), endpoint) {
		t.Errorf("the override notice must name the control plane it displaced, got %q", notices.String())
	}
}

// The render / deploy / status path: every consumer reads through selection,
// so the selected ledger serving reads locally is what keeps `forge env
// render prod` hermetic.
func TestSelectLedger_MachineOverrideReadsNothingFromTheDeclaredControlPlane(t *testing.T) {
	endpoint, hits := controlPlaneTripwire(t)
	dir := t.TempDir()
	declareProdControlPlane(t, dir, endpoint)
	t.Setenv(ledgerfile.DefaultHomeEnv, t.TempDir())
	t.Setenv("FORGE_LEDGER", "machine")
	captureLedgerOverrideNotices(t)

	l, err := selectLedger(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if l.Hosted {
		t.Fatal("FORGE_LEDGER=machine must select the machine ledger for an env that declares a control plane")
	}
	if _, ok := l.Bindings.(machineBindingStore); !ok {
		t.Errorf("bindings = %T, want machineBindingStore", l.Bindings)
	}
	if l.OverriddenEndpoint != endpoint {
		t.Errorf("OverriddenEndpoint = %q, want the declared %q", l.OverriddenEndpoint, endpoint)
	}
	if _, _, err := l.Bindings.Current(context.Background(), "prod"); err != nil {
		t.Fatalf("read the current promotion: %v", err)
	}
	if _, err := l.Releases.List(context.Background()); err != nil {
		t.Fatalf("list releases: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the declared control plane received %d request(s)", n)
	}
}

// A malformed FORGE_LEDGER is refused, never read as the default. The person
// who set it asked to stay off a control plane; `local` read as "declared"
// would send exactly that run to it. Refused even for an env with no KCL, so
// the typo surfaces on the first command rather than the first hosted one.
func TestLedgerSelection_UnrecognisedValueRefusesAndContactsNothing(t *testing.T) {
	endpoint, hits := controlPlaneTripwire(t)
	dir := t.TempDir()
	declareProdControlPlane(t, dir, endpoint)
	t.Setenv(ledgerfile.DefaultHomeEnv, t.TempDir())
	t.Setenv("FORGE_LEDGER", "local")

	for _, env := range []string{"prod", "env-with-no-kcl"} {
		_, err := selectLedger(context.Background(), dir, env)
		if err == nil {
			t.Fatalf("%s: FORGE_LEDGER=local must be refused", env)
		}
		if !strings.Contains(err.Error(), `FORGE_LEDGER="local"`) || !strings.Contains(err.Error(), "FORGE_LEDGER=machine") {
			t.Errorf("%s: the refusal must quote the value and name the valid one, got: %v", env, err)
		}
	}
	if _, err := runImport(t, dir, "--from-file-ledger", sourceFileLedger(t), "--apply"); err == nil {
		t.Fatal("an import under an unrecognised FORGE_LEDGER must be refused")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the declared control plane received %d request(s)", n)
	}
}

// FORGE_LEDGER_HOME alone is a LOCATION and does not select. Pinned on
// purpose: ledgerfile's network-filesystem refusal tells users to set it, and
// if it also chose the backend, every hosted env of such a user would read
// "never promoted" from an empty machine ledger. Selection only — this makes
// no call.
func TestLedgerSelection_HomeAloneDoesNotSelectTheMachineLedger(t *testing.T) {
	endpoint, hits := controlPlaneTripwire(t)
	dir := t.TempDir()
	declareProdControlPlane(t, dir, endpoint)
	t.Setenv(ledgerfile.DefaultHomeEnv, t.TempDir())
	t.Setenv("FORGE_LEDGER", "")

	l, err := selectLedger(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !l.Hosted || l.Bindings.Location() != endpoint {
		t.Fatalf("selection = hosted %v at %q; an env declaring a control plane keeps its ledger there unless FORGE_LEDGER=machine",
			l.Hosted, l.Bindings.Location())
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("selection alone must make no call, got %d", n)
	}
}

// snapshotImporter records what the output held at the moment Import ran.
type snapshotImporter struct {
	out     *bytes.Buffer
	atWrite string
}

func (s *snapshotImporter) Held(context.Context, []string) (ledgerHeld, error) {
	return ledgerHeld{}, nil
}

func (s *snapshotImporter) Import(context.Context, *ledgerImport) (ledgerImportResult, error) {
	s.atWrite = s.out.String()
	return ledgerImportResult{}, nil
}

// A hosted write is named on the line BEFORE it happens — asserted against
// what had been printed when the importer was called, not after.
func TestLedgerImport_NamesAHostedTargetBeforeWriting(t *testing.T) {
	var out bytes.Buffer
	importer := &snapshotImporter{out: &out}
	plan := ledgerImportPlan{Targets: []*ledgerImportTarget{{
		Location:   "https://cp.example.com",
		Hosted:     true,
		Importer:   importer,
		Envs:       []string{"prod"},
		Promotions: map[string][]sourcePromotion{},
	}}}
	if err := applyLedgerImport(context.Background(), &out, plan, "tripwire-project"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(importer.atWrite, "Writing to the HOSTED ledger at https://cp.example.com (environments: prod)") {
		t.Errorf("by the time the hosted write ran, the output must name it; it held:\n%s", importer.atWrite)
	}
}

// The plan labels a hosted target, and — when $FORGE_LEDGER_HOME is set, the
// exact misreading behind the regression — says that variable does not keep
// a declared env local.
func TestLedgerImport_PlanLabelsAHostedTargetAndTheHomeVariable(t *testing.T) {
	t.Setenv(ledgerfile.DefaultHomeEnv, t.TempDir())
	var out bytes.Buffer
	writeImportPlan(&out, ledgerSource{Label: "src", Promotions: map[string][]sourcePromotion{}},
		ledgerImportPlan{Targets: []*ledgerImportTarget{{
			Location: "https://cp.example.com", Hosted: true, Envs: []string{"prod"},
		}}}, true)
	got := out.String()
	for _, want := range []string{"HOSTED", "$FORGE_LEDGER_HOME relocates", "FORGE_LEDGER=machine does"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan must contain %q, got:\n%s", want, got)
		}
	}
}

// `ledger where` reports the override AS an override, with the declaration it
// displaced — never as an env that declares nothing.
func TestLedgerWhere_ReportsTheMachineOverride(t *testing.T) {
	endpoint, hits := controlPlaneTripwire(t)
	dir := t.TempDir()
	declareProdControlPlane(t, dir, endpoint)
	t.Setenv(ledgerfile.DefaultHomeEnv, t.TempDir())
	t.Setenv("FORGE_LEDGER", "machine")
	captureLedgerOverrideNotices(t)

	doc, err := ledgerWhereFor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("where: %v", err)
	}
	if doc.Backend != ledgerBackendMachine || doc.Override != "FORGE_LEDGER=machine" {
		t.Errorf("backend = %q, override = %q; want machine under FORGE_LEDGER=machine", doc.Backend, doc.Override)
	}
	if doc.Declaration == nil || doc.Declaration.Endpoint != endpoint {
		t.Errorf("declaration = %+v, want the overridden %q", doc.Declaration, endpoint)
	}
	if strings.Contains(doc.Because, "declares no forge.ControlPlane") {
		t.Errorf("an override must not read as a missing declaration: %q", doc.Because)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the declared control plane received %d request(s)", n)
	}
}

// Compile-time: the snapshot importer satisfies the seam the import uses.
var _ ledgerImporter = (*snapshotImporter)(nil)

// ─── The render's other control-plane call ──────────────────────────────────
//
// FORGE_LEDGER covers the ledger. The same hermetic render of prod made ONE
// more call — to learn the organization its push base is composed under —
// although prod hosts nothing for that base to judge. Found by running
// control-plane's kata pre-pull test behind a proxy that records every
// outbound connection.

// An env that declares a control plane and hosts nothing is judged without
// asking that control plane anything.
func TestHostedOffBaseImageFindings_NothingHostedContactsNoControlPlane(t *testing.T) {
	endpoint, hits := controlPlaneTripwire(t)
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_tripwire")
	// TestMain stubs the org lookup for the whole package, which would make
	// this assertion vacuous. Restore the REAL call for this test, so the
	// tripwire is reachable if anything asks.
	prevOrg := resolveControlPlaneOrg
	resolveControlPlaneOrg = func(ctx context.Context, env string, cp *ControlPlaneEntity) (string, error) {
		return resolveDeclarationOrg(ctx, env, &cloud.Declaration{Endpoint: cp.Endpoint, TokenEnv: cp.TokenEnv})
	}
	t.Cleanup(func() { resolveControlPlaneOrg = prevOrg })
	cp := &ControlPlaneEntity{Type: "control_plane", Endpoint: endpoint, TokenEnv: "FORGE_TEST_CP_TOKEN"}
	cp.bindOrgLookup("prod")
	e := &KCLEntities{
		ControlPlane: cp,
		Workloads:    []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster}}},
	}

	if got := hostedOffBaseImageFindings(e); len(got) != 0 {
		t.Errorf("findings = %+v, want none for an env that hosts nothing", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the declared control plane received %d request(s) for a push base nothing used", n)
	}
}

// The other half, so the laziness cannot become "never": a hosted image is
// still judged against the base the credential's org composes.
func TestHostedOffBaseImageFindings_AHostedImageIsStillJudged(t *testing.T) {
	calls := 0
	prevOrg := resolveControlPlaneOrg
	resolveControlPlaneOrg = func(context.Context, string, *ControlPlaneEntity) (string, error) {
		calls++
		return "acme", nil
	}
	t.Cleanup(func() { resolveControlPlaneOrg = prevOrg })
	prevName := hostedProjectName
	hostedProjectName = func() string { return "shop" }
	t.Cleanup(func() { hostedProjectName = prevName })

	cp := &ControlPlaneEntity{Endpoint: "https://cp.example.com", RegistryHost: "registry.example.com"}
	cp.bindOrgLookup("prod")
	e := &KCLEntities{ControlPlane: cp, Frontends: []FrontendEntity{{
		Name: "web", Image: "ghcr.io/elsewhere/web", Runtime: FrontendRuntime{Type: RuntimeHosted},
	}}}

	findings := hostedOffBaseImageFindings(e)
	if calls != 1 {
		t.Errorf("org lookups = %d, want 1: a hosted image is judged against the composed base", calls)
	}
	if len(verifiedOffBaseImages(findings)) != 1 {
		t.Errorf("findings = %+v, want ghcr.io/elsewhere/web verified off registry.example.com/acme/shop", findings)
	}
}
