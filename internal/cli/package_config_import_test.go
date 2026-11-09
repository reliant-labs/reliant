package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// scaffoldPackagesIn runs `forge scaffold package` for the default shape and
// the client and eventbus kinds in a fresh project at dir, which the caller
// has already given (or not given) typed config.
func scaffoldPackagesIn(t *testing.T, dir string) map[string]string {
	t.Helper()
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("forge.yaml", []byte("name: testproject\nmodule_path: example.com/testproject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markServiceProject(t, dir)
	for name, kind := range map[string]string{"cache": "", "billingapi": "client", "events": "eventbus"} {
		cmd := newTestPackageNewCmd(kind)
		if err := cmd.RunE(cmd, []string{name}); err != nil {
			t.Fatalf("scaffold package %s (kind %q): %v", name, kind, err)
		}
	}
	files := map[string]string{}
	_ = filepath.WalkDir(filepath.Join(dir, "internal"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			files[rel] = string(b)
		}
		return nil
	})
	return files
}

// TestRunPackageNew_WithoutTypedConfig: in a project with no config protos
// and no pkg/config — reliant, adopting forge in place — a scaffolded
// package names no config, as service.go no longer does. It used to import
// <module>/pkg/config unconditionally, so `go mod tidy` failed and generate
// rolled back over a package that does not exist.
func TestRunPackageNew_WithoutTypedConfig(t *testing.T) {
	// Resolved before scaffoldPackagesIn changes the working directory.
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := scaffoldPackagesIn(t, dir)
	if len(files) == 0 {
		t.Fatal("nothing was scaffolded")
	}
	for rel, src := range files {
		if strings.Contains(src, "pkg/config") || strings.Contains(src, "config.Config") {
			t.Errorf("%s names a config the project does not have:\n%s", rel, src)
		}
	}

	if testing.Short() {
		return // the compile check below builds a module
	}
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	goVersion := "1.24"
	if data, rerr := os.ReadFile(filepath.Join(forgeRoot, "go.mod")); rerr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
				goVersion = strings.TrimSpace(v)
			}
		}
	}
	gomod := "module example.com/testproject\n\ngo " + goVersion + "\n\nrequire github.com/reliant-labs/forge v0.0.0\n\n" +
		"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	// go vet type-checks each package WITH its tests.
	vet := exec.Command("go", "vet", "./internal/...")
	vet.Dir = dir
	vet.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, verr := vet.CombinedOutput(); verr != nil {
		t.Fatalf("packages scaffolded into a project without typed config do not compile: %v\n%s", verr, out)
	}
}

// TestRunPackageNew_WithTypedConfig is the other half: a project whose
// pkg/config declares Config keeps the field and its import.
func TestRunPackageNew_WithTypedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "config", "config.go"), []byte("package config\n\ntype Config struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := scaffoldPackagesIn(t, dir)
	for _, rel := range []string{"internal/cache/service.go", "internal/billingapi/client.go", "internal/events/service.go"} {
		src := files[filepath.FromSlash(rel)]
		if !strings.Contains(src, `"example.com/testproject/pkg/config"`) || !strings.Contains(src, "Config *config.Config") {
			t.Errorf("%s must take the project's typed config:\n%s", rel, src)
		}
	}
}
