// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"errors"
	"go/build"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

// helperEnv makes this test binary, re-executed by startHelper, report that
// it is running and then block until it is killed instead of running tests: a
// live process whose argv, working directory and environment a test chooses.
const helperEnv = "DISKWATCH_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		_, _ = os.Stdout.Write([]byte{'\n'}) // exec is done: /proc shows this image
		_, _ = io.Copy(io.Discard, os.Stdin) // until killed
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startHelper starts this test binary as a blocked process with the given
// argv[0], working directory and extra environment, and returns its pid once
// the helper has said it runs: its exec has replaced the forked image, so
// /proc shows the argv, working directory and environment chosen here.
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
	ready, stdout, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := &exec.Cmd{Path: self, Args: []string{argv0}, Dir: dir, Stdin: stdin, Stdout: stdout,
		Env: append(append(os.Environ(), helperEnv+"=1"), env...)}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	_ = stdout.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = w.Close(); _ = ready.Close() })
	if _, err := ready.Read(make([]byte, 1)); err != nil {
		t.Fatalf("the helper process exited before it ran: %v", err)
	}
	return cmd.Process.Pid
}

// scanOnly scans the real /proc entry of pid alone, through a root that holds
// just that entry. What the rest of the host runs is not this test's subject,
// and the full scan rightly fails on a host where the kernel withholds one of
// this user's processes from it for a reason the scan cannot explain, which
// would fail the test for a process it never made.
func scanOnly(t *testing.T, pid int) procSnapshot {
	t.Helper()
	root := t.TempDir()
	p := strconv.Itoa(pid)
	if err := os.Symlink(filepath.Join(procRoot, p), filepath.Join(root, p)); err != nil {
		t.Fatal(err)
	}
	boot, err := bootTime()
	if err != nil {
		t.Fatal(err)
	}
	s, err := scanProcRoot(root, reader{euid: os.Geteuid()}, boot)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A real process whose command line names the active toolchain's linker is
// found a linker, whatever its comm.
func TestScanProcesses_FindsLinkerByCommandLine(t *testing.T) {
	link := filepath.Join(build.Default.GOROOT, "pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH, "link")
	pid := startHelper(t, link, "")
	s := scanOnly(t, pid)
	if len(s.linkStarts) != 1 || s.linkStarts[0].PID != pid {
		t.Fatalf("linkStarts = %v, want the helper, pid %d", s.linkStarts, pid)
	}
}

// The real scan sees a process's working directory and environment.
func TestScanProcesses_SeesCwdAndEnvironment(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "go-build777")
	// fileBirth is bracketed by files written just before and after the
	// directory: the filesystem stamps all three from one clock, so the
	// test reads no clock of its own. creatorSlack covers a clock step
	// between them, as it does in the proof.
	before := stampFile(t, filepath.Join(tmp, "before"))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	after := stampFile(t, filepath.Join(tmp, "after"))
	if b := fileBirth(dir); b.Before(before.Add(-creatorSlack)) || b.After(after.Add(creatorSlack)) {
		t.Errorf("fileBirth = %v, want the directory's creation, between %v and %v", b, before, after)
	}
	s := scanOnly(t, startHelper(t, "helper", dir, "GOTMPDIR=/somewhere/gotmp-777-1"))
	if !s.names["go-build777"] || !s.names["gotmp-777-1"] {
		t.Fatalf("names = %v, want go-build777 (cwd) and gotmp-777-1 (environment)", s.names)
	}
}

// stampFile writes an empty file at path and returns the time the filesystem
// stamped it with.
func stampFile(t *testing.T, path string) time.Time {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

// fakeStat is a /proc/<pid>/stat line for comm, started start clock ticks
// after boot.
func fakeStat(pid int, comm string, start int) string {
	return strconv.Itoa(pid) + " (" + comm + ") S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 " + strconv.Itoa(start) + " 0 0"
}

// fakeProc writes one process of this user's into a fake /proc root.
func fakeProc(t *testing.T, root string, pid int, comm, cwd string) string {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fakeStat(pid, comm, 500)
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
	s, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0))
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
			if _, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0)); err == nil {
				t.Fatal("a scan that could not read one of this user's processes succeeded")
			}
			_, err := scanProcRoot(root, reader{euid: os.Geteuid() + 1}, time.Unix(0, 0))
			if tc.otherUserOK && err != nil {
				t.Fatalf("another user's unreadable descriptors failed the scan: %v", err)
			}
			if !tc.otherUserOK && err == nil {
				t.Fatal("a process whose stat or fd entry could not be read was skipped as exited")
			}
		})
	}
}

