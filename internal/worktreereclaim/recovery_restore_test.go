package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryRestoreExactIndexAndRawDisk(t *testing.T) {
	heavyUnderShort(t, "recovery restore uses real local Git plumbing")
	source := recoveryTestRepo(t)
	ctx := context.Background()
	recoveryTestWrite(t, source, "file.txt", []byte("stage A\n"), 0644)
	recoveryTestGit(t, source, "add", "file.txt")
	recoveryTestWrite(t, source, "file.txt", []byte("disk B\r\n"), 0644)
	recoveryTestWrite(t, source, "new\nfile-\xff", []byte("untracked\n"), 0755)
	if err := os.Symlink("/outside/not-followed", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	c, err := CaptureRecovery(ctx, source, recoveryTestDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "fresh.git")
	recoveryTestGit(t, source, "init", "--bare", bare)
	recoveryTestGit(t, bare, "fetch", source, c.CommitOID)
	dest := filepath.Join(t.TempDir(), "recovered")
	if err := RestoreRecoveryCheckout(ctx, bare, dest, "new-recovery", c); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRestoredRecovery(ctx, dest, "new-recovery", c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "file.txt"))
	if err != nil || string(data) != "disk B\r\n" {
		t.Fatalf("raw disk: %q %v", data, err)
	}
	if got := strings.TrimSpace(string(recoveryTestGit(t, dest, "show", ":file.txt"))); got != "stage A" {
		t.Fatalf("index %q", got)
	}
	if got := strings.TrimSpace(string(recoveryTestGit(t, dest, "rev-parse", "HEAD"))); got != c.HeadOID {
		t.Fatal("HEAD differs")
	}
	if got := strings.TrimSpace(string(recoveryTestGit(t, source, "symbolic-ref", "HEAD"))); got != "refs/heads/main" {
		t.Fatal("source branch changed")
	}
	recoveryTestWrite(t, dest, "file.txt", []byte("modified"), 0644)
	if ValidateRestoredRecovery(ctx, dest, "new-recovery", c) == nil {
		t.Fatal("retry ignored raw mutation")
	}
	if err := RestoreRecoveryCheckout(ctx, bare, dest, "new-recovery", c); err == nil {
		t.Fatal("overwrote existing destination")
	}
}

func TestRecoveryRestoreLinkedAndStagedDeletion(t *testing.T) {
	heavyUnderShort(t, "linked recovery restore uses local Git subprocesses")
	source := recoveryTestRepo(t)
	ctx := context.Background()
	recoveryTestGit(t, source, "rm", "file.txt")
	recoveryTestWrite(t, source, "file.txt", []byte("recreated untracked\n"), 0644)
	c, err := CaptureRecovery(ctx, source, recoveryTestDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "linked")
	if err := RestoreRecoveryLinkedCheckout(ctx, source, source, dest, "restored-unique", c); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRestoredRecovery(ctx, dest, "restored-unique", c); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(recoveryTestGit(t, dest, "ls-files"))); got != "" {
		t.Fatalf("staged deletion lost: %q", got)
	}
	if got := strings.TrimSpace(string(recoveryTestGit(t, dest, "ls-files", "--others", "--exclude-standard"))); got != "file.txt" {
		t.Fatalf("untracked recreation lost: %q", got)
	}
	another := filepath.Join(t.TempDir(), "collision")
	if RestoreRecoveryLinkedCheckout(ctx, source, source, another, "main", c) == nil {
		t.Fatal("existing source branch overwritten")
	}
	if _, err := os.Stat(another); !os.IsNotExist(err) {
		t.Fatal("branch collision left destination")
	}
}

func TestRecoveryRestoreRejectsDestinationConfigPoison(t *testing.T) {
	heavyUnderShort(t, "destination config validation uses local Git subprocesses")
	for _, key := range []string{"core.worktree", "core.hooksPath", "core.fsmonitor", "filter.pwn.smudge", "include.path", "extensions.worktreeConfig", "core.sshCommand"} {
		t.Run(key, func(t *testing.T) {
			dir := recoveryTestRepo(t)
			recoveryTestGit(t, dir, "config", key, "/tmp/must-not-execute")
			if validateRecoveryDestinationRepo(context.Background(), dir) == nil {
				t.Fatalf("accepted poisoned config %s", key)
			}
		})
	}
}

func TestRecoveryRestoreUnsafePaths(t *testing.T) {
	for _, p := range []string{"../escape", "/absolute", "a/../b", ".git/config", "a/.GIT/x", "a\\b", "x:y", "a/", "a//b", "a. ", ""} {
		if validateRecoveryRelative(p) == nil {
			t.Errorf("accepted unsafe path %q", p)
		}
	}
	for _, p := range []string{"a\nfile", "a/b", "nonutf8-\xff"} {
		if err := validateRecoveryRelative(p); err != nil {
			t.Errorf("rejected valid %q", p)
		}
	}
}
