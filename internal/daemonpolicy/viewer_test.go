// Copyright (c) 2025 Reliant Labs
package daemonpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	protected := viewerProtected(home, "", noEnv)
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
	protected := viewerProtected(home, "", noEnv)

	if err := denyViewerRead(cfg, dotfiles, protected); err != nil {
		t.Errorf("a dotfiles file viewed in its own repo was refused: %v", err)
	}
	// From any other workspace it is the user's ~/.ssh.
	if err := denyViewerRead(filepath.Join(home, ".ssh", "config"), filepath.Join(home, "elsewhere"), protected); !errors.Is(err, ErrViewerCredential) {
		t.Errorf("~/.ssh/config viewed from another project = %v, want ErrViewerCredential", err)
	}
}

// A hard link is the file it links to wherever it sits, so a project hard
// link to a key in ~/.ssh (a directory store) or to ~/.netrc (a file store)
// is refused, even though its path is in the workspace.
func TestDenyViewerRead_HardLinksIntoStores(t *testing.T) {
	home, project := viewerHome(t)
	protected := viewerProtected(home, "", noEnv)
	for name, target := range map[string]string{
		"ssh-key": filepath.Join(home, ".ssh", "id_rsa"),
		"netrc":   filepath.Join(home, ".netrc"),
		"cookies": filepath.Join(home, ".config", "google-chrome", "Default", "Cookies"),
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(project, name)
			if err := os.Link(target, p); err != nil {
				t.Fatalf("os.Link: %v", err)
			}
			if err := denyViewerRead(p, project, protected); !errors.Is(err, ErrViewerCredential) {
				t.Errorf("denyViewerRead(hard link to %s) = %v, want ErrViewerCredential", target, err)
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := checkViewerFile(f, p, project, protected); !errors.Is(err, ErrViewerCredential) {
				t.Errorf("checkViewerFile(hard link to %s) = %v, want ErrViewerCredential", target, err)
			}
		})
	}
	// A project file with a second name of its own is still a project file.
	readme := filepath.Join(project, "README.md")
	if err := os.Link(readme, filepath.Join(project, "README.copy")); err != nil {
		t.Fatalf("os.Link: %v", err)
	}
	if err := denyViewerRead(readme, project, protected); err != nil {
		t.Errorf("a hard-linked project file was refused: %v", err)
	}
}

