package flux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKCLNamesAgree pins the strings spelled in both languages. kcl/lib/flux.k
// DECLARES the ClusterRoles and the impersonated ServiceAccount; this package
// is what a Go writer binds them by. The KCL fixture asserts the same literals
// from its side.
func TestKCLNamesAgree(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "kcl", "lib", "flux.k"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for kcl, goVal := range map[string]string{
		"FLUX_CHART_OCI":              ChartOCI,
		"FLUX_CHART_VERSION":          ChartVersion,
		"FLUX_NAMESPACE":              Namespace,
		"FLUX_RECONCILER_ROLE":        ReconcilerRole,
		"FLUX_SOURCE_ROLE":            SourceRole,
		"FLUX_DEPLOY_SERVICE_ACCOUNT": DeployServiceAccount,
	} {
		want := kcl + ` = "` + goVal + `"`
		if !strings.Contains(text, want) {
			t.Errorf("kcl/lib/flux.k does not declare %s", want)
		}
	}
}
