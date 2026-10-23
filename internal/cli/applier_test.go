package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// applier_test.go pins Bundle.applier = "forge": a real environment that forge
// applies by declaration. The platform's own env is the reason it exists: it
// binds clusters its own hub could converge, and a hub applying the env that
// runs the hub is circular.

// boundOnCluster is an env the way control-plane's prod is shaped: a control
// plane, a cluster workload, and the cluster bound to a connected cluster, which
// is every condition the unit split reads.
func boundOnCluster(applier string) *KCLEntities {
	return &KCLEntities{
		Applier:      applier,
		ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp.example"},
		ClusterTarget: &ClusterTargetEntity{
			Cluster: "gke_p_prod", Namespace: "prod", ConnectedCluster: "prod-control-plane",
		},
		Workloads: []WorkloadEntity{{
			Name: "api",
			Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{
				Cluster: "gke_p_prod", ConnectedCluster: "prod-control-plane",
			}},
		}},
	}
}

// Declared applier = "forge" exempts a real env from the (planned) refusal of
// direct apply. Without it the same env is the one false: a real env that
// declared nothing.
func TestDirectApplyAllowed_DeclaredApplier(t *testing.T) {
	if DirectApplyAllowed(boundOnCluster("")) {
		t.Fatal("control: an undeclared real env must NOT be direct-appliable, or the declaration proves nothing")
	}
	if !DirectApplyAllowed(boundOnCluster("forge")) {
		t.Error("an env that declares applier = forge was not exempted from the planned F-REFUSE-DIRECT")
	}
	// Only the one value is a licence, as with lifecycle.
	if DirectApplyAllowed(boundOnCluster("hub")) {
		t.Error("an unrecognised applier was treated as a licence")
	}
}

// THE LOAD-BEARING ONE. Binding a cluster says WHERE an env lands, not who puts
// it there. Without the override this env reads as hub-converged (bound
// clusters, nothing hosted), and the control plane would converge it as a
// second applier over objects forge applies, which for the platform's own env
// is the circle.
func TestUnitSplit_DeclaredApplierWins(t *testing.T) {
	inferred, err := unitSplitOf(boundOnCluster(""))
	if err != nil {
		t.Fatal(err)
	}
	if !inferred.AllHub() {
		t.Fatal("control: a fully bound env with nothing hosted must read as hub-converged, or this test pins nothing")
	}
	declared, err := unitSplitOf(boundOnCluster("forge"))
	if err != nil {
		t.Fatal(err)
	}
	if declared.AnyHub() {
		t.Errorf("applier = forge was overridden by the cluster bindings: the hub would become a second applier (%s)", declared.Describe())
	}
}

// The ledger decision follows: forge applies locally (ledger-only), the hub
// applies nothing.
func TestLedgerForEntities_DeclaredApplierIsLedgerOnly(t *testing.T) {
	e := boundOnCluster("forge")
	units, err := unitSplitOf(e)
	if err != nil {
		t.Fatal(err)
	}
	l := envLedger{Hosted: true, Units: units, Mixed: forgeAppliesLocally(e, units)}
	if l.hubUnits() {
		t.Error("the ledger would hand the env to the hub")
	}
	if !l.appliesLocally() {
		t.Error("forge does not apply the env locally")
	}
	if !l.ledgerOnly() {
		t.Error("a declared-applier env with no hosted tier should be ledger-only: the control plane records, forge applies")
	}
}

// The declaration is recorded: a reader tells "undeclared" from "forge applies
// it on purpose" without a render. Only a declaration is recorded.
func TestShapeApplier(t *testing.T) {
	if got := shapeApplierOf(boundOnCluster("forge")); got != release.ApplierForge {
		t.Errorf("shape applier = %q, want %q", got, release.ApplierForge)
	}
	if got := shapeApplierOf(boundOnCluster("")); got != "" {
		t.Errorf("an undeclared env recorded applier %q", got)
	}
	if shapeApplierOf(nil) != "" {
		t.Error("nil entities recorded an applier")
	}
}

