// Copyright (c) 2025 Reliant Labs
//go:build linux

package pid1

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The tests run this test binary as the process under test. roleEnv picks
// what it does instead of running tests:
//
//	pid1       call Init, the production entry point (the test starts it as
//	           PID 1 of a fresh PID namespace); what continues is the program
//	program    be the program: the stand-in for `reliant daemon start`
//	subreaper  run the init loop as a subreaper over a child in role program
const (
	roleEnv = "RELIANT_PID1_TEST_ROLE"
	// dieEnv makes the program SIGKILL itself once ready instead of waiting
	// for SIGTERM, to check that a death by signal reaches the init's status.
	dieEnv = "RELIANT_PID1_TEST_DIE"
	// ttyEnv makes the program the terminal one: read a line from stdin,
	// then count the SIGINTs a single ^C delivers.
	ttyEnv = "RELIANT_PID1_TEST_TTY"

	// termExitCode is what the program exits with on SIGTERM. Seeing it as
	// the init's status proves the signal was forwarded AND the code passed
	// back.
	termExitCode = 42
	// liveOrphans is how many orphans the program leaves running when it
	// reports ready, so the test can see they were reparented to the init.
	liveOrphans = 5
	// liveOrphanCmd leaves one. The sleep outlives any test: the test kills
	// it once it has seen it, so a loaded machine cannot let it exit (and be
	// reaped) before it is counted.
	liveOrphanCmd  = "sleep " + liveOrphanSecs + " & exit 0"
	liveOrphanSecs = "300"
)

func TestMain(m *testing.M) {
	switch os.Getenv(roleEnv) {
	case "pid1":
		Init()
		runProgram()
	case "program":
		runProgram()
	case "subreaper":
		code, err := supervise(config{
			path:      "/proc/self/exe",
			argv:      os.Args,
			env:       withRole(os.Environ(), "program"),
			stderr:    os.Stderr,
			subreaper: true,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(code)
	default:
		os.Exit(m.Run())
	}
}

// runProgram behaves like the daemon as far as processes go: it waits for its
// own children through os/exec while their children orphan and die, leaves a
// few orphans running, prints "ready", then waits for SIGTERM.
func runProgram() {
	if os.Getenv(ttyEnv) == "1" {
		runTerminalProgram()
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)

	// Each shell exits 7 while its background sleep lives on, reparented to
	// the init and dying there moments later, so the init is reaping while
	// these Waits run. Every Wait must still get its own child's status.
	for i := 0; i < 30; i++ {
		err := exec.Command("sh", "-c", "sleep 0.01 & exit 7").Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
			fmt.Fprintf(os.Stderr, "exec.Cmd.Wait returned %v, want exit status 7\n", err)
			os.Exit(3)
		}
		if err := exec.Command("true").Run(); err != nil {
			fmt.Fprintf(os.Stderr, "exec.Cmd.Wait for true returned %v, want nil\n", err)
			os.Exit(3)
		}
	}
	if os.Getenv(dieEnv) == "1" {
		// No live orphans: nothing would kill them once the init is gone.
		fmt.Println("ready")
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	for i := 0; i < liveOrphans; i++ {
		if err := exec.Command("sh", "-c", liveOrphanCmd).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "orphaning sleep: %v\n", err)
			os.Exit(3)
		}
	}
	fmt.Println("ready")
	<-sigs
	os.Exit(termExitCode)
}

// runTerminalProgram is `docker run -it reliant daemon start --token`: it
// reads a pasted line from the terminal, then treats a second SIGINT the way
// the daemon does — as "force quit" — by exiting with 10 + the number of
// SIGINTs that arrived.
func runTerminalProgram() {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		os.Exit(3)
	}
	fmt.Printf("read %s", line)
	fmt.Println("ready")
	<-sigs
	n := 1
	settle := time.After(500 * time.Millisecond)
	for {
		select {
		case <-sigs:
			n++
		case <-settle:
			os.Exit(10 + n)
		}
	}
}