// Capability masks explain a refusal, not the absence of work references.
// Even a process with extra capabilities must fail an incomplete scan.
func TestScanProcRoot_CapabilitiesDoNotExcuseUnreadableReferences(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory, so the refusal cannot be staged")
	}
	for _, tc := range []struct {
		name, theirs string
	}{
		{"permitted extra capabilities", "ffffffffffffffff"},
		{"permitted no capabilities", "0000000000000000"},
		{"no readable status", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fakeProc(t, root, 10, "go", "/tmp/go-build1")
			dir := fakeProc(t, root, 11, "systemd", "/")
			if tc.theirs != "" {
				writeStatus(t, filepath.Join(dir, "status"), tc.theirs)
			}
			fd := filepath.Join(dir, "fd")
			if err := os.Chmod(fd, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(fd, 0o755) })

			s, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0))
			if !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("scan error = %v, want permission denied", err)
			}
			if len(s.names) != 0 || len(s.goStarts) != 0 {
				t.Fatalf("incomplete scan returned partial snapshot: %+v", s)
			}
		})
	}
}

// writeStatus writes a /proc status file permitting the capabilities in mask.
func writeStatus(t *testing.T, path, mask string) {
	t.Helper()
	body := "Name:\tx\nCapInh:\t0000000000000000\nCapPrm:\t" + mask + "\nCapEff:\t" + mask + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
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
		if _, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0)); err != nil {
			t.Fatalf("a process whose %v vanished failed the scan: %v", gone, err)
		}
	}
}

// changeMidRead makes the fake process at dir change under the scan: on each
// of the scan's first n reads of its maps (the last reference readProcess
// reads before its start time again), change(i) runs while the read is held
// open. maps is a FIFO for those reads and a plain file after, and the scan's
// read ends only when the FIFO's writer closes it, after change: the change
// lands between one process's reads on every run, with no timing.
func changeMidRead(t *testing.T, dir string, n int, change func(i int)) {
	t.Helper()
	maps := filepath.Join(dir, "maps")
	next := filepath.Join(dir, "maps.next")
	if err := os.Remove(maps); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(maps, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range n {
			w, err := os.OpenFile(maps, os.O_WRONLY, 0) // until the scan opens it
			if err != nil {
				t.Error(err)
				return
			}
			change(i)
			// The next read finds a fresh FIFO (or, after the last, a plain
			// file); this one stays open in the scan until w closes.
			if i+1 < n {
				err = unix.Mkfifo(next, 0o644)
			} else {
				err = os.WriteFile(next, nil, 0o644)
			}
			if err == nil {
				err = os.Rename(next, maps)
			}
			_ = w.Close()
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	t.Cleanup(func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			// The scan read maps fewer times than n: open the FIFO as its
			// reader so the writer's open returns and the goroutine ends.
			if r, err := os.OpenFile(maps, os.O_RDONLY|unix.O_NONBLOCK, 0); err == nil {
				_ = r.Close()
			}
			runtime.Gosched()
		}
	})
}

// A pid whose process exits mid-read and is taken by another is read again:
// the reads before the replacement were of the old process, and judging the
// new one by them would miss what it holds (here, a go-build* directory as
// its working directory), proving that directory dead while it is in use.
func TestScanProcRoot_PidReplacedMidReadIsReadAgain(t *testing.T) {
	root := t.TempDir()
	dir := fakeProc(t, root, 10, "sleep", "/")
	changeMidRead(t, dir, 1, func(int) {
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(fakeStat(10, "go", 900)), 0o644); err != nil {
			t.Error(err)
		}
		if err := os.Remove(filepath.Join(dir, "cwd")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink("/tmp/go-build42", filepath.Join(dir, "cwd")); err != nil {
			t.Error(err)
		}
	})
	s, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !s.names["go-build42"] {
		t.Errorf("names = %v, want go-build42, the working directory of the process that took pid 10", s.names)
	}
	if want := time.Unix(9, 0); len(s.goStarts) != 1 || !s.goStarts[0].Start.Equal(want) {
		t.Errorf("goStarts = %v, want the replacing go command, started %v", s.goStarts, want)
	}
}

// A process that exits mid-read with no other taking its pid holds nothing:
// what was read of it does not count, and the scan goes on.
func TestScanProcRoot_PidExitedMidReadHoldsNothing(t *testing.T) {
	root := t.TempDir()
	fakeProc(t, root, 10, "sleep", "/")
	dir := fakeProc(t, root, 11, "go", "/tmp/go-build1")
	changeMidRead(t, dir, 1, func(int) {
		if err := os.Remove(filepath.Join(dir, "stat")); err != nil {
			t.Error(err)
		}
	})
	s, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if s.names["go-build1"] || len(s.goStarts) != 0 {
		t.Errorf("scan = %+v, want nothing of the exited pid 11", s)
	}
}

// A pid replaced on every read is never read whole: the scan fails, so the
// reaper falls back to its age rule rather than judge from mixed reads.
func TestScanProcRoot_PidReplacedOnEveryReadFailsTheScan(t *testing.T) {
	root := t.TempDir()
	fakeProc(t, root, 10, "sleep", "/")
	dir := fakeProc(t, root, 11, "sleep", "/")
	changeMidRead(t, dir, pidReadAttempts, func(i int) {
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(fakeStat(11, "sleep", 600+i)), 0o644); err != nil {
			t.Error(err)
		}
	})
	if _, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0)); !errors.Is(err, errProcReplaced) {
		t.Fatalf("scan error = %v, want %v", err, errProcReplaced)
	}
}