// Shape round trip: recorded, decoded, validated; omitted when empty so every
// shape recorded before the field encodes (and hashes) as it did.
func TestShapeApplierRoundTrip(t *testing.T) {
	in := release.Shape{Kind: release.EnvSelfManaged, Applier: release.ApplierForge}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"applier":"forge"`) {
		t.Errorf("encoded shape lacks the applier: %s", b)
	}
	none, _ := release.Shape{Kind: release.EnvSelfManaged}.Encode()
	if strings.Contains(string(none), "applier") {
		t.Errorf("an undeclared applier must not be encoded: %s", none)
	}
	if err := (release.Shape{Kind: release.EnvSelfManaged, Applier: "hub"}).Validate(); err == nil {
		t.Error("an unknown applier validated")
	}
	if got := bundleShapeInputOf(envShapeDoc{Shape: in}).Applier; got != release.ApplierForge {
		t.Errorf("bundleShapeInputOf dropped the applier: %q", got)
	}
}

// The applier reaches the projected shape through the bundle package too, so a
// bundle a reconciler reads cannot disagree with the declaration.
func TestProjectShapeCarriesApplier(t *testing.T) {
	shape, err := bundle.ProjectShape(bundle.ShapeInput{Kind: release.EnvSelfManaged, Applier: release.ApplierForge})
	if err != nil {
		t.Fatal(err)
	}
	if shape.Applier != release.ApplierForge {
		t.Errorf("ProjectShape dropped the applier: %+v", shape)
	}
}

// The notice: an env declaring none of lifecycle, applier or hub_converges is
// told every way out, and that forge-applied is a stage ("until its units
// move"), not a place to stay; it must name `applier` and `hub_converges` or
// the notice sends people nowhere useful.
func TestLifecycleNoticeNamesEveryWayOut(t *testing.T) {
	for _, want := range []string{"lifecycle", `applier = "forge"`, "hub_converges", "until its units move", "F-REFUSE-DIRECT"} {
		if !strings.Contains(lifecycleNoticeLine, want) {
			t.Errorf("the notice omits %q: %s", want, lifecycleNoticeLine)
		}
	}
	if strings.ContainsAny(lifecycleNoticeLine, "\n\r") {
		t.Error("the notice spans lines")
	}
}

// Whole chain, no hand-written JSON: real KCL declares applier = "forge" on an
// env whose every cluster is connected, and the Go side must read it as an
// env forge applies, not one the hub converges.
func TestApplier_RealKCLToLedgerDecision(t *testing.T) {
	dir := t.TempDir()
	const kclMod = "[package]\nname = \"applier\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	main := func(applier string) string {
		return `import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    ` + applier + `
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
`
	}
	kclplugin.Register()
	render := func(applier string) *KCLEntities {
		t.Helper()
		for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": main(applier)} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, err := kclrender.Run(dir, dir, nil)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		e, err := parseKCLEntities(out)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}

	hubUnitsOf := func(e *KCLEntities) bool {
		split, err := unitSplitOf(e)
		if err != nil {
			t.Fatal(err)
		}
		return split.AnyHub()
	}
	undeclared := render("")
	if !hubUnitsOf(undeclared) || DirectApplyAllowed(undeclared) {
		t.Fatal("control: an undeclared bound env should be hub-converged and refuse direct apply")
	}
	declared := render(`applier = "forge"`)
	if declared.Applier != "forge" {
		t.Fatalf("the declaration was lost between KCL and the entity: %q", declared.Applier)
	}
	if hubUnitsOf(declared) {
		t.Error("a declared applier env was handed to the hub")
	}
	if !DirectApplyAllowed(declared) {
		t.Error("a declared applier env was not exempted from the planned refusal")
	}
	if got := shapeApplierOf(declared); got != release.ApplierForge {
		t.Errorf("the shape records %q", got)
	}
}