// TestSupervise_OnATerminalTheProgramOwnsIt covers a TTY-attached container.
// The program must be able to read the terminal (a pasted token) rather than
// stop on SIGTTIN, and one ^C must reach it once: the terminal signals its
// foreground process group, so a program sharing the init's group would get
// it a second time, forwarded, and force-quit.
func TestSupervise_OnATerminalTheProgramOwnsIt(t *testing.T) {
	t.Parallel()
	master, slave := openPTY(t)
	cmd, _ := helper(t, "subreaper")
	cmd.Env = append(cmd.Env, ttyEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own with the terminal as its controlling TTY, as a
	// container runtime sets up PID 1 for `docker run -t`.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	lines := make(chan string, 16)
	go func() {
		s := bufio.NewScanner(master)
		for s.Scan() {
			lines <- strings.TrimRight(s.Text(), "\r")
		}
	}()
	if _, err := master.Write([]byte("token\n")); err != nil {
		t.Fatal(err)
	}
	awaitReady(t, lines)
	if _, err := master.Write([]byte{0x03}); err != nil { // ^C
		t.Fatal(err)
	}
	if got := waitExit(t, cmd); got != 11 {
		t.Fatalf("init exit code = %d, want 11: the program should see exactly one SIGINT for one ^C (10 + count)", got)
	}
}

// openPTY opens a pseudo-terminal pair: what the test types goes into master,
// and the helper's stdio is slave.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("pty number: %v", err)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open pty slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

// TestInit_AsPID1ReapsOrphansAndPassesSignalsAndStatus is the production
// shape: reliant started as PID 1 of its own PID namespace, like a
// container's entrypoint. Unprivileged user namespaces stand in for the
// container runtime; where they are disabled the test skips and the
// subreaper test below still covers the init loop.
func TestInit_AsPID1ReapsOrphansAndPassesSignalsAndStatus(t *testing.T) {
	t.Parallel()
	cmd, lines := helper(t, "pid1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot create a user+PID namespace here (%v); the subreaper test covers the init loop", err)
	}

	assertSupervises(t, cmd, lines)
}

// TestSupervise_AsSubreaperReapsOrphansAndPassesSignalsAndStatus runs the
// same init loop without namespaces, adopting orphans as a subreaper.
func TestSupervise_AsSubreaperReapsOrphansAndPassesSignalsAndStatus(t *testing.T) {
	t.Parallel()
	cmd, lines := helper(t, "subreaper")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	assertSupervises(t, cmd, lines)
}

func TestSupervise_ReportsDeathBySignalAs128PlusSignal(t *testing.T) {
	t.Parallel()
	cmd, lines := helper(t, "subreaper")
	cmd.Env = append(cmd.Env, dieEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	awaitReady(t, lines)

	if got, want := waitExit(t, cmd), 128+int(syscall.SIGKILL); got != want {
		t.Fatalf("init exit code = %d, want %d (the program was SIGKILLed)", got, want)
	}
}

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		ws   syscall.WaitStatus
		want int
	}{
		{"exited 0", syscall.WaitStatus(0), 0},
		{"exited 7", syscall.WaitStatus(7 << 8), 7},
		{"killed by SIGTERM", syscall.WaitStatus(syscall.SIGTERM), 143},
		{"killed by SIGKILL", syscall.WaitStatus(syscall.SIGKILL), 137},
	} {
		if got := exitCode(tc.ws); got != tc.want {
			t.Errorf("%s: exitCode = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// assertSupervises drives a started init: the program's own waits succeed
// (else it exits 3 before ready), its orphans land on the init and are reaped
// there, and SIGTERM sent to the init reaches the program, whose exit code
// becomes the init's.
func assertSupervises(t *testing.T, cmd *exec.Cmd, lines <-chan string) {
	t.Helper()
	initPid := cmd.Process.Pid
	awaitReady(t, lines)

	// Not vacuous: the orphans exist, and the kernel handed them to the init.
	// Each is the init's child by the time the program reports ready (the
	// kernel reparents it before the shell's Wait returns), but it may still
	// be the shell's fork on its way to exec'ing sleep, so wait until each
	// one is. They live until killed below, so none can exit, be reaped and
	// go uncounted. Matched by command line: a short-lived sleep from the
	// program's earlier rounds may still be about.
	deadline := time.Now().Add(10 * time.Second)
	for {
		n := countChildren(t, initPid, isLiveOrphan)
		if n >= liveOrphans {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("init (pid %d) has %d live orphan children after ready, want %d reparented to it", initPid, n, liveOrphans)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// End them. Whatever is left after that is a zombie nobody will ever
	// wait for.
	killChildren(t, initPid, isLiveOrphan)
	deadline = time.Now().Add(5 * time.Second)
	for {
		zombies := countChildren(t, initPid, func(p proc) bool { return p.state == "Z" })
		alive := countChildren(t, initPid, func(p proc) bool { return p.comm == "sleep" && p.state != "Z" })
		if zombies == 0 && alive == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d zombie children of the init (pid %d) were never reaped (%d orphans still running)", zombies, initPid, alive)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM the init: %v", err)
	}
	if got := waitExit(t, cmd); got != termExitCode {
		t.Fatalf("init exit code after SIGTERM = %d, want %d (the program's own code on SIGTERM)", got, termExitCode)
	}
}

// helper prepares this test binary in the given role, with its stdout read
// back line by line. Stdout and stderr are real files, so os/exec starts no
// copying goroutine and the helper's children inherit them directly.
func helper(t *testing.T, role string) (*exec.Cmd, <-chan string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	lines := make(chan string, 16)
	go func() {
		s := bufio.NewScanner(r)
		for s.Scan() {
			lines <- s.Text()
		}
	}()

	// -test.run=^$ makes a helper that somehow misses its role run no tests
	// rather than recursing into these ones.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = withRole(os.Environ(), role)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		killTree(cmd)
		// Reap it here, while this test binary is alive: a child it never
		// waits for outlives it as a zombie of the machine's PID 1.
		_, _ = cmd.Process.Wait()
	})
	return cmd, lines
}

// withRole sets roleEnv in env. It replaces any inherited value rather than
// appending a second one: Go's getenv reads the FIRST of duplicate keys, so an
// appended role would be ignored and the helper would rerun its parent's.
func withRole(env []string, role string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, roleEnv+"=") {
			out = append(out, kv)
		}
	}
	return append(out, roleEnv+"="+role)
}

