//go:build linux

package osutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const supervisorArg = "__reliant-command-supervisor"

// Only the re-exec child takes this path. No service/container is created.
// Keeping the dispatch here also lets the real supervisor run in test binaries.
func init() {
	if len(os.Args) > 3 && os.Args[1] == supervisorArg {
		parts := strings.Split(os.Args[2], ",")
		grace, err := time.ParseDuration(parts[0])
		if err != nil {
			os.Exit(125)
		}
		drain := 10 * time.Second
		if len(parts) > 1 {
			if parsed, e := time.ParseDuration(parts[1]); e == nil && parsed > 0 {
				drain = parsed
			}
		}
		if len(parts) > 2 {
			if fd, e := strconv.Atoi(parts[2]); e == nil {
				unix.CloseOnExec(fd)
			}
		}
		result := supervisorResult{}
		code, err := superviseCommand(os.Args[3:], grace, drain, &result)
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Complete = true
		}
		if len(parts) < 3 {
			os.Exit(125)
		}
		fd, parseErr := strconv.Atoi(parts[2])
		if parseErr != nil {
			os.Exit(125)
		}
		control := os.NewFile(uintptr(fd), "supervisor-result")
		encoded, _ := json.Marshal(result)
		// SO_PASSCRED on the receiver supplies credentials from the kernel for
		// this send. A payload that obtains/duplicates the endpoint still sends
		// with its own PID, not the supervisor's.
		writeErr := unix.Sendmsg(int(control.Fd()), encoded, nil, nil, unix.MSG_NOSIGNAL)
		_ = control.Close()
		// The foreground caller may explicitly detach and close its receiver.
		// Missing proof then remains uncertain to that caller, but must not
		// rewrite a completed payload's actual status in the background manager.
		_ = writeErr
		os.Exit(code)
	}
}

// PrepareCommandSupervision wraps a not-yet-started Cmd in a local subreaper. Its
// private payload group leader is held unreaped until group shutdown, so its
// pgid cannot be recycled while signals are sent. Only this isolated child
// calls wait(-1); it cannot steal another daemon command's wait status.
func PrepareCommandSupervision(cmd *exec.Cmd, grace time.Duration) (*CommandSupervision, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err = unix.SetsockoptInt(sockets[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		unix.Close(sockets[0])
		unix.Close(sockets[1])
		return nil, err
	}
	file := os.NewFile(uintptr(sockets[0]), "supervisor-result-reader")
	writer := os.NewFile(uintptr(sockets[1]), "supervisor-result-writer")
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, writer)
	payload := append([]string(nil), cmd.Args...)
	payload[0] = cmd.Path
	args := append([]string{path, supervisorArg, grace.String() + "," + cmd.WaitDelay.String() + "," + strconv.Itoa(fd)}, payload...)
	cmd.Path = path
	cmd.Args = args
	cmd.WaitDelay = 0 // supervisor owns bounded drain; os/exec must not kill it before it reaps
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	supervision := &CommandSupervision{file: file, writer: writer, cmd: cmd}
	supervision.receive = func() ([]byte, error) { return receiveSupervisorResult(file, cmd) }
	return supervision, nil
}

// receiveSupervisorResult rejects every sender except the exact wrapper. It
// runs after Wait, so only already queued packets are read, nonblocking and
// bounded. Credential spoofing requires CAP_SYS_ADMIN/CAP_SYS_PTRACE; such
// privileged payloads are outside a same-UID process isolation guarantee.
func receiveSupervisorResult(file *os.File, cmd *exec.Cmd) ([]byte, error) {
	var hdr unix.CapUserHeader
	hdr.Version = unix.LINUX_CAPABILITY_VERSION_3
	var caps [2]unix.CapUserData
	if err := unix.Capget(&hdr, &caps[0]); err != nil {
		return nil, fmt.Errorf("cannot establish supervisor proof capability boundary: %w", err)
	}
	// Same-UID payloads inherit the daemon's capability set. With ptrace or
	// credential-spoofing capabilities they can impersonate/control the peer;
	// ordinary execution remains allowed but completion proof fails closed.
	unsafe := uint32(1<<unix.CAP_SYS_ADMIN | 1<<unix.CAP_SYS_PTRACE)
	if (caps[0].Effective|caps[0].Permitted)&unsafe != 0 {
		return nil, fmt.Errorf("supervisor completion unconfirmed: privileged same-UID payload can impersonate peer")
	}
	if cmd.Process == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() < 0 {
		return nil, fmt.Errorf("supervisor did not exit normally; tree shutdown unconfirmed")
	}
	var data [4096]byte
	var oob [256]byte
	for i := 0; i < 16; i++ {
		n, on, flags, _, err := unix.Recvmsg(int(file.Fd()), data[:], oob[:], unix.MSG_DONTWAIT)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("authenticated supervisor result unavailable: %v", err)
		}
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
			return nil, fmt.Errorf("truncated supervisor result")
		}
		messages, err := unix.ParseSocketControlMessage(oob[:on])
		if err != nil {
			return nil, err
		}
		for _, m := range messages {
			cred, e := unix.ParseUnixCredentials(&m)
			if e == nil && int(cred.Pid) == cmd.Process.Pid && int(cred.Uid) == os.Getuid() {
				return append([]byte(nil), data[:n]...), nil
			}
		}
	}
	return nil, fmt.Errorf("supervisor result exceeded unauthenticated packet budget")
}

