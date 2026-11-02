package fluxdelivery_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// kclLib is the KCL source of the template, read by THIS process so the test
// cache keys on it.
func kclLib(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "kcl", "lib", "flux_delivery.k"))
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// TestTemplateMatchesKCL: template_gen.go is the KCL raw string, byte for
// byte. Fails when kcl/lib/flux_delivery.k changed and `go generate
// ./pkg/fluxdelivery` was not run. A raw KCL string is verbatim, so reading
// it textually is exact; internal/kcleval's TestTemplateMatchesKCLEvaluated
// proves that against a real evaluation.
func TestTemplateMatchesKCL(t *testing.T) {
	t.Parallel()
	src := kclLib(t)
	const open = `FLUX_DELIVERY_TEMPLATE = r"""`
	start := strings.Index(src, open)
	if start < 0 {
		t.Fatalf("kcl/lib/flux_delivery.k declares no %s", open)
	}
	body := src[start+len(open):]
	end := strings.Index(body, `"""`)
	if end < 0 {
		t.Fatal("FLUX_DELIVERY_TEMPLATE's raw string is not closed")
	}
	if got := body[:end]; got != fluxdelivery.Template {
		t.Errorf("fluxdelivery.Template is stale: run `go generate ./pkg/fluxdelivery`.\n--- kcl ---\n%s\n--- go ---\n%s", got, fluxdelivery.Template)
	}
}

// TestKCLConstantsAgree pins every string spelled in both languages.
func TestKCLConstantsAgree(t *testing.T) {
	t.Parallel()
	src := kclLib(t)
	for kcl, goVal := range map[string]string{
		"FLUX_DELIVERY_API_VERSION":      fluxdelivery.APIVersion,
		"FLUX_DELIVERY_SA":               fluxdelivery.ServiceAccount,
		"FLUX_DELIVERY_PRUNE_ANNOTATION": fluxdelivery.PruneAnnotation,
		"FLUX_DELIVERY_PRUNE_DISABLED":   fluxdelivery.PruneDisabled,
		"FLUX_DELIVERY_RETIRE_ORPHAN":    fluxdelivery.RetireOrphan,
		"FLUX_DELIVERY_RETIRE_TEARDOWN":  fluxdelivery.RetireTeardown,
		"FLUX_DELIVERY_INPUT_LABEL":      fluxdelivery.InputLabel,
	} {
		if want := kcl + ` = "` + goVal + `"`; !strings.Contains(src, want) {
			t.Errorf("kcl/lib/flux_delivery.k does not declare %s", want)
		}
	}
}

// TestTemplateLiteralsAgreeWithGo: the literals the template spells are the
// Go constants a reader of the objects compares against.
func TestTemplateLiteralsAgreeWithGo(t *testing.T) {
	t.Parallel()
	for _, lit := range []string{
		`"` + fluxdelivery.Schema + `"`,
		"apiVersion: " + fluxdelivery.SourceAPIVersion,
		"apiVersion: " + fluxdelivery.KustomizeAPIVersion,
		`mediaType: "` + release.BundleManifestsLayer + `"`,
		`"` + fluxdelivery.PruneAnnotation + `": "` + fluxdelivery.PruneDisabled + `"`,
		`"` + fluxdelivery.FenceAnnotation + `"`,
		`"` + fluxdelivery.ShardLabel + `"`,
		`"` + fluxdelivery.ShardConnected + `"`,
		`"` + fluxdelivery.ShardMain + `"`,
		`key: "` + fluxdelivery.KubeConfigKey + `"`,
		`eq $retire "` + fluxdelivery.RetireTeardown + `"`,
		`eq $retire "` + fluxdelivery.RetireOrphan + `"`,
		`eq $src.verify.mode "` + fluxdelivery.VerifyEnforce + `"`,
	} {
		if !strings.Contains(fluxdelivery.Template, lit) {
			t.Errorf("the template does not spell %s", lit)
		}
	}
	// No Secret, Namespace, RBAC or ConfigMap: only objects one binding
	// owns. And no slugify, which this package's renderer does not provide.
	for _, banned := range []string{"kind: Secret", "kind: Namespace", "kind: Role", "kind: ConfigMap", "copyFrom", "slugify"} {
		if strings.Contains(fluxdelivery.Template, banned) {
			t.Errorf("the template contains %q", banned)
		}
	}
}
