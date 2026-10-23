package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// renderDeliveryPolicy renders a one-workload env with the given
// `delivery_policy = ...` expression ("" for none) through the same seam
// `forge env shape` uses, and returns the decoded entities.
func renderDeliveryPolicy(t *testing.T, policy string) *KCLEntities {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: renders KCL; runs in task test")
	}
	dir := t.TempDir()
	decl := ""
	if policy != "" {
		decl = "delivery_policy = " + policy
	}
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"deliverypolicy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k": `import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    ` + decl + `
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
`,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

// forge.DeliveryPolicy {} renders every control at the default Go records,
// field for field. The two halves hold the defaults separately (kcl/schema.k
// and release.DefaultDeliveryPolicy); this is what keeps them from drifting.
func TestDeliveryPolicyDefaultsAgreeWithKCL(t *testing.T) {
	e := renderDeliveryPolicy(t, "forge.DeliveryPolicy {}")
	if e.DeliveryPolicy == nil {
		t.Fatal("an env that declares a delivery_policy rendered none")
	}
	if want := release.DefaultDeliveryPolicy(); !reflect.DeepEqual(*e.DeliveryPolicy, want) {
		t.Errorf("KCL and Go disagree on the defaults:\n kcl %+v\n go  %+v", *e.DeliveryPolicy, want)
	}
	if got := shapeDeliveryPolicyOf(e); got != nil {
		t.Errorf("an all-default policy must not be recorded in the shape, got %+v", got)
	}
}

func TestDeliveryPolicyAbsentRendersNoPolicyAndRecordsNone(t *testing.T) {
	e := renderDeliveryPolicy(t, "")
	if e.DeliveryPolicy != nil {
		t.Errorf("an env with no delivery_policy rendered %+v", e.DeliveryPolicy)
	}
	if shapeDeliveryPolicyOf(e) != nil {
		t.Error("an env with no delivery_policy recorded one in its shape")
	}
}

// A hardened prod, end to end: KCL -> render contract -> shape field, sorted
// and complete.
func TestDeliveryPolicyReachesTheShape(t *testing.T) {
	e := renderDeliveryPolicy(t, `forge.DeliveryPolicy {
        trusted_builder = forge.TrustedBuilder {mode = "enforce", builders = ["github:acme/api", "cloudbuild:acme/delivery"]}
        admission_verify = forge.Control {mode = "enforce"}
        signed_promotions = forge.Control {mode = "enforce"}
        vuln_gate = forge.VulnPolicy {
            mode = "enforce"
            exemptions = [
                forge.VulnExemption {id = "GHSA-aaaa-bbbb-cccc", reason = "unreachable", expires = "2027-01-31"}
                forge.VulnExemption {id = "CVE-2026-0001", reason = "fixed next base image", expires = "2026-12-01"}
            ]
        }
        approvals = forge.Approvals {mode = "enforce"}
    }`)
	got := shapeDeliveryPolicyOf(e)
	if got == nil {
		t.Fatal("a non-default policy was not recorded")
	}
	want := release.DefaultDeliveryPolicy()
	want.TrustedBuilder = release.ShapeTrustedBuilder{Mode: release.DeliveryEnforce, Builders: []string{"cloudbuild:acme/delivery", "github:acme/api"}}
	want.AdmissionVerify.Mode = release.DeliveryEnforce
	want.SignedPromotions.Mode = release.DeliveryEnforce
	want.VulnGate.Mode = release.DeliveryEnforce
	want.VulnGate.Exemptions = []release.ShapeVulnExempt{
		{ID: "CVE-2026-0001", Reason: "fixed next base image", Expires: "2026-12-01"},
		{ID: "GHSA-aaaa-bbbb-cccc", Reason: "unreachable", Expires: "2027-01-31"},
	}
	want.Approvals.Mode = release.DeliveryEnforce
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("shape policy:\n got  %+v\n want %+v", *got, want)
	}
	if err := (release.Shape{Kind: release.EnvSelfManaged, DeliveryPolicy: got}).Canonical().Validate(); err != nil {
		t.Errorf("a policy KCL accepted must satisfy the shape's validation: %v", err)
	}
}

