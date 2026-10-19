// Copyright (c) 2025 Reliant Labs
//go:build linux

package pid1

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"golang.org/x/sys/unix"
)

// forwardedSignals are relayed from the init to the program: the signals a
// supervisor aims at PID 1 to mean "you, the program" — kubelet and
// `docker stop` (SIGTERM), a terminal or `docker kill -s` (the rest).
// SIGCHLD is the init's own business, SIGKILL and SIGSTOP cannot be caught,
// and the synchronous faults (SIGSEGV, SIGBUS, …) describe the init itself.
var forwardedSignals = []os.Signal{
	syscall.SIGHUP,
	syscall.SIGINT,
	syscall.SIGQUIT,
	syscall.SIGTERM,
	syscall.SIGUSR1,
	syscall.SIGUSR2,
	syscall.SIGWINCH,
}

// Init makes this process a correct init when it is PID 1, and does nothing
// otherwise. Call it first thing in main.
//
// As PID 1 it starts this same binary again — same arguments, environment,
// working directory and file descriptors — as its only child, then becomes a
// minimal init for it: it reaps every process the kernel reparents to it,
// forwards forwardedSignals to the child, and exits with the child's status
// (128+N when signal N killed it, the shell convention). Init never returns
// in that process. The child is not PID 1, so its own Init returns at once
// and it runs the program as usual.
//
// If the child cannot be started, Init says why on stderr and returns, and
// the program runs as PID 1 without reaping, as it did before this existed:
// a machine that leaks zombies beats one that does not start.
func Init() {
	if os.Getpid() != 1 {
		return
	}
	// By its real path, not /proc/self/exe: the kernel names a process after
	// the file it execs, and "exe" in ps, top and pgrep would hide reliant.
	path, err := os.Executable()
	if err != nil {
		path = "/proc/self/exe"
	}
	code, err := supervise(config{
		path:   path,
		argv:   os.Args,
		env:    os.Environ(), //nolint:forbidigo // passes this process environment to a child process; not configuration
		stderr: os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "reliant: running as PID 1 without an init; orphaned processes will not be reaped: %v\n", err)
		return
	}
	os.Exit(code)
}

type config struct {
	path   string
	argv   []string
	env    []string
	stderr io.Writer
	// subreaper adopts orphans with PR_SET_CHILD_SUBREAPER. PID 1 is sent
	// every orphan in its namespace without asking; this lets a test run the
	// same loop in a process that is not PID 1.
	subreaper bool
}

// supervise starts the program and runs the init loop until the program
// exits, returning the code the init should exit with. An error means the
// program never started and this process is as it was, so the caller can
// carry on as the program itself.
func supervise(c config) (int, error) {
	if c.subreaper {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
			return 0, fmt.Errorf("set child subreaper: %w", err)
		}
	}

	// Subscribe before the program exists, so a signal that lands while it
	// starts is queued for it rather than lost — or handled by the Go
	// runtime's default action, which ends the init and, as PID 1, every
	// process in the namespace with it.
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, forwardedSignals...)

	pid, err := start(c)
	if err != nil {
		signal.Stop(sigs)
		if c.subreaper {
			_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0)
		}
		return 0, fmt.Errorf("start %s as the init's child: %w", c.path, err)
	}
	// Tell the two apart in ps and top: they are the same binary with the
	// same arguments. Best-effort.
	_ = os.WriteFile("/proc/self/comm", []byte("reliant-init"), 0)
	// The init runs nothing but the loops below; hand back whatever package
	// initialisation allocated and no longer needs.
	debug.FreeOSMemory()

	go func() {
		for s := range sigs {
			// ESRCH means the program is already gone; reap is about to
			// collect it and exit.
			_ = syscall.Kill(pid, s.(syscall.Signal))
		}
	}()
	return reap(pid, c.stderr), nil
}

// start forks and execs the program in its own process group. When this
// process owns the terminal (docker run -it, an interactive shell) the group
// also takes the terminal's foreground, so ^C reaches the program once,
// straight from the terminal, instead of twice — once more forwarded by the
// init — which a program that treats a second ^C as "force quit" would obey.
func start(c config) (int, error) {
	sys := &syscall.SysProcAttr{Setpgid: true}
	if ownsTerminal() {
		sys.Foreground = true
		sys.Ctty = 0
	}
	return syscall.ForkExec(c.path, c.argv, &syscall.ProcAttr{
		Env: c.env,
		// Descriptors past stderr without close-on-exec pass through at
		// their own numbers, exactly as an exec would have left them.
		Files: []uintptr{0, 1, 2},
		Sys:   sys,
	})
}

// ownsTerminal reports whether stdin is a terminal whose foreground process
// group is this process's own.
func ownsTerminal() bool {
	fg, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
	return err == nil && fg == syscall.Getpgrp()
}

// reap waits for children until the program exits, collecting every orphan
// the kernel reparents to this process along the way, and returns the code
// the init exits with.
//
// Waiting for ANY child is safe here and only here: this process starts no
// other children, so there is no exec.Cmd whose status it could steal.
func reap(pid int, stderr io.Writer) int {
	for {
		var ws syscall.WaitStatus
		got, err := syscall.Wait4(-1, &ws, 0, nil)
		switch {
		case err == syscall.EINTR:
			continue
		case err != nil:
			// ECHILD with the program unreaped cannot happen; never spin on it.
			fmt.Fprintf(stderr, "reliant init: wait: %v\n", err)
			return 1
		case got == pid:
			return exitCode(ws)
		}
	}
}

// exitCode is the status an init reports for its program: the program's own
// exit code, or 128+N when signal N killed it (PID 1 cannot die of most
// signals itself, so it cannot re-raise one).
func exitCode(ws syscall.WaitStatus) int {
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}
