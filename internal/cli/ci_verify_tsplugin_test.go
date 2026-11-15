package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// verify-generated must REFUSE to run when a frontend's TypeScript stubs
// cannot be regenerated, not certify the tree anyway.
//
// `forge generate` only warns and skips a frontend whose protoc-gen-es is
// missing. Under verify-generated that skip is a silent green: the committed
// stubs are never regenerated, so there is nothing to diff, and the gate
// reports "up to date" over half a tree. The scaffolded CI job did exactly
// that — it never ran `npm ci` (houndersclub PR #8).
func TestVerifyTSPluginsResolvable(t *testing.T) {
	t.Parallel()

	const bufGen = "version: v2\nplugins:\n  - local: ./frontends/web/node_modules/.bin/protoc-gen-es\n    out: frontends/web/src/gen\n"
	newProject := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		fe := filepath.Join(root, "frontends", "web")
		if err := os.MkdirAll(fe, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fe, "buf.gen.yaml"), []byte(bufGen), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	web := []config.FrontendConfig{{Name: "web", Type: "nextjs"}}

	t.Run("missing plugin refuses, naming the frontend", func(t *testing.T) {
		t.Parallel()
		err := verifyTSPluginsResolvable(newProject(t), web)
		if err == nil {
			t.Fatal("verify-generated would run with protoc-gen-es missing and silently skip the TypeScript stubs")
		}
		if !strings.Contains(err.Error(), "frontends/web") {
			t.Errorf("error does not name the frontend: %v", err)
		}
	})
	t.Run("frontend-local install passes", func(t *testing.T) {
		t.Parallel()
		root := newProject(t)
		writeTSPluginBin(t, filepath.Join(root, "frontends", "web"))
		if err := verifyTSPluginsResolvable(root, web); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("workspace-hoisted install passes", func(t *testing.T) {
		t.Parallel()
		root := newProject(t)
		writeTSPluginBin(t, root)
		if err := verifyTSPluginsResolvable(root, web); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a frontend type with no TypeScript codegen is not required", func(t *testing.T) {
		t.Parallel()
		if err := verifyTSPluginsResolvable(newProject(t), []config.FrontendConfig{{Name: "web", Type: "static"}}); err != nil {
			t.Fatal(err)
		}
	})
}
