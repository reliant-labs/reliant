// Copyright (c) 2025 Reliant Labs
//go:build linux

package credsandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Available reports why this machine cannot run the sandbox, or nil. The answer
// is cached for the life of the process.
func Available(self string) error {
	probeOnce.Do(func() { probeResult = runHelperProbe(self) })
	return probeResult
}

// Apply rewrites cmd, which has not been started, to run inside the sandbox:
// cmd runs the binary at self as HelperArg, which builds the namespaces and then
// execs the original command with its path, arguments, directory and
// environment unchanged. A non-empty refusal means the command must not run;
// the caller reports it as the command's output.
//
// self is the program that dispatches HelperArg: os.Executable() in the daemon,
// the test binary's own path in a test.
func Apply(cmd *exec.Cmd, self string, plan Plan) (refusal string) {
	if !Enabled() {
		return ""
	}
	if cmd == nil || cmd.Process != nil || cmd.Err != nil || len(cmd.Args) == 0 {
		return ""
	}
	if self == "" {
		return Refusal(errors.New("the sandbox helper binary was not found"))
	}
	if _, err := os.Stat(self); err != nil {
		return Refusal(fmt.Errorf("the sandbox helper binary is gone: %w", err))
	}
	if err := Available(self); err != nil {
		return Refusal(err)
	}

	payload, err := json.Marshal(spec{Plan: plan, Path: cmd.Path, Argv: cmd.Args, Dir: cmd.Dir})
	if err != nil {
		return Refusal(err)
	}
	// The spec travels on a pipe the helper inherits: not on the command line,
	// where ps would show it, and not in the environment, which the command
	// would inherit.
	r, w, err := os.Pipe()
	if err != nil {
		return Refusal(err)
	}
	go func() {
		defer w.Close()
		_, _ = w.Write(payload)
	}()

	cmd.Path = self
	cmd.Args = []string{self, HelperArg}
	cmd.ExtraFiles = append(cmd.ExtraFiles, r)
	if cmd.Env == nil {
		cmd.Env = os.Environ() //nolint:forbidigo // a command with no environment of its own inherits this process's; the helper needs it written down to add its descriptor
	}
	cmd.Env = append(cmd.Env, specFDEnv+"="+strconv.Itoa(3+len(cmd.ExtraFiles)-1))
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	namespaces(cmd.SysProcAttr)
	return ""
}

func namespaces(a *syscall.SysProcAttr) {
	a.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID
	// One id maps to itself, so files keep their owner and the command keeps the
	// daemon's uid; no id is added, so there is nothing to escalate to.
	a.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
	a.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
	a.GidMappingsEnableSetgroups = false
	// The helper needs CAP_SYS_ADMIN inside its own user namespace to mount, and
	// CAP_SETPCAP to empty the bounding set; it drops everything before the
	// command runs.
	a.AmbientCaps = append(a.AmbientCaps, unix.CAP_SYS_ADMIN, unix.CAP_SETPCAP)
}

// specFDEnv names the file descriptor the helper reads its spec from. The
// helper removes it from the environment before the command starts.
const specFDEnv = "RELIANT_CRED_SANDBOX_FD"

type spec struct {
	Plan Plan
	Path string
	Argv []string
	Dir  string
}

var (
	probeOnce   sync.Once
	probeResult error
)

