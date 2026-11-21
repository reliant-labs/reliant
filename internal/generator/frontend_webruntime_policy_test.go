package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPnpmBridgeRejectsPolicyWidening(t *testing.T) {
	for _, tc := range []struct{ name, a, b string }{
		{"deny allow", "allowBuilds: {esbuild: false}", "allowBuilds: {esbuild: true}"},
		{"absent allow", "allowBuilds: {}", "allowBuilds: {esbuild: true}"},
		{"scoped version", "allowBuilds: {'@scope/tool@1': false}", "allowBuilds: {'@scope/tool': true}"},
		{"overlapping versions", "allowBuilds: {'tool@^1': false}", "allowBuilds: {'tool@1.2.3': true}"},
		{"overrides", "overrides: {'parent>child': ^1.0.0}", "overrides: {'parent>child': ^2.0.0}"},
		{"overlapping overrides", "overrides: {'child@^1': 1.2.3}", "overrides: {'parent>child': 2.0.0}"},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: " reverse"}[reverse], func(t *testing.T) {
				root := t.TempDir()
				for name, body := range map[string]string{"a": tc.a, "z": tc.b} {
					writePnpmBridgeFrontend(t, root, name)
					if err := os.WriteFile(filepath.Join(root, "frontends", name, devPnpmWorkspaceName), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				members := []string{"frontends/a", "frontends/z"}
				if reverse {
					members[0], members[1] = members[1], members[0]
				}
				_, err := devPnpmWorkspaceManifest(root, members)
				if err == nil || !strings.Contains(err.Error(), "frontends/a") || !strings.Contains(err.Error(), "frontends/z") {
					t.Fatalf("want conflict naming both sources, got %v", err)
				}
				// A previously approved overlay must not survive the rejection.
				path := filepath.Join(root, devPnpmWorkspaceName)
				if err := os.WriteFile(path, []byte("# forge-dev-workspace-root: GITIGNORED, written by a DEV build of forge.\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := reconcilePnpmBridge(root, members); err == nil {
					t.Fatal("reconcile accepted conflict")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("stale policy still active: %v", err)
				}
			})
		}
	}
}

func TestPnpmBridgeRejectsRelativeOverrides(t *testing.T) {
	for _, spec := range []string{"link:../library", "file:./archive.tgz", "file:../library"} {
		t.Run(spec, func(t *testing.T) {
			root := t.TempDir()
			writePnpmBridgeFrontend(t, root, "web")
			path := filepath.Join(root, "frontends/web", devPnpmWorkspaceName)
			if err := os.WriteFile(path, []byte("overrides: {library: '"+spec+"'}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := devPnpmWorkspaceManifest(root, []string{"frontends/web"})
			if err == nil || !strings.Contains(err.Error(), "relative") || !strings.Contains(err.Error(), path) {
				t.Fatalf("want explicit relative override refusal: %v", err)
			}
		})
	}
}

func TestPnpmBridgeAcceptsIdenticalIndependentPolicies(t *testing.T) {
	root := t.TempDir()
	writePnpmBridgeFrontend(t, root, "a")
	writePnpmBridgeFrontend(t, root, "z")
	if _, err := devPnpmWorkspaceManifest(root, []string{"frontends/a", "frontends/z"}); err != nil {
		t.Fatal(err)
	}
}
