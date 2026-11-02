//go:build linux

package worktreereclaim

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/procledger"
	"github.com/reliant-labs/reliant/internal/proctree"
)

// undumpableHelperEnv makes the test binary an undumpable process of the test's
// user (runTestHelper): it calls prctl(PR_SET_DUMPABLE, 0), as ssh-agent and
// credsandbox's init do, prints "ready", and lives until its stdin closes.
const undumpableHelperEnv = "WORKTREERECLAIM_TEST_UNDUMPABLE"

// runTestHelper runs this binary as a helper process when asked to, and exits;
// TestMain calls it first.
func runTestHelper() {
	if os.Getenv(undumpableHelperEnv) != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		fmt.Fprintln(os.Stderr, "prctl:", err)
		os.Exit(2)
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// uninspectableFixture is a fake /proc entry for pid whose maps cannot be
// opened (EACCES), as an undumpable process's cannot, with the identity /proc
// still shows of one. cmdline nil makes cmdline unreadable too.
func uninspectableFixture(t *testing.T, pid int, comm, name string, cmdline *string, pgrp int, start uint64) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0000 file")
	}
	proc, base := procUseFixture(t)
	dir := filepath.Join(proc, strconv.Itoa(pid))
	if err := os.Rename(base, dir); err != nil {
		t.Fatal(err)
	}
	// Fields 3-22: state ppid pgrp session tty tpgid flags minflt cminflt
	// majflt cmajflt utime stime cutime cstime priority nice num_threads
	// itrealvalue starttime.
	stat := fmt.Sprintf("%d (%s) S 1 %d %d 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0\n", pid, comm, pgrp, pgrp, start)
	write := func(name, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", stat, 0o600)
	write("status", "Name:\t"+name+"\nUmask:\t0022\nState:\tS (sleeping)\n", 0o600)
	if cmdline != nil {
		write("cmdline", *cmdline, 0o600)
	} else {
		write("cmdline", "", 0)
	}
	write("maps", "", 0)
	return proc
}

func argv(args ...string) *string {
	s := strings.Join(args, "\x00") + "\x00"
	return &s
}

// Rule (b): an undumpable process that is a known agent, matched exactly by
// comm, status Name and (when readable) cmdline, is not use; every near miss
// still holds.
func TestUninspectable_KnownAgentsOnlyByExactIdentity(t *testing.T) {
	empty := ""
	for _, tc := range []struct {
		name, comm, status string
		cmdline            *string
		use                bool
	}{
		{"ssh-agent", "ssh-agent", "ssh-agent", argv("ssh-agent", "-s"), false},
		{"ssh-agent by path", "ssh-agent", "ssh-agent", argv("/usr/bin/ssh-agent", "-D", "-a", "/run/user/1000/openssh_agent"), false},
		{"gpg-agent", "gpg-agent", "gpg-agent", argv("gpg-agent", "--homedir", "/home/u/.gnupg", "--daemon"), false},
		{"gnome-keyring-daemon, comm cut to 15 bytes", "gnome-keyring-d", "gnome-keyring-d", argv("/usr/bin/gnome-keyring-daemon", "--daemonize", "--login"), false},
		{"dbus-daemon", "dbus-daemon", "dbus-daemon", argv("/usr/bin/dbus-daemon", "--session", "--address=systemd:", "--nofork"), false},
		{"dbus-broker", "dbus-broker", "dbus-broker", argv("/usr/bin/dbus-broker", "--log", "4", "--controller", "9", "--machine-id", "x"), false},
		{"systemd --user", "systemd", "systemd", argv("/usr/lib/systemd/systemd", "--user"), false},
		{"systemd --user, re-executed", "systemd", "systemd", argv("/lib/systemd/systemd", "--user", "--deserialize=43"), false},
		{"systemd's (sd-pam)", "(sd-pam)", "(sd-pam)", argv("(sd-pam)"), false},
		{"cmdline unreadable: comm and Name decide", "ssh-agent", "ssh-agent", nil, false},

		{"unknown process", "worktreereclaim", "worktreereclaim", argv("/tmp/worktreereclaim.test"), true},
		{"systemd without --user", "systemd", "systemd", argv("/usr/lib/systemd/systemd", "--system"), true},
		{"systemd with a look-alike flag", "systemd", "systemd", argv("/usr/lib/systemd/systemd", "--user-unit=x"), true},
		{"systemd, cmdline unreadable: --user unproven", "systemd", "systemd", nil, true},
		{"comm with the name inside it", "my-ssh-agent", "my-ssh-agent", argv("my-ssh-agent"), true},
		{"comm extending the name", "ssh-agent2", "ssh-agent2", argv("ssh-agent2"), true},
		{"argv[0] extending the name", "ssh-agent", "ssh-agent", argv("/usr/bin/ssh-agent-evil"), true},
		{"argv[0] another program", "ssh-agent", "ssh-agent", argv("/usr/bin/python3", "ssh-agent"), true},
		{"status Name disagrees with comm", "ssh-agent", "bash", argv("ssh-agent"), true},
		{"cut comm, wrong full name", "gnome-keyring-d", "gnome-keyring-d", argv("/usr/bin/gnome-keyring-dump"), true},
		{"empty cmdline (inside execve): comm and Name decide", "ssh-agent", "ssh-agent", &empty, false},
		{"systemd, empty cmdline: --user unproven", "systemd", "systemd", &empty, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pid := 4900001
			proc := uninspectableFixture(t, pid, tc.comm, tc.status, tc.cmdline, pid, 777)
			err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{t.TempDir()})
			if tc.use && err == nil {
				t.Fatal("an uninspectable process that is not a known agent must hold")
			}
			if !tc.use && err != nil {
				t.Fatalf("a known agent is not use: %v", err)
			}
			if tc.use && !strings.Contains(err.Error(), "("+tc.comm+")") {
				t.Errorf("the hold should name the process: %v", err)
			}
		})
	}
}

