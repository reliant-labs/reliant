package openfiles

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeProc builds a procfs-shaped tree: pid -> link name -> target. fd entries
// are written as "fd/<n>". Targets need not exist, as in the real /proc.
func fakeProc(t *testing.T, procs map[string]map[string]string, maps map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake /proc tree is made of symlinks")
	}
	root := t.TempDir()
	for pid, links := range procs {
		if err := os.MkdirAll(filepath.Join(root, pid, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, target := range links {
			if err := os.Symlink(target, filepath.Join(root, pid, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for pid, content := range maps {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, pid, "maps"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestTakeProcReadsEverySource(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "cwd-dir")
	exe := filepath.Join(base, "bin", "tool")
	fd := filepath.Join(base, "fd-dir", "open.log")
	mapped := filepath.Join(base, "lib", "libx.so")
	spaced := filepath.Join(base, "has space", "lib.so")
	deleted := filepath.Join(base, "gone-dir", "unlinked.tmp")
	rootLink := filepath.Join(base, "chroot")

	root := fakeProc(t,
		map[string]map[string]string{
			"100": {
				"cwd": cwd, "exe": exe, "root": rootLink,
				"fd/0": "/dev/null",
				"fd/3": fd,
				"fd/4": "socket:[12345]",
				"fd/5": "pipe:[678]",
				"fd/6": "anon_inode:[eventpoll]",
				"fd/7": deleted + " (deleted)",
			},
		},
		map[string]string{
			"100": "00400000-00452000 r-xp 00000000 08:01 1234    " + mapped + "\n" +
				"00500000-00600000 r-xp 00000000 08:01 1235    " + spaced + "\n" +
				"7ffc0000-7ffc1000 rw-p 00000000 00:00 0         [stack]\n" +
				"7ffd0000-7ffd1000 rw-p 00000000 00:00 0 \n",
		})
	// Non-pid entries beside the processes, as in the real /proc.
	if err := os.WriteFile(filepath.Join(root, "meminfo"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := takeProc(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cwd, exe, fd, mapped, spaced, rootLink, filepath.Dir(fd), base, filepath.Dir(deleted)} {
		if !snap.Holds(p) {
			t.Errorf("Holds(%q) = false", p)
		}
	}
	for _, p := range []string{"socket:[12345]", "/stack", filepath.Join(base, "cwd-dir-sibling"), filepath.Join(base, "other")} {
		if snap.Holds(p) {
			t.Errorf("Holds(%q) = true", p)
		}
	}
	for p := range snap.covered {
		if strings.Contains(p, "(deleted)") || strings.Contains(p, "socket:") || strings.Contains(p, "[stack]") {
			t.Errorf("non-path %q leaked into the snapshot", p)
		}
	}
}

// A process that has vanished or denies access has an incomplete directory;
// the rest of the machine still counts.
func TestTakeProcSkipsVanishedProcesses(t *testing.T) {
	base := t.TempDir()
	held := filepath.Join(base, "held")
	root := fakeProc(t, map[string]map[string]string{
		"1": {"cwd": held},
		// No cwd/exe/root/maps, as when the process exited mid-scan.
		"2": {},
	}, nil)
	// A pid directory with nothing readable in it at all.
	if err := os.MkdirAll(filepath.Join(root, "3"), 0o755); err != nil {
		t.Fatal(err)
	}

	snap, err := takeProc(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Holds(held) {
		t.Error("the readable process was lost to its unreadable neighbours")
	}
}

func TestTakeProcFailsClosed(t *testing.T) {
	t.Run("procfs not mounted", func(t *testing.T) {
		if _, err := takeProc(context.Background(), filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("a missing /proc produced a snapshot")
		}
	})
	t.Run("no paths at all", func(t *testing.T) {
		root := fakeProc(t, map[string]map[string]string{"1": {"fd/0": "socket:[1]"}}, nil)
		if _, err := takeProc(context.Background(), root); err == nil {
			t.Fatal("an empty listing was accepted as a snapshot")
		}
	})
	t.Run("context already done", func(t *testing.T) {
		root := fakeProc(t, map[string]map[string]string{"1": {"cwd": "/somewhere"}}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := takeProc(ctx, root)
		if err == nil || !strings.Contains(err.Error(), "did not finish") {
			t.Fatalf("a cancelled scan must be an error: %v", err)
		}
	})
}

// TestTakeSeesAFileThisProcessHolds is the end-to-end check on the real
// /proc: no fake lsof, no PATH — a file opened here must be reported in use.
func TestTakeSeesAFileThisProcessHolds(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Take reads /proc only on Linux")
	}
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "held.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	idle := t.TempDir()

	t.Setenv("PATH", t.TempDir()) // no lsof: the answer must not depend on it
	snap, err := Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Holds(f.Name()) || !snap.Holds(dir) {
		t.Errorf("a file held open by this process is not reported in use")
	}
	if snap.Holds(idle) {
		t.Errorf("an idle directory is reported in use")
	}

	// And after it is unlinked, the directory it lived in is still held.
	if err := os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
	snap, err = Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Holds(dir) {
		t.Errorf("a directory holding an unlinked-but-open file is reported idle")
	}
}

func TestSourceEnvForcesLsofOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux has a choice of source")
	}
	t.Setenv(SourceEnv, "lsof")
	t.Setenv("PATH", t.TempDir())
	if _, err := Take(context.Background()); err == nil || !strings.Contains(err.Error(), "lsof") {
		t.Fatalf("FORGE_OPENFILES_SOURCE=lsof with no lsof must fail through the lsof source: %v", err)
	}
}
