//go:build linux

package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
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
	// Exit also tears down the descriptor table, so its fd links read EACCES
	// (seen under load on git, postgres and gcc children). The same proof
	// applies; a live process with an unreadable descriptor still holds.
	for _, tc := range []struct {
		name, flags string
		holds       bool
	}{
		{"unreadable fd of an exiting single thread", "4194316", false},
		{"unreadable fd of a live process", "4194312", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root reads a mode-0400 directory's links")
			}
			proc, base := procUseFixture(t)
			root := t.TempDir()
			stat := fmt.Sprintf("123 (git) R 1 1 1 0 -1 %s 0 0 0 0 0 0 0 0 20 0 1 0\n", tc.flags)
			if err := os.WriteFile(filepath.Join(base, "stat"), []byte(stat), 0600); err != nil {
				t.Fatal(err)
			}
			fd := filepath.Join(base, "fd")
			if err := os.Symlink("/unrelated", filepath.Join(fd, "3")); err != nil {
				t.Fatal(err)
			}
			// Listable, but its links cannot be read: readlink is EACCES.
			if err := os.Chmod(fd, 0400); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(fd, 0700) })
			err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root})
			if tc.holds && err == nil {
				t.Fatal("an unreadable descriptor without proof of exit is not absence")
			}
			if !tc.holds && err != nil {
				t.Fatalf("an exiting process cannot use the workspace: %v", err)
			}
		})
	}
	// Exit drops the mm while maps is being read: the read fails (ESRCH).
	// A directory in its place fails the read the same way, after the open.
	for _, tc := range []struct {
		name, flags string
		holds       bool
	}{
		{"maps read fails for an exiting single thread", "4194316", false},
		{"maps read fails for a live process", "4194312", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc, base := procUseFixture(t)
			stat := fmt.Sprintf("123 (git) R 1 1 1 0 -1 %s 0 0 0 0 0 0 0 0 20 0 1 0\n", tc.flags)
			if err := os.WriteFile(filepath.Join(base, "stat"), []byte(stat), 0600); err != nil {
				t.Fatal(err)
			}
			maps := filepath.Join(base, "maps")
			if err := os.Remove(maps); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(maps, 0700); err != nil {
				t.Fatal(err)
			}
			err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{t.TempDir()})
			if tc.holds && err == nil {
				t.Fatal("unreadable mappings without proof of exit are not absence")
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

// A process that exits between the open of its maps and the read fails the read
// ESRCH. Parallel git children do exactly that mid-scan, and holding on it kept
// archived worktrees "in use" by processes that no longer existed.
func TestRecoveryProcessUseMapsOfAnExitedProcessIsNotUse(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not on PATH")
	}
	cmd := exec.Command(sleep, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid))
	maps, err := os.Open(filepath.Join(base, "maps"))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer maps.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reaped: the opened maps now belongs to no task
	_, err = mapsUse(context.Background(), maps, []string{t.TempDir()})
	if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("reading an exited process's maps: got %v, want ESRCH", err)
	}
	if !mapsVanished(base, err) {
		t.Fatal("an exited process has no mappings and cannot hold the workspace")
	}
}

// Only ESRCH, or the process gone from /proc, proves the mappings are gone; any
// other read failure of a listed process is uncertainty and holds.
func TestRecoveryProcessUseMapsReadFailureHolds(t *testing.T) {
	_, base := procUseFixture(t)
	if mapsVanished(base, fmt.Errorf("read maps: %w", syscall.EIO)) {
		t.Fatal("EIO from a listed process is not absence")
	}
	if !mapsVanished(base, &os.PathError{Op: "read", Path: filepath.Join(base, "maps"), Err: syscall.ESRCH}) {
		t.Fatal("ESRCH means the task has no mm left")
	}
	if !mapsVanished(filepath.Join(t.TempDir(), "gone"), syscall.EIO) {
		t.Fatal("a process no longer in /proc maps nothing")
	}
	_, err := mapsUse(context.Background(), iotest.ErrReader(syscall.EIO), []string{t.TempDir()})
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("a read failure must surface: %v", err)
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