func TestShapeDeliveryPolicyOf(t *testing.T) {
	def := release.DefaultDeliveryPolicy()
	off := def
	off.VulnGate.Mode = release.DeliveryOff
	for name, tc := range map[string]struct {
		in   *KCLEntities
		want *release.ShapeDeliveryPolicy
	}{
		"no entities": {nil, nil},
		"no policy":   {&KCLEntities{}, nil},
		"defaults":    {&KCLEntities{DeliveryPolicy: &def}, nil},
		"one value":   {&KCLEntities{DeliveryPolicy: &off}, &off},
	} {
		if got := shapeDeliveryPolicyOf(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v want %+v", name, got, tc.want)
		}
	}
}

// renderDataAccess renders a one-workload env with the given `data_access` and
// `lifecycle` expressions ("" for none) through the seam `forge env shape` uses.
func renderDataAccess(t *testing.T, dataAccess, lifecycle string) *KCLEntities {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: renders KCL; runs in task test")
	}
	dir := t.TempDir()
	decl := ""
	if dataAccess != "" {
		decl += "data_access = " + dataAccess + "\n    "
	}
	if lifecycle != "" {
		decl += "lifecycle = \"" + lifecycle + "\"\n    "
	}
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"dataaccess\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k": `import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    ` + decl + `workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
`,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

// An env with no lifecycle (a real env) that declares nothing is audited, and
// that is Go's default too: the shape records nothing, and absence reads as
// audited. The two halves hold the default separately (kcl/schema.k and
// release.DefaultDataAccess); this keeps them from drifting.
func TestDataAccessDefaultsAgreeWithKCL(t *testing.T) {
	e := renderDataAccess(t, "", "")
	if want := release.DefaultDataAccess(); e.DataAccess.Resolved() != want {
		t.Errorf("KCL and Go disagree on the default: kcl %+v, go %+v", e.DataAccess.Resolved(), want)
	}
	if shapeDataAccessOf(e) != nil {
		t.Errorf("the default was recorded in the shape: %+v", shapeDataAccessOf(e))
	}
}

// A local or ephemeral env defaults to open, and records it: that is how the
// control plane and a reader tell it from a real env that declared nothing.
func TestDataAccessLifecycleDefaults(t *testing.T) {
	for _, tc := range []struct {
		lifecycle string
		want      release.DBQueryPolicy
	}{
		{"local", release.DBQueryOpen},
		{"ephemeral", release.DBQueryOpen},
		{"", release.DBQueryAudited},
	} {
		e := renderDataAccess(t, "", tc.lifecycle)
		if got := e.DataAccess.Resolved().DBQuery; got != tc.want {
			t.Errorf("lifecycle %q: data_access resolves to %s, want %s", tc.lifecycle, got, tc.want)
		}
		recorded := shapeDataAccessOf(e)
		if tc.want == release.DBQueryAudited && recorded != nil {
			t.Errorf("lifecycle %q: the default was recorded: %+v", tc.lifecycle, recorded)
		}
		if tc.want != release.DBQueryAudited && (recorded == nil || recorded.DBQuery != tc.want) {
			t.Errorf("lifecycle %q: shape records %+v, want %s", tc.lifecycle, recorded, tc.want)
		}
	}
}

// A declaration wins over the lifecycle's default, in both directions, and is
// recorded in the shape.
func TestDataAccessDeclarationOverridesTheLifecycle(t *testing.T) {
	for _, tc := range []struct {
		decl, lifecycle string
		want            release.DBQueryPolicy
	}{
		{`forge.DataAccess {db_query = "deny"}`, "", release.DBQueryDeny},
		{`forge.DataAccess {db_query = "deny"}`, "local", release.DBQueryDeny},
		{`forge.DataAccess {db_query = "open"}`, "", release.DBQueryOpen},
		{`forge.DataAccess {db_query = "audited"}`, "local", release.DBQueryAudited},
		{`forge.DataAccess {}`, "local", release.DBQueryOpen},
	} {
		e := renderDataAccess(t, tc.decl, tc.lifecycle)
		if got := e.DataAccess.Resolved().DBQuery; got != tc.want {
			t.Errorf("%s with lifecycle %q: data_access resolves to %s, want %s", tc.decl, tc.lifecycle, got, tc.want)
		}
	}
	e := renderDataAccess(t, `forge.DataAccess {db_query = "deny"}`, "")
	if got := shapeDataAccessOf(e); got == nil || got.DBQuery != release.DBQueryDeny {
		t.Errorf("a declared deny is not in the shape: %+v", got)
	}
}
