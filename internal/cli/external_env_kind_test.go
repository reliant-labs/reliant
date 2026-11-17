package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

// externalWL is a workload bound to forge.OnExternal with no hooks
// (record-only).
func externalWL(name string) WorkloadEntity {
	return WorkloadEntity{
		Name: name, Kind: "service",
		Runtime: RuntimeEntity{Type: RuntimeExternal, External: &deploytarget.ExternalRuntime{}},
		Spec:    deployv1alpha1.WorkloadSpec{Kind: deployv1alpha1.KindService, Image: "ghcr.io/acme/" + name},
	}
}

// An env forge applies through forge.OnExternal hooks is SELF_MANAGED when it
// declares a control plane: the control plane keeps its ledger and records
// its promotions (it refuses a LOCAL env's), and its secrets are write-only.
// With no control plane the kind is unchanged: none, and a LOCAL shape.
func TestExternalEnvKind(t *testing.T) {
	cp := &ControlPlaneEntity{Endpoint: "https://cp"}
	ext := externalWL("api")
	cases := []struct {
		name  string
		e     *KCLEntities
		want  deploytarget.HostedEnvKind
		shape release.EnvKind
	}{
		{"all OnExternal + control plane", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{ext, externalWL("worker")}},
			deploytarget.HostedEnvSelfManaged, release.EnvSelfManaged},
		{"OnExternal + host + control plane", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{ext, hostWL("tool")}},
			deploytarget.HostedEnvSelfManaged, release.EnvSelfManaged},
		{"OnExternal + hosted", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{ext, hostedWL("web")}},
			deploytarget.HostedEnvPersistent, release.EnvPersistent},
		{"all OnExternal, no control plane", &KCLEntities{Workloads: []WorkloadEntity{ext}}, "", release.EnvLocal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hostedEnvKindOf(c.e); got != c.want {
				t.Errorf("hostedEnvKindOf = %q, want %q", got, c.want)
			}
			if got := shapeEnvKindOf(c.e); got != c.shape {
				t.Errorf("shapeEnvKindOf = %q, want %q", got, c.shape)
			}
		})
	}
}

