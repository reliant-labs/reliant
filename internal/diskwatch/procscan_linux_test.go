// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"go/build"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// A linker is known by its executable and command line, not only by a comm
// of exactly "link": a -toolexec wrapper, or a linker run through a renamed
// binary, still counts as a possible creator of go-link-* directories.
func TestCreatorKinds(t *testing.T) {
	for _, tc := range []struct {
		name, comm, exe string
		cmdline         string
		isGo, isLink    bool
	}{
		{"go command", "go", "/usr/local/go/bin/go", "go\x00build\x00./...\x00", true, false},
		{"linker", "link", "/usr/local/go/pkg/tool/linux_amd64/link", "/usr/local/go/pkg/tool/linux_amd64/link\x00-o\x00a.out\x00", false, true},
		{"linker under another comm", "ld-wrapper", "/opt/ld-wrapper", "/usr/local/go/pkg/tool/linux_amd64/link\x00-o\x00a.out\x00", false, true},
		{"linker by exe alone", "x", "/usr/local/go/pkg/tool/linux_amd64/link", "", false, true},
		{"deleted linker binary", "x", "/root/go/pkg/tool/linux_amd64/link (deleted)", "", false, true},
		{"toolexec wrapper", "toolexec", "/bin/toolexec", "/bin/toolexec\x00/usr/local/go/pkg/tool/linux_amd64/link\x00-o\x00a.out\x00", false, true},
		{"go toolchain via GOTOOLCHAIN", "go1.22.0", "/home/u/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.22.0.linux-amd64/bin/go", "go\x00test\x00", true, false},
		{"unrelated", "sleep", "/usr/bin/sleep", "sleep\x0030\x00", false, false},
		{"argument named link is not argv[0..1]", "cp", "/usr/bin/cp", "cp\x00-r\x00link\x00", false, false},
	} {
		isGo, isLink := creatorKinds(tc.comm, tc.exe, []byte(tc.cmdline))
		if isGo != tc.isGo || isLink != tc.isLink {
			t.Errorf("%s: creatorKinds = go %v link %v, want go %v link %v", tc.name, isGo, isLink, tc.isGo, tc.isLink)
		}
	}
}

// helperEnv makes this test binary, re-executed by startHelper, block until
// it is killed instead of running tests: a live process whose argv, working
// directory and environment a test chooses.
const helperEnv = "DISKWATCH_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin) // until killed
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startHelper starts this test binary as a blocked process with the given
// argv[0], working directory and extra environment, and returns its pid once
// /proc shows its exec done (its environment carries helperEnv).
func startHelper(t *testing.T, argv0, dir string, env ...string) int {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdin, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := &exec.Cmd{Path: self, Args: []string{argv0}, Dir: dir, Stdin: stdin,
		Env: append(append(os.Environ(), helperEnv+"=1"), env...)}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = w.Close() })
	return cmd.Process.Pid
}

// waitScan scans /proc until found accepts the snapshot: a child is visible
// once Start returns, but its exec may not have replaced the forked image yet.
func waitScan(t *testing.T, found func(procSnapshot) bool) procSnapshot {
	t.Helper()
	for i := 0; ; i++ {
		s, err := scanProcesses()
		if err != nil {
			t.Fatal(err)
		}
		if found(s) {
			return s
		}
		if i == 1000 {
			t.Fatalf("the helper process never appeared as expected: %+v", s)
		}
		runtime.Gosched()
	}
}

// A real process whose command line names the active toolchain's linker is
// found a linker, whatever its comm.
func TestScanProcesses_FindsLinkerByCommandLine(t *testing.T) {
	link := filepath.Join(build.Default.GOROOT, "pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH, "link")
	pid := startHelper(t, link, "")
	waitScan(t, func(s procSnapshot) bool {
		for _, p := range s.linkStarts {
			if p.PID == pid {
				return true
			}
		}
		return false
	})
}

// The real scan sees a process's working directory and environment.
func TestScanProcesses_SeesCwdAndEnvironment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-build777")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	startHelper(t, "helper", dir, "GOTMPDIR=/somewhere/gotmp-777-1")
	waitScan(t, func(s procSnapshot) bool { return s.names["go-build777"] && s.names["gotmp-777-1"] })
	if b := fileBirth(dir); b.IsZero() || b.After(time.Now()) || b.Before(time.Now().Add(-time.Hour)) {
		t.Errorf("fileBirth = %v, want the directory's creation, just now", b)
	}
}

