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

// A process inside exit has dropped its mm (exe) before it is a zombie, so its
// links read ENOENT while it is still listed. That is proof only for a
// single-threaded task flagged PF_EXITING; anything short of it holds.
func TestRecoveryProcessUseExitingProcessIsNotUse(t *testing.T) {
	for _, tc := range []struct {
		name, flags, threads string
		holds                bool
	}{
		{"exiting single thread", "4194564", "1", false},
		{"live, no PF_EXITING", "4194560", "1", true},
		{"exiting leader of a live group", "4194564", "3", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc, base := procUseFixture(t)
			root := t.TempDir()
			// Fields 3-20: state ppid pgrp session tty tpgid flags ... num_threads.
			stat := fmt.Sprintf("123 (git) R 1 1 1 0 -1 %s 0 0 0 0 0 0 0 0 20 0 %s 0\n", tc.flags, tc.threads)
			if err := os.WriteFile(filepath.Join(base, "stat"), []byte(stat), 0600); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(base, "exe"))
			err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root})
			if tc.holds && err == nil {
				t.Fatal("a missing exe without proof of exit is not absence")
			}
			if !tc.holds && err != nil {
				t.Fatalf("an exiting process cannot use the workspace: %v", err)
			}
		})
	}
	t.Run("short stat is not proof", func(t *testing.T) {
		proc, base := procUseFixture(t)
		os.Remove(filepath.Join(base, "exe"))
		if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{t.TempDir()}); err == nil {
			t.Fatal("unparsable stat must hold")
		}
	})
}
func TestRecoveryInspectionExemptsOnlyExactLiveOwnDescriptors(t *testing.T) {
	proc, base := procUseFixture(t)
	own := filepath.Join(proc, fmt.Sprint(os.Getpid()))
	if err := os.Rename(base, own); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	p := filepath.Join(root, "inspected")
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd := fmt.Sprint(f.Fd())
	if err := os.Symlink(p, filepath.Join(own, "fd", fd)); err != nil {
		t.Fatal(err)
	}
	ctx := withInspectionFiles(context.Background(), []*os.File{f})
	if err := checkProcessUseAt(ctx, proc, os.Geteuid(), []string{root}); err != nil {
		t.Fatal("exact live inspector descriptor must be allowed", err)
	}
	if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root}); err == nil {
		t.Fatal("no exemption without trusted context")
	}
	if err := os.Symlink(p, filepath.Join(own, "fd", "999999")); err != nil {
		t.Fatal(err)
	}
	if err := checkProcessUseAt(ctx, proc, os.Geteuid(), []string{root}); err == nil {
		t.Fatal("other own descriptor must veto")
	}
	os.Remove(filepath.Join(own, "fd", "999999"))
	os.Remove(filepath.Join(own, "cwd"))
	if err := os.Symlink(p, filepath.Join(own, "cwd")); err != nil {
		t.Fatal(err)
	}
	if err := checkProcessUseAt(ctx, proc, os.Geteuid(), []string{root}); err == nil {
		t.Fatal("own cwd still vetoes")
	}
	os.Remove(filepath.Join(own, "cwd"))
	os.Symlink("/unrelated", filepath.Join(own, "cwd"))
	f.Close()
	if err := checkProcessUseAt(ctx, proc, os.Geteuid(), []string{root}); err == nil {
		t.Fatal("closed descriptor cannot retain exemption")
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
