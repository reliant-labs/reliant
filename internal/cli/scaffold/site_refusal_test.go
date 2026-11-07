package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A static site has no Go module, so the scaffolds that write Go and proto
// refuse in it up front — naming the site, and the way to add an API —
// instead of failing half-way through their generate with proto/ left behind.
func TestRequireServiceKindRefusesASite(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"forge.yaml":     "name: landing\n",
		"package.json":   `{"name":"landing","devDependencies":{"vite":"^8"}}`,
		"vite.config.ts": "export default {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := requireServiceKind(root, "service")
	if err == nil {
		t.Fatal("scaffold service in a site must be refused")
	}
	for _, want := range []string{"static site", "no go.mod", "project new <name>-api --service", "deploy/static-site"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %v", want, err)
		}
	}

	// With a go.mod the root is a Go module, not a site: the refusal is no
	// longer the site's (whatever the project's own kind decides).
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/landing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireServiceKind(root, "service"); err != nil && strings.Contains(err.Error(), "static site") {
		t.Fatalf("a Go module must not be refused as a site: %v", err)
	}
}
