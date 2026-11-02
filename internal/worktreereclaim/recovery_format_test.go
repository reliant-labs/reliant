// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	recoveryTestGit(t, dir, "init", "-q", "-b", "main", ".")
	recoveryTestWrite(t, dir, "file.txt", []byte("HEAD C\n"), 0644)
	recoveryTestGit(t, dir, "add", ".")
	recoveryTestGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}
func recoveryTestGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return b
}
func recoveryTestWrite(t *testing.T, dir, name string, b []byte, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, b, mode); e != nil {
		t.Fatal(e)
	}
}
func recoveryTestDescriptor() RecoveryCapture {
	return RecoveryCapture{CaptureID: "capture-test", WorktreeID: "worktree-test", RepoID: "repo-test", Rel: ".", RemoteURL: "https://github.com/example/repository.git", RecoveryRef: "refs/heads/reliant-recovery/worktree-test/capture-test/repo-test", CapturedAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}
}

// Regression: safe blob bytes do not make credential-bearing tree names
// safe. The historical case deletes the name before capture, requiring the
// complete reachable tree closure (not just current disk/index) to be scanned.
func TestRecoveryCredentialFilenameSourceAndHistory(t *testing.T) {
	heavyUnderShort(t, "credential filename regression uses real local Git plumbing")
	token := "ghp_" + strings.Repeat("x", 36)
	for _, where := range []string{"untracked", "index-only", "deleted-history", "history-directory"} {
		t.Run(where, func(t *testing.T) {
			root := recoveryTestRepo(t)
			name := token + ".txt"
			if where == "history-directory" {
				name = token + "/safe.txt"
			}
			recoveryTestWrite(t, root, name, []byte("ordinary source text\n"), 0644)
			if where != "untracked" {
				recoveryTestGit(t, root, "add", "--", name)
			}
			if where == "index-only" {
				if e := os.Remove(filepath.Join(root, name)); e != nil {
					t.Fatal(e)
				}
			}
			if where == "deleted-history" || where == "history-directory" {
				recoveryTestGit(t, root, "commit", "-qm", "safe message")
				tip := strings.TrimSpace(string(recoveryTestGit(t, root, "rev-parse", "HEAD")))
				if e := recoveryScanClosure(context.Background(), root, []string{tip}, ""); e == nil {
					t.Fatal("historical credential-bearing tree name passed publication gate")
				}
				recoveryTestGit(t, root, "rm", "--", name)
				recoveryTestGit(t, root, "commit", "-qm", "remove old file")
			}
			ctx := withGitObserver(context.Background(), func(args []string) {
				for _, arg := range args {
					if arg == "push" || arg == "fetch" || arg == "ls-remote" {
						t.Fatal("credential filename reached remote operation")
					}
				}
			})
			if _, e := CaptureRecovery(ctx, root, recoveryTestDescriptor()); e == nil {
				t.Fatal("credential-bearing filename capture accepted")
			}
			if len(recoveryTestGit(t, root, "for-each-ref", "refs/reliant/recovery-pins")) != 0 {
				t.Fatal("unsafe filename capture pinned")
			}
		})
	}
}

func TestRecoveryCaptureStagedDeletionRecreation(t *testing.T) {
	heavyUnderShort(t, "recovery capture uses real local Git plumbing")
	dir := recoveryTestRepo(t)
	recoveryTestGit(t, dir, "rm", "file.txt")
	recoveryTestWrite(t, dir, "file.txt", []byte("recreated untracked\n"), 0644)
	c, e := CaptureRecovery(context.Background(), dir, recoveryTestDescriptor())
	if e != nil {
		t.Fatal(e)
	}
	m, e := ReadRecoveryManifest(context.Background(), dir, c)
	if e != nil {
		t.Fatal(e)
	}
	if len(m.IndexEntries) != 0 || len(m.DiskEntries) != 1 || len(m.UntrackedPaths) != 1 {
		t.Fatalf("lost staged deletion/recreated membership: %+v", m)
	}
}

func TestRecoveryCaptureHeldBeforeEgress(t *testing.T) {
	heavyUnderShort(t, "recovery refusals use real local Git plumbing")
	cases := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"staged-only-sensitive", func(t *testing.T, d string) {
			recoveryTestWrite(t, d, "file.txt", []byte("API_KEY=staged-only-value\n"), 0644)
			recoveryTestGit(t, d, "add", "file.txt")
			recoveryTestWrite(t, d, "file.txt", []byte("safe disk\n"), 0644)
		}},
		{"deleted-history-sensitive", func(t *testing.T, d string) {
			recoveryTestWrite(t, d, "file.txt", []byte("password=old-value\n"), 0644)
			recoveryTestGit(t, d, "add", ".")
			recoveryTestGit(t, d, "commit", "-qm", "intermediate")
			recoveryTestWrite(t, d, "file.txt", []byte("safe now\n"), 0644)
			recoveryTestGit(t, d, "add", ".")
			recoveryTestGit(t, d, "commit", "-qm", "reverted")
		}},
		{"ignored-generated-sensitive", func(t *testing.T, d string) {
			recoveryTestWrite(t, d, ".gitignore", []byte(".env\n"), 0644)
			recoveryTestWrite(t, d, ".env", []byte("// Code generated DO NOT EDIT.\npassword=value\n"), 0644)
		}},
		{"binary-untracked", func(t *testing.T, d string) { recoveryTestWrite(t, d, "file.bin", []byte{1, 0, 2}, 0644) }},
		{"operation", func(t *testing.T, d string) { recoveryTestWrite(t, d, ".git/MERGE_HEAD", []byte("anything"), 0644) }},
		{"filter", func(t *testing.T, d string) { recoveryTestGit(t, d, "config", "filter.example.clean", "echo unsafe") }},
		{"sparse", func(t *testing.T, d string) { recoveryTestGit(t, d, "config", "core.sparseCheckout", "true") }},
		{"index-flag", func(t *testing.T, d string) { recoveryTestGit(t, d, "update-index", "--assume-unchanged", "file.txt") }},
		{"nested", func(t *testing.T, d string) {
			p := filepath.Join(d, "nested")
			if e := os.Mkdir(p, 0755); e != nil {
				t.Fatal(e)
			}
			recoveryTestGit(t, p, "init", "-q")
		}},
		{"unknown-ignored", func(t *testing.T, d string) {
			recoveryTestWrite(t, d, ".gitignore", []byte("notes/\n"), 0644)
			recoveryTestWrite(t, d, "notes/work.txt", []byte("unique work"), 0644)
		}},
		{"lfs", func(t *testing.T, d string) {
			recoveryTestWrite(t, d, "large", []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abcd\n"), 0644)
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := recoveryTestRepo(t)
			tt.setup(t, dir)
			calls := 0
			ctx := withGitObserver(context.Background(), func(args []string) {
				for _, a := range args {
					if a == "push" || a == "fetch" || a == "ls-remote" {
						calls++
					}
				}
			})
			_, e := CaptureRecovery(ctx, dir, recoveryTestDescriptor())
			if e == nil {
				t.Fatal("unsupported capture succeeded")
			}
			if calls != 0 {
				t.Fatalf("egress commands=%d", calls)
			}
			pins := recoveryTestGit(t, dir, "for-each-ref", "refs/reliant/recovery-pins")
			if len(pins) != 0 {
				t.Fatal("ref published/pinned despite refusal")
			}
		})
	}
}