// Only a permission failure is uninspectability. A known agent whose state is
// missing for another reason still holds.
func TestUninspectable_OtherFailuresStillHold(t *testing.T) {
	pid := 4900002
	proc := uninspectableFixture(t, pid, "ssh-agent", "ssh-agent", argv("ssh-agent"), pid, 777)
	maps := filepath.Join(proc, strconv.Itoa(pid), "maps")
	if err := os.Remove(maps); err != nil {
		t.Fatal(err)
	}
	if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{t.TempDir()}); err == nil {
		t.Fatal("missing maps is not proof of anything, whatever the process is called")
	}
}

// Rule (a): an undumpable process the daemon accounts for is checked against
// the paths the daemon recorded for it, and only when pid, process group and
// start time all match the ledger.
func TestUninspectable_DaemonAccountedProcess(t *testing.T) {
	const start = 123456
	root := t.TempDir()
	outside := t.TempDir()
	for i, tc := range []struct {
		name  string
		entry procledger.Process
		use   bool
	}{
		{"accounted, holding nothing in the workspace", procledger.Process{Pgid: -1, Start: start, Paths: []string{outside, "/usr/bin/reliant"}}, false},
		{"accounted, its cwd in the workspace", procledger.Process{Pgid: -1, Start: start, Paths: []string{filepath.Join(root, "sub"), "/usr/bin/reliant"}}, true},
		{"accounted, its executable in the workspace", procledger.Process{Pgid: -1, Start: start, Paths: []string{outside, filepath.Join(root, "bin", "reliant")}}, true},
		{"recycled pid: another start time", procledger.Process{Pgid: -1, Start: start + 1, Paths: []string{outside}}, true},
		{"another process group", procledger.Process{Pgid: 1, Start: start, Paths: []string{outside}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pid := 4910000 + i
			proc := uninspectableFixture(t, pid, "reliant", "reliant", argv("/usr/bin/reliant", credsandbox.HelperArg), pid, start)
			entry := tc.entry
			entry.Pid = pid
			if entry.Pgid == -1 {
				entry.Pgid = pid
			}
			procledger.Record(entry)
			t.Cleanup(func() { procledger.Forget(entry.Pid, entry.Start) })
			err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root})
			if tc.use && err == nil {
				t.Fatal("must hold")
			}
			if !tc.use && err != nil {
				t.Fatalf("an accounted process holding nothing in the workspace is not use: %v", err)
			}
		})
	}
	t.Run("forgotten once reaped", func(t *testing.T) {
		pid := 4919999
		proc := uninspectableFixture(t, pid, "reliant", "reliant", argv("/usr/bin/reliant", credsandbox.HelperArg), pid, start)
		procledger.Record(procledger.Process{Pid: pid, Pgid: pid, Start: start, Paths: []string{outside}})
		procledger.Forget(pid, start)
		if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root}); err == nil {
			t.Fatal("a process the ledger no longer lists is unaccounted, and holds")
		}
	})
}

// liveProc is a private /proc holding only the given real processes: entries
// link to their real /proc/<pid>, so every read is the kernel's, and no other
// process of the host (another agent's, another test binary's) can change the
// verdict.
func liveProc(t *testing.T, pids ...int) string {
	t.Helper()
	proc := t.TempDir()
	for _, pid := range pids {
		if err := os.Symlink(filepath.Join("/proc", strconv.Itoa(pid)), filepath.Join(proc, strconv.Itoa(pid))); err != nil {
			t.Fatal(err)
		}
	}
	return proc
}

