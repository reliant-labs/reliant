// Copyright (c) 2025 Reliant Labs
package daemonpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// viewerHome lays out a home directory with credential stores and a project,
// and returns it with the project root. Paths are resolved so a temp dir under
// a symlinked /tmp (macOS) compares as the daemon would see it.
func viewerHome(t *testing.T) (home, project string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		".ssh/id_rsa", ".aws/credentials", ".kube/config", ".git-credentials",
		".config/gh/hosts.yml", ".docker/config.json", ".netrc", ".npmrc",
		".config/google-chrome/Default/Cookies",
		".claude/.credentials.json", ".claude/settings.json",
		".config/app/settings.toml", "go/pkg/mod/example.com/m@v1/go.mod",
		"proj/README.md",
	} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, filepath.Join(home, "proj")
}

func TestDenyViewerRead(t *testing.T) {
	home, project := viewerHome(t)
	protected := viewerProtected(home, "")
	link := func(name, target string) string {
		p := filepath.Join(project, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sshLink := link("key", filepath.Join(home, ".ssh", "id_rsa"))
	awsDirLink := link("cloud", filepath.Join(home, ".aws"))

	for _, tc := range []struct {
		name, path, root string
		refused          bool
	}{
		{"ssh private key", filepath.Join(home, ".ssh", "id_rsa"), project, true},
		{"ssh directory itself", filepath.Join(home, ".ssh"), project, true},
		{"aws credentials", filepath.Join(home, ".aws", "credentials"), project, true},
		{"kube config", filepath.Join(home, ".kube", "config"), project, true},
		{"git credentials", filepath.Join(home, ".git-credentials"), project, true},
		{"gh login", filepath.Join(home, ".config", "gh", "hosts.yml"), project, true},
		{"docker login", filepath.Join(home, ".docker", "config.json"), project, true},
		{"netrc", filepath.Join(home, ".netrc"), project, true},
		{"browser profile", filepath.Join(home, ".config", "google-chrome", "Default", "Cookies"), project, true},
		{"claude login", filepath.Join(home, ".claude", ".credentials.json"), project, true},
		{"a missing file in a store", filepath.Join(home, ".ssh", "id_ed25519"), project, true},
		{"traversal into a store", filepath.Join(project, "..", ".ssh", "id_rsa"), project, true},
		{"symlink in the project to a key", sshLink, project, true},
		{"symlink in the project to a store directory", filepath.Join(awsDirLink, "credentials"), project, true},
		{"a project rooted at home does not exempt its stores", filepath.Join(home, ".ssh", "id_rsa"), home, true},

		{"a project file", filepath.Join(project, "README.md"), project, false},
		{"the module cache", filepath.Join(home, "go", "pkg", "mod", "example.com", "m@v1", "go.mod"), project, false},
		{"an ordinary config file", filepath.Join(home, ".config", "app", "settings.toml"), project, false},
		{"claude settings", filepath.Join(home, ".claude", "settings.json"), project, false},
		{"a system file", "/etc/hosts", project, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := denyViewerRead(tc.path, tc.root, protected)
			if tc.refused && !errors.Is(err, ErrViewerCredential) {
				t.Fatalf("denyViewerRead(%s) = %v, want ErrViewerCredential", tc.path, err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("denyViewerRead(%s) = %v, want nil", tc.path, err)
			}
		})
	}
}

// A dotfiles repo that ~/.ssh links into is a project whose files the viewer
// still shows: the store lives outside it, the file inside it.
func TestDenyViewerRead_DotfilesRepoKeepsItsFiles(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dotfiles := filepath.Join(home, "dotfiles")
	if err := os.MkdirAll(filepath.Join(dotfiles, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dotfiles, "ssh", "config")
	if err := os.WriteFile(cfg, []byte("Host *\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dotfiles, "ssh"), filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	protected := viewerProtected(home, "")

	if err := denyViewerRead(cfg, dotfiles, protected); err != nil {
		t.Errorf("a dotfiles file viewed in its own repo was refused: %v", err)
	}
	// From any other workspace it is the user's ~/.ssh.
	if err := denyViewerRead(filepath.Join(home, ".ssh", "config"), filepath.Join(home, "elsewhere"), protected); !errors.Is(err, ErrViewerCredential) {
		t.Errorf("~/.ssh/config viewed from another project = %v, want ErrViewerCredential", err)
	}
}
