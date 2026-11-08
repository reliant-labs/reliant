package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func writeProjectFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestCRDSourceRoots pins how a project's CRD sources are derived. REGISTERING
// forge's tier kinds (a reference to their AddToScheme) installs their CRDs.
// Merely importing the package does not. That distinction is the one
// control-plane hit: its secret materializer imports the package for one
// constant, and treating the import as "install the CRDs" collided with
// control-plane's own same-named kinds.
func TestCRDSourceRoots(t *testing.T) {
	const imp = `"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"`
	cases := map[string]struct {
		files map[string]string
		want  []string
	}{
		"no operator at all": {map[string]string{"main.go": "package main\n"}, nil},
		"own api/ only":      {map[string]string{"api/v1/types.go": "package v1\n"}, []string{"./api/..."}},
		"imports for a constant only": {map[string]string{
			"internal/m/m.go": "package m\nimport forgev1 " + imp + "\nvar N = forgev1.ManagedSecretsSecretName\n",
		}, nil},
		"registers the scheme": {map[string]string{
			"internal/app/app.go": "package app\nimport " + imp + "\nvar F = v1alpha1.AddToScheme\n",
		}, []string{ForgeTierAPIPackage}},
		"registers via an alias, alongside own api/": {map[string]string{
			"api/v1/types.go":     "package v1\n",
			"internal/app/app.go": "package app\nimport tiers " + imp + "\nfunc init() { _ = tiers.AddToScheme }\n",
		}, []string{"./api/...", ForgeTierAPIPackage}},
		"a test file does not count": {map[string]string{
			"internal/app/app_test.go": "package app\nimport " + imp + "\nvar F = v1alpha1.AddToScheme\n",
		}, nil},
		"a different package's AddToScheme does not count": {map[string]string{
			"internal/app/app.go": "package app\nimport (\n\tforgev1 " + imp + "\n\tv1alpha1 \"example.com/other/v1alpha1\"\n)\nvar N = forgev1.ManagedSecretsSecretName\nvar F = v1alpha1.AddToScheme\n",
		}, nil},
		"vendored copies are ignored": {map[string]string{
			"vendor/x/x.go": "package x\nimport " + imp + "\nvar F = v1alpha1.AddToScheme\n",
		}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := CRDSourceRoots(writeProjectFiles(t, c.files))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CRDSourceRoots = %v, want %v", got, c.want)
			}
		})
	}
}

// TestCheckOneSourcePerKind: the same kind from two groups must fail loudly.
// Emitting both would give two same-named KCL lambdas, KCL keeps the last, and
// the other group's CRD (and every live CR of it) would be pruned from the
// cluster.
func TestCheckOneSourcePerKind(t *testing.T) {
	doc := func(group string) CRDDoc {
		return CRDDoc{Kind: "SimpleBackend", Lambda: CRDLambdaName("SimpleBackend"),
			CRD: apiext.CustomResourceDefinition{Spec: apiext.CustomResourceDefinitionSpec{Group: group}}}
	}
	if err := checkOneSourcePerKind([]CRDDoc{doc("forge.dev")}); err != nil {
		t.Fatal(err)
	}
	err := checkOneSourcePerKind([]CRDDoc{doc("reliant.dev"), doc("forge.dev")})
	if err == nil || !strings.Contains(err.Error(), "reliant.dev and forge.dev") || !strings.Contains(err.Error(), "delete its own SimpleBackend") {
		t.Fatalf("want a collision error naming both groups and the fix, got %v", err)
	}
}