// fakeProc writes one process of this user's into a fake /proc root.
func fakeProc(t *testing.T, root string, pid int, comm, cwd string) string {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (" + comm + ") S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 500 0 0"
	for name, body := range map[string]string{"stat": stat, "cmdline": comm + "\x00", "environ": "", "maps": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []string{"cwd", "root", "exe"} {
		if err := os.Symlink(cwd, filepath.Join(dir, l)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A scan that could not read one of this user's processes is not a proof:
// it fails, so the reaper falls back to its age rule instead of removing a
// directory that process may hold. Only an entry that is gone (ENOENT/ESRCH)
// is an exit; every other failure is a /proc the scan cannot trust.
func TestScanProcRoot_UnreadableProcessFailsTheScan(t *testing.T) {
	root := t.TempDir()
	fakeProc(t, root, 10, "go", "/tmp/go-build1")
	s, err := scanProcRoot(root, os.Geteuid(), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("a fully readable scan failed: %v", err)
	}
	if !s.names["go-build1"] || len(s.goStarts) != 1 {
		t.Fatalf("scan = %+v, want go-build1 referenced and one go command", s)
	}

	for _, tc := range []struct {
		name    string
		breakIt func(t *testing.T, dir string)
		// otherUserOK: the failure is in what only the owner reads, so
		// another user's process with it does not fail the scan.
		otherUserOK bool
	}{
		{"unreadable fd directory", func(t *testing.T, dir string) { replace(t, filepath.Join(dir, "fd"), true) }, true},
		{"unreadable fd entry", func(t *testing.T, dir string) {
			// Lstat of <pid>/fd fails ENOTDIR: neither exit nor ours to skip.
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"unreadable stat", func(t *testing.T, dir string) { replace(t, filepath.Join(dir, "stat"), false) }, false},
		{"malformed stat", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("11 (sleep) S 1"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fakeProc(t, root, 10, "go", "/tmp/go-build1")
			tc.breakIt(t, fakeProc(t, root, 11, "sleep", "/"))
			if _, err := scanProcRoot(root, os.Geteuid(), time.Unix(0, 0)); err == nil {
				t.Fatal("a scan that could not read one of this user's processes succeeded")
			}
			_, err := scanProcRoot(root, os.Geteuid()+1, time.Unix(0, 0))
			if tc.otherUserOK && err != nil {
				t.Fatalf("another user's unreadable descriptors failed the scan: %v", err)
			}
			if !tc.otherUserOK && err == nil {
				t.Fatal("a process whose stat or fd entry could not be read was skipped as exited")
			}
		})
	}
}

// replace swaps path for a non-directory (dir) or a directory (!dir): reading
// it then fails with ENOTDIR or EISDIR, an error that is not an exit, for
// root too (a mode-000 directory is readable by root).
func replace(t *testing.T, path string, dir bool) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	var err error
	if dir {
		err = os.WriteFile(path, nil, 0o644)
	} else {
		err = os.Mkdir(path, 0o755)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// A process that exited mid-scan (its entries gone, ENOENT) holds nothing
// and does not fail the scan, wherever in the scan its exit is seen.
func TestScanProcRoot_ExitedProcessIsNotIncomplete(t *testing.T) {
	for _, gone := range [][]string{{"cwd", "root", "exe"}, {"stat"}, {"fd", "stat", "cmdline"}} {
		root := t.TempDir()
		fakeProc(t, root, 10, "go", "/")
		dir := fakeProc(t, root, 12, "go", "/")
		for _, l := range gone {
			if err := os.RemoveAll(filepath.Join(dir, l)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := scanProcRoot(root, os.Geteuid(), time.Unix(0, 0)); err != nil {
			t.Fatalf("a process whose %v vanished failed the scan: %v", gone, err)
		}
	}
}
