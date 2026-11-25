package frontenddeps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceInstallDirUsesYarnRootWithStalePnpmManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	member := filepath.Join(root, "repo", "web")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"package.json":        `{"private":true,"packageManager":"yarn@1.22.22","workspaces":["repo/*"]}`,
		"yarn.lock":           "# yarn lockfile v1\n",
		"pnpm-workspace.yaml": "packages: ['repo/*', '!repo/web']\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := WorkspaceInstallDir(member, root); got != root {
		t.Errorf("WorkspaceInstallDir = %q, want Yarn root %q", got, root)
	}
}

func TestWorkspaceInstallDirStopsAtUnrelatedBoundary(t *testing.T) {
	t.Parallel()
	common := t.TempDir()
	member := filepath.Join(common, "other-project", "web")
	boundary := filepath.Join(common, "this-project")
	if got := WorkspaceInstallDir(member, boundary); got != member {
		t.Fatalf("WorkspaceInstallDir = %q, want local %q", got, member)
	}
}

func TestWorkspaceInstallDirHonorsBoundariesAndMembership(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, content string
		boundary, memberFile    bool
		wantRoot                bool
	}{
		{"npm ancestor outside project", "package.json", `{"workspaces":["repo/web"]}`, true, false, false},
		{"npm member excluded", "package.json", `{"workspaces":["repo/*","!repo/web"]}`, false, false, false},
		{"pnpm member excluded", "pnpm-workspace.yaml", "packages: ['repo/*', '!repo/web']\n", false, false, false},
		{"pnpm local workspace", "pnpm-workspace.yaml", "packages: ['repo/*']\n", false, true, false},
		{"pnpm workspace member", "pnpm-workspace.yaml", "packages: ['repo/*']\n", false, false, true},
		{"pnpm bridge still routes through member", "pnpm-workspace.yaml", PnpmBridgeMarker + "\npackages: ['repo/*']\n", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			member := filepath.Join(root, "repo", "web")
			if err := os.MkdirAll(member, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, tc.manifest), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.memberFile {
				if err := os.WriteFile(filepath.Join(member, "pnpm-workspace.yaml"), []byte("allowBuilds: {}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			limit := root
			if tc.boundary {
				limit = filepath.Dir(member)
			}
			want := member
			if tc.wantRoot {
				want = root
			}
			if got := WorkspaceInstallDir(member, limit); got != want {
				t.Errorf("WorkspaceInstallDir = %q, want %q", got, want)
			}
		})
	}
}