// runHelperProbe runs the helper once on /bin/true with an empty plan, to learn
// whether this machine allows the namespaces at all.
func runHelperProbe(self string) error {
	payload, _ := json.Marshal(spec{Path: "/bin/true", Argv: []string{"true"}, Dir: "/"})
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	_, _ = w.Write(payload)
	_ = w.Close()

	cmd := exec.Command(self)
	cmd.Args = []string{self, HelperArg}
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = []string{"PATH=/usr/bin:/bin", specFDEnv + "=3"}
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	namespaces(cmd.SysProcAttr)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	_ = r.Close()
	if err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// Dispatch runs this process as the sandbox helper and exits when its first
// argument is HelperArg, and returns at once otherwise. A program that runs
// credentials-none commands calls it first, before anything else.
func Dispatch() {
	dispatched.Store(true)
	if len(os.Args) < 2 || os.Args[1] != HelperArg {
		return
	}
	os.Exit(runHelper())
}

// runHelper is the sandbox's init (PID 1 of the new PID namespace). It reads the
// spec, rebuilds the filesystem view, drops every privilege, and runs the
// command as its only child until it exits.
func runHelper() int {
	// Capabilities, no_new_privs and the bounding set belong to a thread, and the
	// command inherits them from the thread that forks it: every step below, and
	// the fork, stay on this one.
	runtime.LockOSThread()

	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "credsandbox: "+format+"\n", a...)
		return RefusedExitCode
	}

	fd, err := strconv.Atoi(os.Getenv(specFDEnv)) //nolint:forbidigo // the helper's private channel from its parent; not configuration
	if err != nil || fd < 3 {
		return fail("no spec")
	}
	f := os.NewFile(uintptr(fd), "spec")
	raw, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return fail("reading spec: %v", err)
	}
	var s spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return fail("decoding spec: %v", err)
	}
	if os.Getpid() != 1 {
		return fail("not the init of a PID namespace (pid %d)", os.Getpid())
	}

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fail("making mounts private: %v", err)
	}
	// A /proc of this namespace's own, or the command would see (and read the
	// environment and memory of) every process of the daemon's namespace.
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fail("mounting a private /proc: %v", err)
	}
	if err := applyPlan(s.Plan); err != nil {
		return fail("%v", err)
	}
	if err := dropPrivileges(); err != nil {
		return fail("dropping privileges: %v", err)
	}
	return superviseCommand(s)
}

// superviseCommand starts the command as the init's only child, forwards the
// signals aimed at the init to the command's process group, reaps orphans, and
// returns the command's status.
func superviseCommand(s spec) int {
	args := s.Argv
	if len(args) == 0 {
		args = []string{s.Path}
	}
	// Subscribed before the command exists: PID 1 ignores a signal it has no
	// handler for, so a SIGTERM from the daemon's timeout would otherwise be lost.
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2)

	pid, err := syscall.ForkExec(s.Path, args, &syscall.ProcAttr{
		Dir:   s.Dir,
		Env:   withoutSpecFD(os.Environ()), //nolint:forbidigo // hands the command the environment the daemon built for it, minus the helper's own variable
		Files: []uintptr{0, 1, 2},
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "credsandbox: starting %s: %v\n", s.Path, err)
		if errors.Is(err, syscall.ENOENT) {
			return 127
		}
		return RefusedExitCode
	}
	go func() {
		for sig := range sigs {
			// ESRCH: the command is already gone and about to be reaped.
			_ = syscall.Kill(-pid, sig.(syscall.Signal))
		}
	}()
	return reapUntil(pid)
}

