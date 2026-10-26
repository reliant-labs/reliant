//go:build linux

package worktreereclaim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func procUseFixture(t *testing.T) (string, string) {
	t.Helper()
	proc := t.TempDir()
	base := filepath.Join(proc, "123")
	if err := os.MkdirAll(filepath.Join(base, "fd"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cwd", "exe"} {
		if err := os.Symlink("/unrelated", filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte("123 (name with ) parens) S 0 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "maps"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	return proc, base
}
func TestRecoveryProcessUseCwdFDExeMappedAndDeleted(t *testing.T) {
	for _, kind := range []string{"cwd", "exe", "fd", "maps", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			proc, base := procUseFixture(t)
			root := t.TempDir()
			path := filepath.Join(root, "file with space")
			switch kind {
			case "cwd", "exe":
				os.Remove(filepath.Join(base, kind))
				if err := os.Symlink(path, filepath.Join(base, kind)); err != nil {
					t.Fatal(err)
				}
			case "fd":
				if err := os.Symlink(path, filepath.Join(base, "fd", "4")); err != nil {
					t.Fatal(err)
				}
			case "maps", "deleted":
				if kind == "deleted" {
					path += " (deleted)"
				}
				data := fmt.Sprintf("1234-5678 rw-p 00000000 00:00 123 %s\n", path)
				if err := os.WriteFile(filepath.Join(base, "maps"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root}); err == nil {
				t.Fatal("active use must hold")
			}
		})
	}
}
func TestRecoveryProcessUseUnreadableAndUnsupportedStateHold(t *testing.T) {
	proc, base := procUseFixture(t)
	root := t.TempDir()
	if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(base, "maps"))
	if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root}); err == nil {
		t.Fatal("missing same-owner maps is not absence")
	}
	os.Remove(filepath.Join(base, "cwd"))
	if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root}); err == nil {
		t.Fatal("missing live cwd is not absence")
	}
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte("123 (zombie) Z 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root}); err != nil {
		t.Fatalf("verified zombie has no filesystem lifetime: %v", err)
	}
}
func TestRecoveryProcessUseBoundaryAndCancellation(t *testing.T) {
	root := "/workspace/recover"
	if processPathUses("/workspace/recover-other/file", []string{root}) {
		t.Fatal("prefix sibling is not use")
	}
	if !processPathUses(root+"/file (deleted)", []string{root}) {
		t.Fatal("deleted open file still in use")
	}
	proc, _ := procUseFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkProcessUseAt(ctx, proc, os.Geteuid(), []string{root}); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatal("cancellation must hold")
	}
}
