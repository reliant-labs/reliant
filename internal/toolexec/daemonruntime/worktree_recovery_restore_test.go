package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func TestRecoveryPublicationNeverOverwrites(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux no-overwrite publication")
	}
	root := t.TempDir()
	source := filepath.Join(root, "stage")
	target := filepath.Join(root, "existing")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "original"), []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if publishRecoveryDirectory(source, target) == nil {
		t.Fatal("overwrote existing destination")
	}
	if data, err := os.ReadFile(filepath.Join(target, "original")); err != nil || string(data) != "untouched" {
		t.Fatal("existing data changed")
	}
}

func TestRecoveryRestoreRejectsUnsafeWorkspaceMapping(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../outside", "/absolute", "x/../escape", ".git", "a\\b"} {
		req := RecoveryRestoreCommand{RecoveryRestoreRequest: worktreereclaim.RecoveryRestoreRequest{RecoveryID: "catalog", IdempotencyKey: "key", DestinationPath: filepath.Join(root, "new"), Branch: "separate", Captures: []worktreereclaim.RecoveryCapture{{RepoID: "repo", Rel: rel}}}, Repositories: map[string]string{"repo": root}, Token: "ephemeral"}
		got := restoreRecoveryWorkspace(context.Background(), req)
		if got.Outcome != worktreereclaim.OutcomeHeld {
			t.Fatalf("accepted unsafe rel %q: %+v", rel, got)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatal("unsafe mapping published destination")
	}
}