func awaitReady(t *testing.T, lines <-chan string) {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line := <-lines:
			if line == "ready" {
				return
			}
		case <-timeout:
			t.Fatal("the program never reported ready")
		}
	}
}

func waitExit(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if ws.Signaled() {
			t.Fatalf("the init itself died of %v; it should exit with its program's status", ws.Signal())
		}
		return ws.ExitStatus()
	case <-time.After(15 * time.Second):
		t.Fatal("the init did not exit")
		return -1
	}
}

type proc struct {
	pid, ppid int
	comm      string
	state     string
}

func countChildren(t *testing.T, parent int, match func(proc) bool) int {
	t.Helper()
	ps, err := processes()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range ps {
		if p.ppid == parent && match(p) {
			n++
		}
	}
	return n
}

// isLiveOrphan reports whether p is a running sleep from liveOrphanCmd.
func isLiveOrphan(p proc) bool {
	if p.state == "Z" {
		return false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/cmdline")
	return err == nil && string(raw) == "sleep\x00"+liveOrphanSecs+"\x00"
}

// killChildren SIGKILLs parent's children that match.
func killChildren(t *testing.T, parent int, match func(proc) bool) {
	t.Helper()
	ps, err := processes()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ppid == parent && match(p) {
			if err := syscall.Kill(p.pid, syscall.SIGKILL); err != nil {
				t.Fatalf("kill orphan %d: %v", p.pid, err)
			}
		}
	}
}

func processes() ([]proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // exited since ReadDir
		}
		// "pid (comm) state ppid ...": comm may hold spaces and parens.
		s := string(raw)
		open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || closing < open {
			continue
		}
		fields := strings.Fields(s[closing+1:])
		if len(fields) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		out = append(out, proc{pid: pid, ppid: ppid, comm: s[open+1 : closing], state: fields[0]})
	}
	return out, nil
}

// killTree SIGKILLs a helper that is still running together with its
// descendants: in subreaper mode the init is not PID 1, so killing it alone
// would leave the program running (as PID 1 of its namespace, killing it
// takes the namespace down regardless). A helper already waited for is left
// alone — its pid, and its children's, may belong to someone else by now.
func killTree(cmd *exec.Cmd) {
	if cmd.Process.Signal(syscall.Signal(0)) != nil {
		return
	}
	ps, _ := processes()
	var walk func(int)
	walk = func(p int) {
		for _, c := range ps {
			if c.ppid == p {
				walk(c.pid)
			}
		}
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	walk(cmd.Process.Pid)
}
