package kclvendor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKCL lays down deploy/kcl/<rel> under a fresh project root.
func writeKCL(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, "deploy", "kcl", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The forge.registry(default) helper is gone: the registry is a literal the
// env declares. MigrateRegistryHelper rewrites every call to its literal
// argument, so an existing project keeps rendering exactly what it rendered.
func TestMigrateRegistryHelper_RewritesEveryCallToItsLiteral(t *testing.T) {
	root := t.TempDir()
	prod := writeKCL(t, root, "prod/main.k", `import forge

# Overridable via -D registry=.
_registry = forge.registry("us-central1-docker.pkg.dev/acme/prod")
_cluster = forge.ClusterTarget {
    cluster = "gke"
    namespace = "acme-prod"
    registry = forge.registry( 'ghcr.io/acme' )
}
_other = forge.registry("a") + "/" + forge.registry("b")
`)
	lib := writeKCL(t, root, "lib/targets.k", `import forge
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev", registry = forge.registry("localhost:5050")}
`)
	untouched := writeKCL(t, root, "dev/main.k", `import forge
_registry = option("registry") or "localhost:5050"
# forge.registry is mentioned only in this comment
`)
	before, _ := os.ReadFile(untouched)

	migrated, err := MigrateRegistryHelper(root)
	if err != nil {
		t.Fatalf("MigrateRegistryHelper: %v", err)
	}
	if len(migrated) != 2 {
		t.Errorf("migrated = %v, want the two files that called forge.registry", migrated)
	}

	got, _ := os.ReadFile(prod)
	for _, want := range []string{
		`_registry = "us-central1-docker.pkg.dev/acme/prod"`,
		`registry = 'ghcr.io/acme'`,
		`_other = "a" + "/" + "b"`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("prod/main.k lacks %s after migration:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "forge.registry(") {
		t.Errorf("prod/main.k still calls forge.registry:\n%s", got)
	}
	if g, _ := os.ReadFile(lib); !strings.Contains(string(g), `registry = "localhost:5050"}`) {
		t.Errorf("lib/targets.k not migrated:\n%s", g)
	}
	// A project's own option("registry") is ordinary KCL, and a comment is
	// not a call: neither is touched.
	if after, _ := os.ReadFile(untouched); string(after) != string(before) {
		t.Errorf("dev/main.k changed but calls no forge.registry:\n%s", after)
	}

	// Idempotent.
	again, err := MigrateRegistryHelper(root)
	if err != nil || len(again) != 0 {
		t.Errorf("second MigrateRegistryHelper = (%v, %v), want nothing to do", again, err)
	}
}

// A call MigrateRegistryHelper cannot rewrite to a literal (a non-literal
// argument) is left in place, and CheckRegistryHelper still refuses it, naming
// the file, so the user is never told "migrated" about a file that will not
// render.
func TestMigrateRegistryHelper_NonLiteralArgumentIsReported(t *testing.T) {
	root := t.TempDir()
	writeKCL(t, root, "prod/main.k", `import forge
_default = "ghcr.io/acme"
_registry = forge.registry(_default)
`)
	if _, err := MigrateRegistryHelper(root); err != nil {
		t.Fatalf("MigrateRegistryHelper: %v", err)
	}
	err := CheckRegistryHelper(root)
	var stale *StaleRegistryHelperError
	if !errors.As(err, &stale) {
		t.Fatalf("CheckRegistryHelper = %v, want a StaleRegistryHelperError", err)
	}
	if !strings.Contains(err.Error(), "deploy/kcl/prod/main.k") || !strings.Contains(err.Error(), "_registry = \"<registry>\"") {
		t.Errorf("the refusal should name the file and the literal to write; got:\n%v", err)
	}
}

// CheckRegistryHelper is what every render calls: an unmigrated project fails
// with the one command that fixes it, instead of KCL's "attribute 'registry'
// not found in module 'forge'".
func TestCheckRegistryHelper_NamesTheFix(t *testing.T) {
	root := t.TempDir()
	writeKCL(t, root, "staging/main.k", `import forge
_registry = forge.registry("ghcr.io/acme")
`)
	err := CheckRegistryHelper(root)
	if err == nil {
		t.Fatal("CheckRegistryHelper on an unmigrated project: want an error, got nil")
	}
	for _, want := range []string{"deploy/kcl/staging/main.k", "forge.registry", "forge generate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should name %q; got:\n%v", want, err)
		}
	}
	if _, err := MigrateRegistryHelper(root); err != nil {
		t.Fatal(err)
	}
	if err := CheckRegistryHelper(root); err != nil {
		t.Errorf("CheckRegistryHelper after migration = %v, want nil", err)
	}
}
