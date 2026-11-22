package kcleval_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclvendor"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/fluxdelivery/internal/kcleval"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The Go/KCL agreement tests: what a Go writer (the control-plane hub) and a
// KCL render (a self-hosted bootstrap, `forge env render` review) produce for
// the same binding must be the same object. Each evaluates the forge KCL
// module embedded in this test binary, so a change to kcl/ is a recompile and
// Go's test cache cannot replay a stale pass.

func slow(t *testing.T) {
	t.Helper()
	testkit.Slow(t, "evaluates the forge KCL module through kclrender",
		testkit.Inputs("kcl/lib/flux_delivery.k", "kcl/render.k", "pkg/fluxdelivery/**"))
}

// TestTemplateMatchesKCLEvaluated: the generated constant is the EVALUATED
// KCL string — which also proves the fast TestTemplateMatchesKCL's textual
// read of the raw string is exact.
func TestTemplateMatchesKCLEvaluated(t *testing.T) {
	slow(t)
	got, err := kcleval.Template(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != fluxdelivery.Template {
		t.Errorf("fluxdelivery.Template is not the evaluated FLUX_DELIVERY_TEMPLATE; run `go generate ./pkg/fluxdelivery`.\n--- kcl ---\n%s\n--- go ---\n%s", got, fluxdelivery.Template)
	}
}

// TestResourceSetMatchesKCL: fluxdelivery.ResourceSet and kcl's
// flux_delivery_resourceset build the same ResourceSet for one binding.
func TestResourceSetMatchesKCL(t *testing.T) {
	slow(t)
	in := fluxdelivery.BindingInput{
		Schema: fluxdelivery.Schema, Org: "acme", Env: "env-prod", Namespace: "flux-acme",
		Fence: "7", Promotion: "promo-7",
		Labels: map[string]string{"reliant.dev/org": "acme"},
		Source: fluxdelivery.Source{
			Name: fluxdelivery.SourceName("acme", "env-prod", ""), URL: "oci://registry.example.com/acme/bundle.v1/prod",
			Digest: "sha256:" + strings.Repeat("2", 64), Interval: "10m0s",
			Verify: fluxdelivery.Verify{Mode: fluxdelivery.VerifyOff},
		},
		Appliers: []fluxdelivery.Applier{{
			Name:   fluxdelivery.KustomizationName("acme", "env-prod", "", "vke"),
			Source: fluxdelivery.SourceName("acme", "env-prod", ""), Cluster: "vke", Path: release.BundleClusterPath("vke"),
			KubeConfigSecret: "vke-kubeconfig", Wait: true, Timeout: "5m0s",
		}},
	}
	if err := fluxdelivery.Validate(in); err != nil {
		t.Fatal(err)
	}
	want, err := fluxdelivery.ResourceSet(in)
	if err != nil {
		t.Fatal(err)
	}
	m, err := in.Map()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	main := "import forge\nimport json\n\n" +
		`rs = forge.flux_delivery_resourceset("` + fluxdelivery.ResourceSetName(in.Env, in.Binding) + `", "` + in.Namespace +
		`", inputs = [json.decode(r"""` + string(raw) + `""")])` + "\n"
	var out struct {
		RS map[string]any `json:"rs"`
	}
	if err := kcleval.Eval(t.TempDir(), main, &out); err != nil {
		t.Fatal(err)
	}
	if norm := viaJSON(t, want); !reflect.DeepEqual(out.RS, norm) {
		g, _ := json.MarshalIndent(out.RS, "", "  ")
		w, _ := json.MarshalIndent(norm, "", "  ")
		t.Errorf("kcl and Go disagree on the ResourceSet:\n--- kcl ---\n%s\n--- go ---\n%s", g, w)
	}
}

// TestRBACMatchesKCL: the Role kcl's flux_delivery_rbac declares grants
// exactly fluxdelivery.Rules, the rules the hub writes from Go.
func TestRBACMatchesKCL(t *testing.T) {
	slow(t)
	var out struct {
		RBAC []map[string]any `json:"rbac"`
	}
	if err := kcleval.Eval(t.TempDir(), "import forge\n\nrbac = forge.flux_delivery_rbac(\"flux-acme\")\n", &out); err != nil {
		t.Fatal(err)
	}
	var role map[string]any
	for _, o := range out.RBAC {
		if o["kind"] == "Role" {
			role = o
		}
	}
	if role == nil {
		t.Fatalf("flux_delivery_rbac declares no Role: %v", out.RBAC)
	}
	if want := viaJSON(t, map[string]any{"rules": fluxdelivery.Rules()})["rules"]; !reflect.DeepEqual(role["rules"], want) {
		t.Errorf("kcl Role rules %v, Go Rules %v", role["rules"], want)
	}
}

func viaJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMain keeps the embedded module's cache out of the developer's real
// cache directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fluxdelivery-kclcache-")
	if err != nil {
		panic(err)
	}
	restore := kclvendor.SetCacheDirForTest(filepath.Join(dir, "kcl"))
	code := m.Run()
	restore()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