func withoutSpecFD(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, specFDEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// scratchDirs are shared directories other processes create files in all the
// time. A file hidden inside one is covered in place; rebuilding the directory
// would make everything the command created there vanish when it exits.
var scratchDirs = map[string]bool{"/tmp": true, "/var/tmp": true, "/dev/shm": true, "/run": true}

// applyPlan builds the command's view of the filesystem.
func applyPlan(p Plan) error {
	mounts, err := readMounts()
	if err != nil {
		return err
	}

	// Every spelling the same thing has: its real path, and its path through
	// every other mount of the same filesystem. The workspace disk is mounted at
	// /home/workspace and again at /mnt/workspace-disk, so a file hidden under
	// one is otherwise one `cat` away under the other.
	var targets []string
	for _, h := range p.Hide {
		targets = append(targets, canonical(h))
	}
	targets = dedupe(append(targets, aliasPaths(targets, mounts)...))

	rebuild := map[string][]string{}
	var cover []string
	for _, t := range targets {
		fi, err := os.Lstat(t)
		if err != nil {
			continue // nothing there to hide
		}
		parent := filepath.Dir(t)
		if fi.Mode()&os.ModeSocket != 0 || scratchDirs[parent] {
			cover = append(cover, t)
			continue
		}
		rebuild[parent] = append(rebuild[parent], filepath.Base(t))
	}

	// Shallowest first: a directory rebuilt inside one that was already rebuilt
	// is rebuilt from the bind mounts the outer rebuild made.
	parents := make([]string, 0, len(rebuild))
	for d := range rebuild {
		parents = append(parents, d)
	}
	sort.Slice(parents, func(i, j int) bool { return len(parents[i]) < len(parents[j]) })
	for _, d := range parents {
		if err := rebuildWithout(d, rebuild[d]); err != nil {
			return err
		}
	}
	for _, c := range cover {
		if err := unix.Mount("/dev/null", c, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("covering %s: %w", c, err)
		}
	}
	for _, ro := range p.ReadOnly {
		if err := makeReadOnly(canonical(ro)); err != nil {
			return err
		}
	}
	return nil
}

// canonical resolves the symlinks in path's directory (a hidden file named
// through /var/run is /run's), but not path itself: a link that is the thing to
// hide is hidden, not what it points at.
func canonical(path string) string {
	path = filepath.Clean(path)
	dir, base := filepath.Split(path)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(real, base)
	}
	return path
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// rebuildWithout replaces directory dir with a tmpfs holding a bind mount of
// every entry of the original except names. The entries are absent, not covered,
// so a file the daemon replaces by rename later does not appear through a mount
// that still pointed at the old one.
func rebuildWithout(dir string, names []string) error {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil
	}
	// Opened before the cover goes on: /proc/self/fd/N keeps naming the original
	// directory after dir shows the tmpfs.
	src, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", dir, err)
	}
	defer func() { _ = unix.Close(src) }()
	orig := "/proc/self/fd/" + strconv.Itoa(src)

	entries, err := os.ReadDir(orig)
	if err != nil {
		return fmt.Errorf("listing %s: %w", dir, err)
	}
	if err := unix.Mount("tmpfs", dir, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=32m,mode=0755"); err != nil {
		return fmt.Errorf("masking %s: %w", dir, err)
	}
	hidden := map[string]bool{}
	for _, n := range names {
		hidden[n] = true
	}
	for _, e := range entries {
		if hidden[e.Name()] {
			continue
		}
		from := filepath.Join(orig, e.Name())
		to := filepath.Join(dir, e.Name())
		info, err := os.Lstat(from)
		if err != nil {
			continue
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(from)
			if err != nil {
				continue
			}
			if err := os.Symlink(target, to); err != nil {
				return fmt.Errorf("recreating link %s: %w", to, err)
			}
		case info.IsDir():
			if err := os.Mkdir(to, 0o755); err != nil {
				return fmt.Errorf("recreating %s: %w", to, err)
			}
			if err := unix.Mount(from, to, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
				return fmt.Errorf("binding %s: %w", to, err)
			}
		default:
			f, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("recreating %s: %w", to, err)
			}
			_ = f.Close()
			if err := unix.Mount(from, to, "", unix.MS_BIND, ""); err != nil {
				return fmt.Errorf("binding %s: %w", to, err)
			}
		}
	}
	return nil
}

// makeReadOnly remounts path read-only, keeping the flags its mount already has
// (the kernel refuses to loosen a flag a user namespace inherited). A path that
// does not exist is skipped.
func makeReadOnly(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if err := unix.Mount(path, path, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("binding %s read-only: %w", path, err)
	}
	mounts, err := readMounts()
	if err != nil {
		return err
	}
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY)
	for i := len(mounts) - 1; i >= 0; i-- {
		if mounts[i].mountPoint == path {
			flags |= mountFlags(mounts[i].options)
			break
		}
	}
	if err := unix.Mount("", path, "", flags, ""); err != nil {
		return fmt.Errorf("remounting %s read-only: %w", path, err)
	}
	return nil
}