// fakeStatus writes the fake process at dir a /proc/<pid>/status with the
// given Uid line (real, effective, saved, filesystem) and permitted set.
func fakeStatus(t *testing.T, dir, uids, capPrm string) {
	t.Helper()
	body := "Name:\tsystemd\nUid:\t" + uids + "\nGid:\t1\t1\t1\t1\nCapInh:\t0000000000000000\nCapPrm:\t" + capPrm + "\nCapEff:\t" + capPrm + "\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withhold makes reading the fake process's environment fail EACCES, as the
// kernel's ptrace read check refuses a capability holder's references to its
// own user. root reads a mode-000 file, so the test needs another user.
func withhold(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file: no EACCES to explain")
	}
	if err := os.Chmod(filepath.Join(dir, "environ"), 0); err != nil {
		t.Fatal(err)
	}
}

// fakeCgroupFile writes the fake process at dir a /proc/<pid>/cgroup whose
// cgroup v2 entry is path.
func fakeCgroupFile(t *testing.T, dir, path string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte("0::"+path+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// managerCgroup is this user's service manager's cgroup.
func managerCgroup() string {
	u := strconv.Itoa(os.Geteuid())
	return "/user.slice/user-" + u + ".slice/user@" + u + ".service/init.scope"
}

// A systemd manager's identity and capabilities do not prove the absence of
// references. Its unreadable environment may hold a GOTMPDIR even when its
// command line and cgroup look exactly like the user's service manager.
func TestScanProcRoot_ServiceManagerFailsIncompleteScan(t *testing.T) {
	uid := strconv.Itoa(os.Geteuid())
	ours := uid + "\t" + uid + "\t" + uid + "\t" + uid
	build := func(t *testing.T, comm string) (root, dir string) {
		root = t.TempDir()
		fakeProc(t, root, 10, "go", "/tmp/go-build1")
		dir = fakeProc(t, root, 11, comm, "/")
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("/lib/systemd/systemd\x00--user\x00GOTMPDIR=/x/gotmp-5-5\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "environ"), []byte("GOTMPDIR=/tmp/gotmp-11-1\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		withhold(t, dir)
		return root, dir
	}

	root, dir := build(t, "systemd")
	fakeStatus(t, dir, ours, "0000000800000000")
	fakeCgroupFile(t, dir, managerCgroup())
	s, err := scanProcRoot(root, reader{euid: os.Geteuid()}, time.Unix(0, 0))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("scan error = %v, want permission denied", err)
	}
	if len(s.names) != 0 || len(s.goStarts) != 0 {
		t.Fatalf("incomplete scan returned partial snapshot: %+v", s)
	}

	// Everything else unreadable still fails the proof.
	for _, tc := range []struct {
		name  string
		comm  string
		as    reader
		setup func(t *testing.T, dir string)
	}{
		{"a capability holder in another cgroup", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, ours, "0000000800000000")
			fakeCgroupFile(t, dir, "/user.slice/user-"+uid+".slice/user@"+uid+".service/app.slice/build.service")
		}},
		{"the manager's cgroup with another command", "go", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, ours, "0000000800000000")
			fakeCgroupFile(t, dir, managerCgroup())
		}},
		{"no cgroup file", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, ours, "0000000800000000")
		}},
		{"unreadable cgroup file", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, ours, "0000000800000000")
			replace(t, filepath.Join(dir, "cgroup"), false)
		}},
		{"a cgroup file with no v2 entry", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, ours, "0000000800000000")
			if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte("1:name=systemd:"+managerCgroup()+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"a uid not ours (setuid)", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, uid+"\t"+uid+"\t0\t"+uid, "0000000800000000")
			fakeCgroupFile(t, dir, managerCgroup())
		}},
		{"permitted set within the reader's", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, ours, "0000000800000000")
			fakeCgroupFile(t, dir, managerCgroup())
		}},
		{"no capability at all", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeStatus(t, dir, ours, "0000000000000000")
			fakeCgroupFile(t, dir, managerCgroup())
		}},
		{"no status", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			fakeCgroupFile(t, dir, managerCgroup())
		}},
		{"unreadable status", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			replace(t, filepath.Join(dir, "status"), false)
			fakeCgroupFile(t, dir, managerCgroup())
		}},
		{"status with no CapPrm", "systemd", reader{euid: os.Geteuid()}, func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "status"), []byte("Uid:\t"+ours+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			fakeCgroupFile(t, dir, managerCgroup())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, dir := build(t, tc.comm)
			tc.setup(t, dir)
			_, err := scanProcRoot(root, tc.as, time.Unix(0, 0))
			if err == nil {
				t.Fatal("an unexplained refusal did not fail the scan")
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("err = %v, want it to name the refusal", err)
			}
		})
	}
}