// `forge env deploy` of an all-OnExternal control-plane env is not refused as
// LOCAL, and its hosted ledger applies from this machine (the hooks run here)
// while the control plane only records: the ledger-only shape a self-managed
// cluster env has.
func TestExternalEnvAppliesLocallyAndIsLedgerOnly(t *testing.T) {
	e := &KCLEntities{ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp"}, Workloads: []WorkloadEntity{externalWL("api")}}
	if !envAppliesLocally(e) {
		t.Fatal("an OnExternal env's deploy hook runs on this machine; dispatchHostedDeploy would refuse it as LOCAL")
	}
	if forgeAppliesItselfLocally(e) {
		t.Fatal("an OnExternal part is the author's tooling, not a cluster/compose part (it needs no kubeconfig in CI)")
	}
	units, err := unitSplitOf(e)
	if err != nil {
		t.Fatal(err)
	}
	l := envLedger{Hosted: true, Units: units, HostedTiers: hostedTiersOf(e), Mixed: forgeAppliesLocally(e, units)}
	if !l.appliesLocally() || !l.ledgerOnly() {
		t.Fatalf("ledger = %+v: want it applied from here (appliesLocally) with the control plane only recording (ledgerOnly)", l)
	}
	if _, ok := envReportsSessions(e); ok {
		t.Fatal("an OnExternal env reported local sessions; the server confines sessions to LOCAL envs")
	}
}

// A self-managed OnExternal env's secrets are write-only: `forge env up` never
// pulls them, armed values are never served to it, and a host workload that
// needs them is refused with the control plane's own write-only wording rather
// than started without them.
func TestExternalEnvSecretsAreWriteOnly(t *testing.T) {
	e := localEnvEntities("https://cp.example.com")
	e.Workloads = append(e.Workloads, externalWL("worker"))
	if got := hostedEnvKindOf(e); got != deploytarget.HostedEnvSelfManaged {
		t.Fatalf("kind = %q, want self-managed", got)
	}
	prev := pullLocalSecretsFor
	t.Cleanup(func() { pullLocalSecretsFor = prev; disarmPulledSecrets() })
	pullLocalSecretsFor = func(context.Context, string, *KCLEntities) (map[string]string, error) {
		t.Fatal("a self-managed env's secret values were pulled")
		return nil, nil
	}
	if err := armLocalSecretsForUp(context.Background(), "prod", e, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	armPulledSecrets("prod", map[string]string{"STRIPE_SECRET_KEY": "sk_canary"})
	if _, ok := pulledSecretsFor(e); ok {
		t.Fatal("armed values were served to a self-managed env")
	}
	disarmPulledSecrets()

	_, _, err := localWorkloadConfig(context.Background(), t.TempDir(), "prod", e, e.Workloads[0], secretsRequired)
	if err == nil || !strings.Contains(err.Error(), "only LOCAL environments' secrets can be pulled; this environment's secrets are write-only") ||
		!strings.Contains(err.Error(), "SELF_MANAGED") {
		t.Fatalf("err = %v, want the write-only refusal naming SELF_MANAGED", err)
	}

	// A LOCAL env (host only) is not refused for the same workload.
	local := localEnvEntities("https://cp.example.com")
	if hostedSecretsWriteOnly(local) {
		t.Fatal("a LOCAL env's secrets are pullable, not write-only")
	}
}

// `forge release record` admits a KCL-declared env whose every workload is
// OnExternal (the team's own tooling deploys it), recording on the control
// plane the env declares; any other declared env is still refused before a
// cut, and so is one that does not render.
func TestReleaseRecord_AdmitsAnAllExternalDeclaredEnv(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "deploy", "kcl", "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "deploy", "kcl", "prod", "main.k"), []byte("# prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var render func() (*KCLEntities, error)
	prevRender := renderRecordEnv
	renderRecordEnv = func(context.Context, string, string) (*KCLEntities, error) { return render() }
	t.Cleanup(func() { renderRecordEnv = prevRender })

	cp := newFakeRecordCP()
	var opened *KCLEntities
	useRecordLedger(t, func(ctx context.Context, dir, project, env, token string, declared *KCLEntities) (recordLedger, error) {
		opened = declared
		return hostedRecordTarget(cp)(ctx, dir, project, env, token, declared)
	})
	sc := recordScope{projectDir: projectDir, project: "shop", getenv: func(string) string { return "" }}
	rec := func() error {
		_, err := recordReleaseIn(context.Background(),
			releaseRecordOptions{env: "prod", version: "v1", commit: recordCommitA}, sc, &bytes.Buffer{})
		return err
	}
	refused := func(err error) bool {
		var r *promoteRefusedError
		return errors.As(err, &r) && r.Reason == reasonRecordOnlyRelease
	}

	external := &KCLEntities{ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp.acme.example"},
		Workloads: []WorkloadEntity{externalWL("api"), externalWL("worker")}}
	for _, c := range []struct {
		name string
		e    *KCLEntities
	}{
		{"OnExternal + cluster", &KCLEntities{Workloads: []WorkloadEntity{externalWL("api"), clusterWL("db", "k3d-x", "ns")}}},
		{"OnExternal + hosted", &KCLEntities{ControlPlane: external.ControlPlane, Workloads: []WorkloadEntity{externalWL("api"), hostedWL("web")}}},
		{"no workloads", &KCLEntities{}},
	} {
		render = func() (*KCLEntities, error) { return c.e, nil }
		if err := rec(); !refused(err) {
			t.Fatalf("%s: err = %v, want the record_only_release refusal", c.name, err)
		}
	}
	render = func() (*KCLEntities, error) { return nil, errors.New("kcl: boom") }
	if err := rec(); !refused(err) || !strings.Contains(err.Error(), "kcl: boom") {
		t.Fatalf("an unrenderable declaration: err = %v, want the refusal naming the render error", err)
	}
	if len(cp.called("CutRelease")) != 0 {
		t.Fatal("a refused record cut a release")
	}

	render = func() (*KCLEntities, error) { return external, nil }
	if err := rec(); err != nil {
		t.Fatalf("an all-OnExternal env: %v", err)
	}
	if opened != external {
		t.Fatal("the record ledger was not opened with the env's declaration (its control plane and kind)")
	}
	if len(cp.called("Promote")) != 1 {
		t.Fatal("the record was not promoted into the env")
	}
}

// The default record ledger of a declared OnExternal env is the control plane
// the env declares, not Reliant cloud's default endpoint.
func TestDefaultRecordLedger_UsesTheDeclaredControlPlane(t *testing.T) {
	t.Setenv(ledgerSelectionEnv, "")
	declared := &KCLEntities{ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp.acme.example"},
		Workloads: []WorkloadEntity{externalWL("api")}}
	target, err := defaultRecordLedger(context.Background(), t.TempDir(), "shop", "prod", "rlat_test", declared)
	if err != nil {
		t.Fatal(err)
	}
	if target.endpoint != "https://cp.acme.example" || !target.ledger.Hosted {
		t.Fatalf("target = %+v, want the declared control plane", target)
	}
	if s, ok := target.ledger.Bindings.(*hostedStore); !ok || s.kind != deploytarget.HostedEnvSelfManaged {
		t.Fatalf("store = %#v, want a SELF_MANAGED hosted store", target.ledger.Bindings)
	}

	undeclared, err := defaultRecordLedger(context.Background(), t.TempDir(), "shop", "prod", "rlat_test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimRight(cloud.DefaultEndpoint, "/"); strings.TrimRight(undeclared.endpoint, "/") != want {
		t.Fatalf("undeclared endpoint = %q, want %q", undeclared.endpoint, want)
	}
}