// waitUninspectable waits until /proc refuses to list pid's descriptors, as it
// does once the process is undumpable, and skips when it never does (root, or
// a kernel that shows it).
func waitUninspectable(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
		if errors.Is(err, fs.ErrPermission) {
			return
		}
		if time.Now().After(deadline) {
			t.Skipf("process %d stayed inspectable here (%v): nothing to prove", pid, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitListening waits for an agent's socket to exist: the agent is running.
func waitListening(t *testing.T, sock string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ssh-agent never created %s", sock)
		}
	}
}

// startUndumpable starts this binary as an undumpable helper in dir, its own
// process group's leader, and returns it once it is undumpable. Kept alive as
// briefly as the caller needs: while it lives, every same-owner process-use
// check on the host holds.
func startUndumpable(t *testing.T, dir string) (*exec.Cmd, func()) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), undumpableHelperEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once bool
	stop := func() {
		if once {
			return
		}
		once = true
		_ = stdin.Close()
		_ = cmd.Wait()
	}
	t.Cleanup(stop)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		stop()
		t.Fatalf("helper did not start: %q %v", line, err)
	}
	return cmd, stop
}

// An undumpable process that is neither accounted for nor a known agent holds,
// even with its working directory outside the workspace: nothing proves where
// it is.
func TestUninspectable_RealUnknownUndumpableProcessHolds(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	cmd, stop := startUndumpable(t, outside)
	pid := cmd.Process.Pid
	waitUninspectable(t, pid)
	err := checkProcessUseAt(context.Background(), liveProc(t, pid), os.Geteuid(), []string{root})
	stop()
	if err == nil {
		t.Fatal("an unknown undumpable process must hold the workspace")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("held for the wrong reason: %v", err)
	}
}

// The same real undumpable process, entered in the daemon's ledger: the
// ledger's paths decide, and the kernel's process group and start time must
// match it.
func TestUninspectable_RealAccountedUndumpableProcess(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	cmd, stop := startUndumpable(t, outside)
	pid := cmd.Process.Pid
	waitUninspectable(t, pid)
	id, err := readProcIdentity(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	proc := liveProc(t, pid)
	check := func(paths []string, start uint64) error {
		procledger.Record(procledger.Process{Pid: pid, Pgid: id.pgrp, Start: start, Paths: paths})
		defer procledger.Forget(pid, start)
		return checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root})
	}
	clear := check([]string{outside}, id.start)
	inside := check([]string{root}, id.start)
	recycled := check([]string{outside}, id.start+1)
	stop()
	if id.pgrp != pid {
		t.Fatalf("helper is its own process group's leader, /proc says pgrp %d", id.pgrp)
	}
	if clear != nil {
		t.Errorf("accounted and holding nothing in the workspace: %v", clear)
	}
	if inside == nil {
		t.Error("accounted with a path in the workspace must hold")
	}
	if recycled == nil {
		t.Error("a ledger entry for another start time must not account for this process")
	}
}

// End to end through the daemon's launch path: credsandbox's init, started as
// a proctree tree, is entered in the ledger with what it holds, is not use of
// a workspace it does not run in, and leaves the ledger once released.
func TestUninspectable_SandboxInitStartedThroughProctreeIsAccounted(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := credsandbox.Available(self); err != nil {
		t.Skipf("this host does not allow the sandbox: %v", err)
	}
	root, outside := t.TempDir(), t.TempDir()
	cmd := exec.Command("cat")
	cmd.Dir = outside
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if refusal := credsandbox.Apply(cmd, self, credsandbox.Plan{}); refusal != "" {
		t.Fatalf("sandbox refused: %s", refusal)
	}
	tree := proctree.Default().Prepare(cmd, proctree.Owner{Command: "cat"})
	if err := cmd.Start(); err != nil {
		tree.Started(0)
		t.Fatal(err)
	}
	tree.Started(cmd.Process.Pid)
	pid := cmd.Process.Pid
	var once bool
	stop := func() {
		if !once {
			once = true
			_ = stdin.Close()
			_ = cmd.Wait()
			tree.Release()
		}
	}
	t.Cleanup(stop)
	entry, ok := procledger.Lookup(pid)
	if !ok {
		stop()
		t.Fatal("the sandbox init was not entered in the ledger")
	}
	waitUninspectable(t, pid)
	err = checkProcessUseAt(context.Background(), liveProc(t, pid), os.Geteuid(), []string{root})
	held := checkProcessUseAt(context.Background(), liveProc(t, pid), os.Geteuid(), []string{outside})
	stop()
	if err != nil {
		t.Fatalf("the daemon's own sandbox init, running elsewhere, is not use: %v", err)
	}
	if held == nil {
		t.Fatal("the sandbox init running in the workspace must hold it")
	}
	if want, _ := filepath.EvalSymlinks(outside); entry.Paths[0] != want || entry.Pgid != pid {
		t.Errorf("ledger entry %+v: want cwd %s and process group %d", entry, want, pid)
	}
	if _, ok := procledger.Lookup(pid); ok {
		t.Error("a released tree must leave the ledger")
	}
}