// supervisorChildren only observes direct children of this isolated process.
// /proc/task/*/children is not available on all sandbox kernels; ppid from
// stat is portable across supported Linux procfs implementations. An unreaped
// direct child cannot have its numeric PID recycled before our wait.
func supervisorChildren() ([]byte, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		end := strings.LastIndexByte(string(b), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(b[end+1:]))
		if len(fields) > 1 && fields[1] == strconv.Itoa(os.Getpid()) {
			ids = append(ids, e.Name())
		}
	}
	return []byte(strings.Join(ids, " ")), nil
}

func superviseCommand(argv []string, grace, drain time.Duration, result *supervisorResult) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Deny same-UID ptrace/proc-fd access to the wrapper before the payload
	// exists. Capable debuggers/root are not an isolation boundary we claim.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return 125, err
	}
	if len(argv) == 0 {
		return 125, fmt.Errorf("missing command")
	}
	// If the daemon disappears unexpectedly, the wrapper receives TERM and
	// still owns bounded descendant cleanup; the restart journal prevents a
	// successor daemon claiming that unknown in-flight command was reaped.
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0, 0, 0); err != nil {
		return 125, err
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return 125, err
	}
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	pid, err := syscall.ForkExec(argv[0], argv, &syscall.ProcAttr{Env: os.Environ(), Files: []uintptr{0, 1, 2}, Sys: &syscall.SysProcAttr{Setpgid: true}})
	if err != nil {
		return 125, err
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	cancelled := false
	implicitDrain := false
	var killAt time.Time
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if err != nil && err != unix.EINTR {
			return 125, err
		}
		// WNOWAIT leaves the payload leader reserved until the group signal.
		exited := info.Signo != 0
		if cancelled && implicitDrain && exited {
			children, e := supervisorChildren()
			if e != nil {
				return 125, e
			}
			if strings.TrimSpace(string(children)) == strconv.Itoa(pid) {
				var ws syscall.WaitStatus
				_, e = syscall.Wait4(pid, &ws, 0, nil)
				if e != nil {
					return 125, e
				}
				if ws.Signaled() {
					return 128 + int(ws.Signal()), nil
				}
				return ws.ExitStatus(), nil
			}
		}
		if !cancelled && exited {
			// No descendants means the command is fully complete: reap the
			// reserved leader and avoid charging normal commands cleanup grace.
			children, e := supervisorChildren()
			if e != nil {
				return 125, e
			}
			if strings.TrimSpace(string(children)) == strconv.Itoa(pid) {
				var ws syscall.WaitStatus
				_, e = syscall.Wait4(pid, &ws, 0, nil)
				if e != nil {
					return 125, e
				}
				if ws.Signaled() {
					return 128 + int(ws.Signal()), nil
				}
				return ws.ExitStatus(), nil
			}
			// Ordinary shell backgrounding is not silently allowed to outlive the
			// run-owned command: drain remaining descendants before reporting done.
			// Preserve the ordinary bounded output-drain window. No cancel
			// was requested, so a short-lived background writer may finish.
			killAt = time.Now().Add(drain)
			cancelled = true
			implicitDrain = true
		}
		if cancelled && !time.Now().Before(killAt) {
			break
		}
		select {
		case <-sigs:
			if implicitDrain {
				implicitDrain = false
				killAt = time.Now().Add(grace)
				_ = syscall.Kill(-pid, syscall.SIGTERM)
			}
			if !cancelled {
				cancelled = true
				killAt = time.Now().Add(grace)
				_ = syscall.Kill(-pid, syscall.SIGTERM)
			}
		case <-tick.C:
		}
	}
	// pid still belongs to our unreaped child. Group kill reaches forks created
	// during grace even when the shell leader exited before them.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return 125, err
	}
	deadline := time.Now().Add(3 * time.Second)
	rootCode := 0
	for {
		var ws syscall.WaitStatus
		got, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err == syscall.ECHILD {
			if implicitDrain {
				fmt.Fprintln(os.Stderr, "command descendants were stopped after shell exit; output collection was cut off")
				result.OutputIncomplete = true
			}
			return rootCode, nil
		}
		if err != nil && err != syscall.EINTR {
			return 125, err
		}
		if got > 0 {
			if got == pid {
				if ws.Signaled() {
					rootCode = 128 + int(ws.Signal())
				} else {
					rootCode = ws.ExitStatus()
				}
			}
			continue
		}
		if time.Now().After(deadline) {
			return 125, fmt.Errorf("descendants did not exit before shutdown deadline")
		}
		// Subreaper-owned escaped descendants are now direct children. Their PIDs
		// cannot recycle before we reap them; never target processes outside this
		// supervisor's child list.
		children, readErr := supervisorChildren()
		if readErr != nil {
			return 125, readErr
		}
		for _, word := range strings.Fields(string(children)) {
			child, e := strconv.Atoi(word)
			if e == nil && child > 0 {
				_ = syscall.Kill(child, syscall.SIGKILL)
			}
		}
		<-tick.C
	}
}