// The viewer checks the file it opened: when the path names another file by
// the time it is checked (a symlink swapped after the open), the open file is
// refused, whichever way the swap went.
func TestCheckViewerFile_RefusesAPathThatChanged(t *testing.T) {
	home, project := viewerHome(t)
	protected := viewerProtected(home, "", noEnv)
	key := filepath.Join(home, ".ssh", "id_rsa")
	readme := filepath.Join(project, "README.md")
	link := filepath.Join(project, "link")
	point := func(target string) {
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	// Opened the key, then pointed at README.md for the check.
	point(key)
	f, err := os.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	point(readme)
	if err := checkViewerFile(f, link, project, protected); !errors.Is(err, ErrViewerCredential) {
		t.Errorf("an open key checked as README.md = %v, want ErrViewerCredential", err)
	}
	_ = f.Close()

	// Unchanged, a project file passes.
	f, err = os.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := checkViewerFile(f, link, project, protected); err != nil {
		t.Errorf("an unchanged project file was refused: %v", err)
	}
}

// A key that ~/.ssh reaches through a symlink (to the file, or to a directory
// holding it) is in the store: a project hard link to it is refused, and so is
// a project symlink to it, though neither names a path under ~/.ssh. A cycle
// of symlinks in the store does not stop the walk.
func TestDenyViewerRead_StoreReachesKeysThroughSymlinks(t *testing.T) {
	home, project := viewerHome(t)
	protected := viewerProtected(home, "", noEnv)
	keys := filepath.Join(home, "keys")
	for _, f := range []string{"key", "dir/key", "single"} {
		p := filepath.Join(keys, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ssh := filepath.Join(home, ".ssh")
	// id_rsa is a regular file in viewerHome; here it is a symlink.
	if err := os.Remove(filepath.Join(ssh, "id_rsa")); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"id_rsa":   filepath.Join(keys, "key"),
		"keydir":   filepath.Join(keys, "dir"),
		"id_ecdsa": filepath.Join(keys, "single"),
		"loop":     ssh,
	} {
		if err := os.Symlink(target, filepath.Join(ssh, name)); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"login.txt": filepath.Join(keys, "key"),
		"deploy":    filepath.Join(keys, "dir", "key"),
	} {
		t.Run("hard link "+name, func(t *testing.T) {
			p := filepath.Join(project, name)
			if err := os.Link(target, p); err != nil {
				t.Fatalf("os.Link: %v", err)
			}
			if err := denyViewerRead(p, project, protected); !errors.Is(err, ErrViewerCredential) {
				t.Errorf("denyViewerRead(project hard link to %s) = %v, want ErrViewerCredential", target, err)
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := checkViewerFile(f, p, project, protected); !errors.Is(err, ErrViewerCredential) {
				t.Errorf("checkViewerFile(project hard link to %s) = %v, want ErrViewerCredential", target, err)
			}
		})
	}
	t.Run("the link target by its own path", func(t *testing.T) {
		if err := denyViewerRead(filepath.Join(keys, "single"), project, protected); !errors.Is(err, ErrViewerCredential) {
			t.Errorf("denyViewerRead(target of ~/.ssh/id_ecdsa) = %v, want ErrViewerCredential", err)
		}
	})
	t.Run("a project symlink to the link target", func(t *testing.T) {
		p := filepath.Join(project, "single")
		if err := os.Symlink(filepath.Join(keys, "single"), p); err != nil {
			t.Fatal(err)
		}
		if err := denyViewerRead(p, project, protected); !errors.Is(err, ErrViewerCredential) {
			t.Errorf("denyViewerRead(project symlink to the target of ~/.ssh/id_ecdsa) = %v, want ErrViewerCredential", err)
		}
	})
	t.Run("a project file is still served", func(t *testing.T) {
		if err := denyViewerRead(filepath.Join(project, "README.md"), project, protected); err != nil {
			t.Errorf("a project file was refused: %v", err)
		}
	})
}

// foldStat is os.Stat on a case-insensitive volume under root: each path
// component below root matches a directory entry whatever its case, as on
// macOS's default APFS. Symlink resolution still keeps the spelling it is
// given, which is what makes string prefixes miss.
func foldStat(root string) func(string) (os.FileInfo, error) {
	return func(p string) (os.FileInfo, error) {
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return os.Stat(p)
		}
		cur := root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			next := filepath.Join(cur, part)
			if _, err := os.Lstat(next); err != nil {
				entries, rerr := os.ReadDir(cur)
				if rerr != nil {
					return nil, err
				}
				for _, e := range entries {
					if strings.EqualFold(e.Name(), part) {
						next = filepath.Join(cur, e.Name())
						break
					}
				}
			}
			cur = next
		}
		return os.Stat(cur)
	}
}

// On a case-insensitive volume ~/.SSH/id_rsa opens ~/.ssh/id_rsa. The guard
// recognises the store by identity, not spelling.
func TestDenyViewerRead_CaseInsensitiveVolume(t *testing.T) {
	home, project := viewerHome(t)
	protected := viewerProtected(home, "", noEnv)
	saved := viewerStat
	viewerStat = foldStat(home)
	t.Cleanup(func() { viewerStat = saved })

	for _, rel := range []string{
		".SSH/id_rsa", ".Ssh/ID_RSA", ".SSH/id_ed25519", ".SSH",
		".AWS/credentials", ".Config/GH/hosts.yml", ".NETRC",
		".Config/Google-Chrome/Default/Cookies", "PROJ/../.SSH/id_rsa",
	} {
		p := filepath.Join(home, rel)
		if err := denyViewerRead(p, project, protected); !errors.Is(err, ErrViewerCredential) {
			t.Errorf("denyViewerRead(%s) = %v, want ErrViewerCredential", p, err)
		}
	}
	for _, rel := range []string{"PROJ/README.md", ".Config/App/settings.toml", ".Claude/settings.json"} {
		p := filepath.Join(home, rel)
		if err := denyViewerRead(p, project, protected); err != nil {
			t.Errorf("denyViewerRead(%s) = %v, want nil", p, err)
		}
	}
}

// The same on a real case-insensitive volume, where the host has one (macOS,
// Windows): the open-file check too, which compares the file with itself.
func TestCheckViewerFile_CaseInsensitiveVolume(t *testing.T) {
	home, project := viewerHome(t)
	upper := filepath.Join(home, ".SSH", "id_rsa")
	f, err := os.Open(upper)
	if err != nil {
		t.Skipf("needs a case-insensitive volume; %s is case-sensitive (%v)", home, err)
	}
	defer f.Close()
	protected := viewerProtected(home, "", noEnv)
	if err := denyViewerRead(upper, project, protected); !errors.Is(err, ErrViewerCredential) {
		t.Errorf("denyViewerRead(%s) = %v, want ErrViewerCredential", upper, err)
	}
	if err := checkViewerFile(f, upper, project, protected); !errors.Is(err, ErrViewerCredential) {
		t.Errorf("checkViewerFile(%s) = %v, want ErrViewerCredential", upper, err)
	}
}

// noEnv is an environment with nothing set.
func noEnv(string) string { return "" }

// A credential store the environment relocates (XDG_CONFIG_HOME, GH_CONFIG_DIR,
// …) is refused at its configured location, by path, through a project
// symlink and through a project hard link, while the default location stays
// protected and ordinary configuration beside the store is still served.
func TestDenyViewerRead_RelocatedStores(t *testing.T) {
	home, project := viewerHome(t)
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"config/gh/hosts.yml", "config/app/settings.toml", "config/google-chrome/Default/Cookies",
		"share/keyrings/login.keyring", "ghdir/hosts.yml", "docker/config.json", "docker/features.json",
		"claude/.credentials.json", "claude/settings.json", "kube/a", "kube/b",
	} {
		p := filepath.Join(data, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{
		"XDG_CONFIG_HOME":   filepath.Join(data, "config"),
		"XDG_DATA_HOME":     filepath.Join(data, "share"),
		"GH_CONFIG_DIR":     filepath.Join(data, "ghdir"),
		"DOCKER_CONFIG":     filepath.Join(data, "docker"),
		"CLAUDE_CONFIG_DIR": filepath.Join(data, "claude"),
		"KUBECONFIG":        filepath.Join(data, "kube", "a") + string(filepath.ListSeparator) + filepath.Join(data, "kube", "b"),
	}
	protected := viewerProtected(home, "", func(k string) string { return env[k] })

	refused := []string{
		filepath.Join(data, "config", "gh", "hosts.yml"),
		filepath.Join(data, "config", "google-chrome", "Default", "Cookies"),
		filepath.Join(data, "share", "keyrings", "login.keyring"),
		filepath.Join(data, "ghdir", "hosts.yml"),
		filepath.Join(data, "docker", "config.json"),
		filepath.Join(data, "claude", ".credentials.json"),
		filepath.Join(data, "kube", "a"),
		filepath.Join(data, "kube", "b"),
		// The defaults are still protected.
		filepath.Join(home, ".config", "gh", "hosts.yml"),
		filepath.Join(home, ".ssh", "id_rsa"),
	}
	for _, p := range refused {
		if err := denyViewerRead(p, project, protected); !errors.Is(err, ErrViewerCredential) {
			t.Errorf("denyViewerRead(%s) = %v, want ErrViewerCredential", p, err)
		}
	}
	for name, target := range map[string]string{
		"xdg-gh": filepath.Join(data, "config", "gh", "hosts.yml"),
		"gh-dir": filepath.Join(data, "ghdir", "hosts.yml"),
	} {
		t.Run("symlink "+name, func(t *testing.T) {
			p := filepath.Join(project, "sym-"+name)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			if err := denyViewerRead(p, project, protected); !errors.Is(err, ErrViewerCredential) {
				t.Errorf("denyViewerRead(project symlink to %s) = %v, want ErrViewerCredential", target, err)
			}
		})
		t.Run("hard link "+name, func(t *testing.T) {
			p := filepath.Join(project, "hard-"+name)
			if err := os.Link(target, p); err != nil {
				t.Fatalf("os.Link: %v", err)
			}
			if err := denyViewerRead(p, project, protected); !errors.Is(err, ErrViewerCredential) {
				t.Errorf("denyViewerRead(project hard link to %s) = %v, want ErrViewerCredential", target, err)
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := checkViewerFile(f, p, project, protected); !errors.Is(err, ErrViewerCredential) {
				t.Errorf("checkViewerFile(project hard link to %s) = %v, want ErrViewerCredential", target, err)
			}
		})
	}
	for _, p := range []string{
		filepath.Join(data, "config", "app", "settings.toml"),
		filepath.Join(data, "docker", "features.json"),
		filepath.Join(data, "claude", "settings.json"),
		filepath.Join(project, "README.md"),
	} {
		if err := denyViewerRead(p, project, protected); err != nil {
			t.Errorf("denyViewerRead(%s) = %v, want nil", p, err)
		}
	}
}

// DenyViewerRead reads the relocating variables from the daemon's environment.
func TestDenyViewerRead_ReadsTheEnvironment(t *testing.T) {
	home, project := viewerHome(t)
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, "elsewhere")
	hosts := filepath.Join(cfg, "gh", "hosts.yml")
	if err := os.MkdirAll(filepath.Dir(hosts), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hosts, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := DenyViewerRead(hosts, project); !errors.Is(err, ErrViewerCredential) {
		t.Errorf("DenyViewerRead(%s) with XDG_CONFIG_HOME=%s = %v, want ErrViewerCredential", hosts, cfg, err)
	}
}
