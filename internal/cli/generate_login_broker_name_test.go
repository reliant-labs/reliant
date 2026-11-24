package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The broker's own cookie names came from filepath.Base(projectDir): "." when
// generate runs from the project root ("._session"), and the branch name in a
// worktree. They follow forge.yaml's name, the one pkg/middleware's cookie is
// scaffolded from, whatever the directory is called.
func TestGenerateLoginBroker_NamesCookiesAfterForgeYAML(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "w3-some-branch-worktree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() {
		if err := generateLoginBroker("example.com/other", dir); err != nil {
			t.Errorf("generateLoginBroker: %v", err)
		}
	})
	b, err := os.ReadFile(filepath.Join(dir, "internal", "app", "login_broker.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{`const SessionCookieName = "shop_session"`, `CookiePrefix:     "shop",`} {
		if !strings.Contains(src, want) {
			t.Errorf("broker is missing %s", want)
		}
	}
}
