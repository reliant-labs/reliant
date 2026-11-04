// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"os"
	"os/exec"
	"path/filepath"
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

// A real process running as a linker under another comm is found a linker.
func TestScanProcesses_FindsLinkerByCommandLine(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary on PATH")
	}
	cmd := &exec.Cmd{Path: sleep, Args: []string{"/usr/local/go/pkg/tool/linux_amd64/link", "30"}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	// The child is visible in /proc once Start returns, but its exec may
	// not have replaced the forked image's command line yet: poll for it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := scanProcesses()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range s.linkStarts {
			if p.PID == cmd.Process.Pid {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d (comm sleep, argv[0] .../link) not among the linkers %v", cmd.Process.Pid, s.linkStarts)
		}
		time.Sleep(10 * time.Millisecond)
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
// directory that process may hold.
func TestScanProcRoot_UnreadableOwnProcessFailsTheScan(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory; the unreadable case cannot be built")
	}
	root := t.TempDir()
	fakeProc(t, root, 10, "go", "/tmp/go-build1")
	s, err := scanProcRoot(root, os.Geteuid(), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("a fully readable scan failed: %v", err)
	}
	if !s.names["go-build1"] || len(s.goStarts) != 1 {
		t.Fatalf("scan = %+v, want go-build1 referenced and one go command", s)
	}

	hidden := fakeProc(t, root, 11, "sleep", "/")
	fd := filepath.Join(hidden, "fd")
	if err := os.Chmod(fd, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fd, 0o755) })
	if _, err := scanProcRoot(root, os.Geteuid(), time.Unix(0, 0)); err == nil {
		t.Fatal("a scan that could not read one of this user's processes' descriptors succeeded")
	}

	// Another user's process is not ours to read: its unreadable
	// descriptors do not fail the scan.
	if _, err := scanProcRoot(root, os.Geteuid()+1, time.Unix(0, 0)); err != nil {
		t.Fatalf("another user's unreadable process failed the scan: %v", err)
	}
}

// A process that exited mid-scan (its links gone) holds nothing and does not
// fail the scan.
func TestScanProcRoot_ExitedProcessIsNotIncomplete(t *testing.T) {
	root := t.TempDir()
	dir := fakeProc(t, root, 12, "go", "/")
	for _, l := range []string{"cwd", "root", "exe"} {
		if err := os.Remove(filepath.Join(dir, l)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := scanProcRoot(root, os.Geteuid(), time.Unix(0, 0)); err != nil {
		t.Fatalf("an exited process failed the scan: %v", err)
	}
}