func mountFlags(options string) uintptr {
	var f uintptr
	for _, o := range strings.Split(options, ",") {
		switch o {
		case "nosuid":
			f |= unix.MS_NOSUID
		case "nodev":
			f |= unix.MS_NODEV
		case "noexec":
			f |= unix.MS_NOEXEC
		case "noatime":
			f |= unix.MS_NOATIME
		case "nodiratime":
			f |= unix.MS_NODIRATIME
		case "relatime":
			f |= unix.MS_RELATIME
		}
	}
	return f
}

type mountInfo struct {
	devMajMin  string
	root       string
	mountPoint string
	options    string
}

// readMounts is the namespace's mount table, in mount order.
func readMounts() ([]mountInfo, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("reading the mount table: %w", err)
	}
	return parseMounts(string(raw)), nil
}

func parseMounts(raw string) []mountInfo {
	var out []mountInfo
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		out = append(out, mountInfo{
			devMajMin:  f[2],
			root:       unescapeMount(f[3]),
			mountPoint: unescapeMount(f[4]),
			options:    f[5],
		})
	}
	return out
}

// unescapeMount undoes mountinfo's \040-style octal escapes.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// aliasPaths returns, for each path, the other paths the same file or directory
// has through other mounts of the same filesystem: a path under mount M (root R)
// is at offset (path - M.mountPoint) from R inside the filesystem, which every
// other mount M2 of the device shows at M2.mountPoint + (offset - M2.root) when
// M2's root contains it.
func aliasPaths(paths []string, mounts []mountInfo) []string {
	var out []string
	for _, p := range paths {
		m, ok := owningMount(p, mounts)
		if !ok {
			continue
		}
		inFS := filepath.Join(m.root, strings.TrimPrefix(p, strings.TrimSuffix(m.mountPoint, "/")))
		for _, o := range mounts {
			if o.devMajMin != m.devMajMin || o == m {
				continue
			}
			rootPrefix := strings.TrimSuffix(o.root, "/")
			if inFS != o.root && !strings.HasPrefix(inFS, rootPrefix+"/") {
				continue
			}
			out = append(out, filepath.Join(o.mountPoint, strings.TrimPrefix(inFS, rootPrefix)))
		}
	}
	return out
}

// owningMount is the topmost mount with the longest mount point that contains p.
func owningMount(p string, mounts []mountInfo) (mountInfo, bool) {
	var best mountInfo
	found := false
	for _, m := range mounts {
		mp := strings.TrimSuffix(m.mountPoint, "/")
		if p != m.mountPoint && !strings.HasPrefix(p, mp+"/") {
			continue
		}
		if !found || len(m.mountPoint) >= len(best.mountPoint) {
			best, found = m, true
		}
	}
	return best, found
}

// dropPrivileges removes every capability and the ability to gain one, and makes
// the init unreadable through /proc and ptrace to the command it starts.
func dropPrivileges() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("making the init undumpable: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("clearing ambient capabilities: %w", err)
	}
	for c := 0; c <= lastCap(); c++ {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(c), 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("dropping capability %d from the bounding set: %w", c, err)
		}
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("clearing capabilities: %w", err)
	}
	return nil
}

func lastCap() int {
	raw, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return 63
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 63
	}
	return n
}

// reapUntil waits for children until pid exits, collecting every orphan the
// kernel reparents to the init, and returns the status the init exits with.
func reapUntil(pid int) int {
	for {
		var ws syscall.WaitStatus
		got, err := syscall.Wait4(-1, &ws, 0, nil)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case err != nil:
			return 1
		case got == pid:
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
	}
}
