package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated config loader is pkg/config/config_gen.go.
//
// It was called config.go until the tier rename made the "_gen" suffix state
// that forge owns and rewrites the file (codegen.GenerateConfigLoader writes
// config_gen.go and retires the old name). This post-generation check kept
// looking for the pre-rename spelling, so it fired on EVERY healthy project:
//
//	⚠️  Post-generation warnings:
//	  • cmd/<bin>/cmd/serve.go imports pkg/config but pkg/config/config.go
//	    was not generated. Check your proto/config/ annotations.
//
// A warning that is always on is worse than no warning. It tells a user to
// go audit annotations that are fine, and it trains them to skip the one
// block that would matter when something IS wrong — which is exactly what
// happened while a real scaffold failure was being diagnosed.
func TestValidateGeneratedProject_GeneratedConfigLoaderStaysSilent(t *testing.T) {
	projectDir := writeConfigCheckFixture(t, "config_gen.go")

	for _, w := range validateGeneratedProject(projectDir, nil, nil, nil) {
		if strings.Contains(w, "pkg/config") {
			t.Errorf("pkg/config/config_gen.go exists, so the loader WAS generated; "+
				"validateGeneratedProject must not warn. got: %q", w)
		}
	}
}

// The negative control: with no generated loader on disk at all, the check
// must still fire — the rename fix must not have turned it off.
func TestValidateGeneratedProject_MissingConfigLoaderWarns(t *testing.T) {
	projectDir := writeConfigCheckFixture(t, "")

	var found string
	for _, w := range validateGeneratedProject(projectDir, nil, nil, nil) {
		if strings.Contains(w, "pkg/config") {
			found = w
		}
	}
	if found == "" {
		t.Fatal("serve.go imports pkg/config and no loader was generated; expected a warning")
	}
	if !strings.Contains(found, "config_gen.go") {
		t.Errorf("warning should name the file forge actually generates (config_gen.go); got: %q", found)
	}
}

// writeConfigCheckFixture lays out the minimum this check reads: a serve.go
// that imports pkg/config, plus optionally the generated loader named by
// loaderFile ("" writes none).
func writeConfigCheckFixture(t *testing.T, loaderFile string) string {
	t.Helper()
	projectDir := t.TempDir()

	// bootstrapBinaryName falls back to the project dir's base name, which
	// is what this fixture relies on.
	bin := filepath.Base(projectDir)
	serveDir := filepath.Join(projectDir, "cmd", bin, "cmd")
	if err := os.MkdirAll(serveDir, 0o755); err != nil {
		t.Fatalf("mkdir serve dir: %v", err)
	}
	serve := "package cmd\n\nimport (\n\t\"example.com/demo/pkg/config\"\n)\n\nvar _ = config.Load\n"
	if err := os.WriteFile(filepath.Join(serveDir, "serve.go"), []byte(serve), 0o644); err != nil {
		t.Fatalf("write serve.go: %v", err)
	}

	if loaderFile != "" {
		configDir := filepath.Join(projectDir, "pkg", "config")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, loaderFile), []byte("package config\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", loaderFile, err)
		}
	}
	return projectDir
}
