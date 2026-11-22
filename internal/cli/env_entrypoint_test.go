package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forge report 017a7441: `forge -C ../watches env shape prod` said prod was
// not found, while listing it among the declared envs: the entrypoint was
// stat'ed relative to the working directory, not the project.
func TestEnvEntrypointIsTheProjectsUnderARelativeProjectDir(t *testing.T) {
	root := t.TempDir()
	mainK := filepath.Join(root, "watches", "deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Dir(mainK), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainK, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root))
	if err := os.MkdirAll("elsewhere", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir("elsewhere")

	got, err := envRenderMainK("", "../watches", "prod")
	if err != nil {
		t.Fatalf("a declared env under -C ../watches must resolve: %v", err)
	}
	if want := filepath.Join("..", "watches", "deploy", "kcl", "prod", "main.k"); got != want {
		t.Errorf("entrypoint = %q, want %q (the project's, readable from here)", got, want)
	}
	_, err = envRenderMainK("", "../watches", "staging")
	// filepath.Join: the display path is OS-native (deploy\kcl\... on Windows).
	if err == nil || !strings.Contains(err.Error(), "neither "+filepath.Join("deploy", "kcl", "staging", "main.k")) {
		t.Errorf("an undeclared env names its entrypoint as declared, kclDir-relative: %v", err)
	}
}
