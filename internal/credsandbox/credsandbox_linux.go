// Copyright (c) 2025 Reliant Labs
//go:build linux

package credsandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	"github.com/reliant-labs/reliant/internal/logging"
)

// Available reports why this machine cannot run the sandbox, or nil. The answer
// is cached for the life of the process.
func Available(self string) error {
	probeOnce.Do(func() {
		probeResult = runHelperProbe(self)
		if probeResult != nil {
			logging.Warn("credsandbox: this machine refused the sandbox, so every credentials-none command is refused "+
				"and credentialed commands get no "+SignalEnv, "reason", probeResult.Error())
		}
	})
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
	if cmd.Env == nil {
		cmd.Env = os.Environ() //nolint:forbidigo // a command with no environment of its own inherits this process's; the helper needs it written down to add its descriptor
	}

	plan, err := prepareOverlays(plan)
	if err != nil {
		return Refusal(err)
	}
	runDir, runDirFile, err := newRunDir()
	if err != nil {
		return Refusal(fmt.Errorf("making the command's run directory: %w", err))
	}
	parentFD, err := unix.Open(filepath.Dir(runDir), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = runDirFile.Close()
		_ = removeTree(runDir)
		return Refusal(fmt.Errorf("opening the run directory's parent: %w", err))
	}
	parentFile := os.NewFile(uintptr(parentFD), filepath.Dir(runDir))
	abandon := func(cause error) string {
		_ = runDirFile.Close()
		_ = parentFile.Close()
		_ = removeTree(runDir)
		return Refusal(cause)
	}
	// From here the run directory is used through its descriptor only.
	ref := "/proc/self/fd/" + strconv.Itoa(int(runDirFile.Fd()))
	if err := makePrivateTmpDirs(ref); err != nil {
		return abandon(fmt.Errorf("making the command's private /tmp: %w", err))
	}
	cmd.Env = privateTmpEnv(cmd.Env)
	if len(plan.Overlay) > 0 {
		if overlayErr := overlayAvailable(); overlayErr != nil {
			var err error
			if plan, cmd.Env, err = withoutOverlays(plan, cmd.Env, ref); err != nil {
				return abandon(err)
			}
		}
	}

	// The run directory and its parent travel as descriptors the helper
	// inherits, after the caller's own ExtraFiles, then the spec's pipe.
	first := 3 + len(cmd.ExtraFiles)
	payload, err := json.Marshal(spec{Plan: plan, Path: cmd.Path, Argv: cmd.Args, Dir: cmd.Dir,
		RunDir: runDir, RunDirFD: first, RunParentFD: first + 1, TmpRoot: os.TempDir()})
	if err != nil {
		return abandon(err)
	}
	// The spec travels on a pipe the helper inherits: not on the command line,
	// where ps would show it, and not in the environment, which the command
	// would inherit.
	r, w, err := os.Pipe()
	if err != nil {
		return abandon(err)
	}
	go func() {
		defer w.Close()
		_, _ = w.Write(payload)
	}()

	cmd.Path = self
	cmd.Args = []string{self, HelperArg}
	cmd.ExtraFiles = append(cmd.ExtraFiles, runDirFile, parentFile, r)
	sandboxFiles.Store(cmd, []*os.File{runDirFile, parentFile, r})
	cmd.Env = append(cmd.Env, specFDEnv+"="+strconv.Itoa(first+2))
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
	// CAP_SETPCAP to empty the bounding set; an overlay mount takes the
	// mounter's file capabilities for its copy-ups and work directory too
	// (they reach only the files of the one id mapped). It drops everything
	// before the command runs.
	a.AmbientCaps = append(a.AmbientCaps, unix.CAP_SYS_ADMIN, unix.CAP_SETPCAP,
		unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH, unix.CAP_FOWNER, unix.CAP_CHOWN, unix.CAP_FSETID)
}

// specFDEnv names the file descriptor the helper reads its spec from. The
// helper removes it from the environment before the command starts.
const specFDEnv = "RELIANT_CRED_SANDBOX_FD"

type spec struct {
	Plan Plan
	Path string
	Argv []string
	Dir  string
	// RunDir holds the upper and work directories of the command's overlays;
	// the helper removes it when the command exits.
	RunDir string `json:",omitempty"`
	// RunDirFD and RunParentFD are the descriptors the helper inherits of the
	// run directory and the directory holding it. The helper reaches the run
	// directory only through RunDirFD, and removes it from RunParentFD only
	// while RunDir's name there is still that directory.
	RunDirFD    int `json:",omitempty"`
	RunParentFD int `json:",omitempty"`
	// TmpRoot is the daemon's temp directory (os.TempDir), which the command's
	// private /tmp covers when it is under /tmp (privateTmp).
	TmpRoot string `json:",omitempty"`
	// ProbeOverlay, in the probe only, is a directory to try an overlay mount
	// in. The helper prints the outcome as its one line of output.
	ProbeOverlay string `json:",omitempty"`
}

var (
	probeOnce     sync.Once
	probeResult   error
	overlayResult error
)

// overlayAvailable reports why the kernel refused the probe's unprivileged
// overlay mount, or nil. Only meaningful once Available returned nil.
func overlayAvailable() error { return overlayResult }

// overlayProbeOK is the probe's output when its overlay mount worked.
const overlayProbeOK = "overlay: ok"

// runHelperProbe runs the helper once on /bin/true with an empty plan, to learn
// whether this machine allows the namespaces at all, and whether it allows an
// overlay mount inside them (the private caches; see withoutOverlays).
func runHelperProbe(self string) error {
	probeDir, err := os.MkdirTemp("", "reliant-credsandbox-probe-")
	if err != nil {
		overlayResult = err
	} else {
		defer func() { _ = removeTree(probeDir) }()
	}
	payload, _ := json.Marshal(spec{Path: "/bin/true", Argv: []string{"true"}, Dir: "/", ProbeOverlay: probeDir})
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
	if overlayResult == nil {
		if got := strings.TrimSpace(out.String()); got != overlayProbeOK {
			overlayResult = errors.New(strings.TrimPrefix(got, "overlay: "))
		}
	}
	if overlayResult != nil {
		logging.Warn("credsandbox: this kernel refused an unprivileged overlay mount, so credentials-none commands "+
			"get private, empty Go caches (seeded from the shared module cache) and the shared tool caches read-only; "+
			"builds in them start cold", "reason", overlayResult.Error())
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

	if s.Dir == "" {
		// The command starts in this init's directory. Named by its path, so
		// the command's chdir resolves through the view built below: a
		// directory inherited as is stays on the mount it was on, and `..`
		// from there walks the filesystem the view covers.
		s.Dir, _ = os.Getwd()
	}
	runDir := "" // as this init reaches it: through its descriptor
	if s.RunDir != "" {
		// The run directory and its parent arrive as descriptors. Close-on-exec
		// first: the command must never inherit a way back into the daemon's
		// /tmp. Then the name RunDir must still be that directory, and the
		// directory still a private one of this user's (newRunDir).
		for _, fd := range []int{s.RunDirFD, s.RunParentFD} {
			if fd < 3 {
				return fail("no run directory descriptor")
			}
			unix.CloseOnExec(fd)
		}
		if err := checkRunDir(s.RunDirFD); err != nil {
			return fail("%v", err)
		}
		name := filepath.Base(s.RunDir)
		if !sameDirAt(s.RunDirFD, s.RunParentFD, name) {
			return fail("the run directory %s is no longer the one the daemon made", s.RunDir)
		}
		// The inherited descriptors belong to the daemon's mount namespace,
		// and the kernel binds nothing from a mount outside this one. So the
		// run directory is opened again here, and used only once it is proved
		// to be the very directory the daemon made and handed over.
		nsFD, err := unix.Open(s.RunDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fail("opening the run directory: %v", err)
		}
		if !sameDir(nsFD, s.RunDirFD) {
			return fail("the run directory %s is no longer the one the daemon made", s.RunDir)
		}
		runDir = "/proc/self/fd/" + strconv.Itoa(nsFD)
		// Removed when the command exits, by this init; the daemon removes the
		// run directory of an init that was killed first (sweepRunDirs), once
		// the process this names is gone. Recorded before the private /proc
		// goes on: there, this process is 1.
		defer removeRunDir(runDir, nsFD, s.RunParentFD, name)
		self, err := os.Readlink("/proc/self")
		if err != nil {
			return fail("reading this init's pid: %v", err)
		}
		if err := writeOwnerAt(s.RunDirFD, self); err != nil {
			return fail("recording the run directory's owner: %v", err)
		}
	}

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fail("making mounts private: %v", err)
	}
	// A /proc of this namespace's own, or the command would see (and read the
	// environment and memory of) every process of the daemon's namespace.
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fail("mounting a private /proc: %v", err)
	}
	if s.ProbeOverlay != "" {
		if err := probeOverlay(s.ProbeOverlay); err != nil {
			fmt.Printf("overlay: %v\n", err)
		} else {
			fmt.Println(overlayProbeOK)
		}
		return 0
	}
	if err := applyPlan(s.Plan, runDir, s.Dir, s.TmpRoot); err != nil {
		return fail("%v", err)
	}
	if err := dropPrivileges(); err != nil {
		return fail("dropping privileges: %v", err)
	}
	code := superviseCommand(s)
	if s.RunDir != "" {
		reportDiscarded(s.Plan, runDir)
	}
	return code
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

	env := withoutSpecFD(os.Environ()) //nolint:forbidigo // hands the command the environment the daemon built for it, minus the helper's own variable
	if s.RunDir != "" {
		// Again here, not only in Apply: the daemon adds variables after it
		// (proctree gives every command a GOTMPDIR of its own, under the
		// machine's /tmp, which the command cannot see).
		env = privateTmpEnv(env)
	}
	pid, err := syscall.ForkExec(s.Path, args, &syscall.ProcAttr{
		Dir:   s.Dir,
		Env:   env,
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

// applyPlan builds the command's view of the filesystem: the hidden paths
// first (they rebuild directories, which would drop a mount made inside them
// earlier), then the read-only paths and the carve-outs inside them, the
// shallowest first, then the private overlays, and last the private /tmp and
// /var/tmp (privateTmp), which cover whatever the steps before did there.
func applyPlan(p Plan, runDir, cwd, tmpRoot string) error {
	mounts, err := readMounts()
	if err != nil {
		return err
	}
	masked, err := hideAndSeal(p, mounts)
	if err != nil {
		return err
	}

	type op struct {
		path     string
		writable bool
	}
	ro := readOnlyPaths(p.ReadOnly, mounts)
	var ops []op
	for _, r := range ro {
		ops = append(ops, op{path: r})
	}
	for _, w := range p.Writable {
		ops = append(ops, op{path: canonical(w), writable: true})
	}
	sort.SliceStable(ops, func(i, j int) bool { return depthOf(ops[i].path) < depthOf(ops[j].path) })
	for _, o := range ops {
		apply := makeReadOnly
		if o.writable {
			apply = makeWritable
		}
		if err := apply(o.path); err != nil {
			return err
		}
	}

	if err := applyOverlays(p, runDir, masked, mounts); err != nil {
		return err
	}
	if runDir == "" {
		return nil
	}
	return privateTmp(runDir, cwd, tmpRoot, mounts)
}

// applyOverlays mounts the private overlays, the shallowest first; one inside
// another is private already and is skipped. Under each, the Passthrough
// directories are bound back as they are, from descriptors taken before the
// overlay covers them. Every other path the overlaid directory has (the disk
// mounted again at /mnt/workspace-disk) is made read-only, or a write there
// would reach the shared directory after all.
//
// An overlay ignores the mounts inside its lower directory, so it would show
// again what the sandbox hid in there (masked: the directories rebuilt without
// an entry, the sockets covered): such a directory is made read-only instead,
// with the Passthrough ones writable inside it.
func applyOverlays(p Plan, runDir string, masked []string, mounts []mountInfo) error {
	type entry struct {
		index int
		path  string
	}
	var entries []entry
	for i, o := range p.Overlay {
		entries = append(entries, entry{i, canonical(o.Path)})
	}
	sort.SliceStable(entries, func(a, b int) bool { return depthOf(entries[a].path) < depthOf(entries[b].path) })
	var done []string
	for _, e := range entries {
		path := e.path
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			continue
		}
		if under(path, done...) {
			continue
		}
		done = append(done, path)
		var pass []string
		for _, w := range p.Passthrough {
			if w = canonical(w); w != path && under(w, path) && !under(w, pass...) {
				pass = append(pass, w)
			}
		}
		readOnly := func(cause error) error {
			// Read-only is the safe half of private: the command cannot plant
			// anything there, it only cannot add to it either.
			if err := makeReadOnly(path); err != nil {
				if cause != nil {
					return fmt.Errorf("overlaying %s: %v; and making it read-only: %w", path, cause, err)
				}
				return err
			}
			for _, w := range pass {
				if _, err := os.Lstat(w); err != nil {
					return &passthroughError{path: w, err: err}
				}
				if err := makeWritable(w); err != nil {
					return err
				}
			}
			return nil
		}
		// The directory's other paths first, before its overlay exists: each
		// gets a read-only bind mount of its own in this namespace, which
		// changes that one mount's flag and nothing else (not the filesystem,
		// not the overlay's lower or upper layer, not any mount outside the
		// namespace). overlayAliases never names path itself.
		for _, alias := range overlayAliases(path, mounts) {
			if err := makeReadOnly(alias); err != nil {
				return err
			}
		}
		hidesSomething := false
		for _, m := range masked {
			if under(m, path) {
				hidesSomething = true
			}
		}
		if runDir == "" || hidesSomething {
			if err := readOnly(nil); err != nil {
				return err
			}
		} else if err := overlayWithPassthrough(path, filepath.Join(runDir, "o"+strconv.Itoa(e.index)), pass); err != nil {
			var pe *passthroughError
			if errors.As(err, &pe) {
				return err
			}
			if err := readOnly(err); err != nil {
				return err
			}
		}
	}
	return nil
}

// passthroughError is a directory the plan keeps real that the sandbox could
// not reach: the command is refused rather than run with it private.
type passthroughError struct {
	path string
	err  error
}

func (e *passthroughError) Error() string {
	return fmt.Sprintf("keeping %s real for the command: %v", e.path, e.err)
}

func (e *passthroughError) Unwrap() error { return e.err }

// overlayAliases are the other paths the directory path has through other
// mounts of its filesystem (aliasPaths), less any that is path itself or holds
// or lies inside it: a mount listed twice at path, or a mount of an enclosing
// directory, would otherwise make the overlay's own path, or its whole tree,
// read-only.
func overlayAliases(path string, mounts []mountInfo) []string {
	var out []string
	for _, a := range dedupe(aliasPaths([]string{path}, mounts)) {
		if under(a, path) || under(path, a) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// overlayWithPassthrough mounts the private overlay on dir and binds each of
// pass, real, back over it.
func overlayWithPassthrough(dir, work string, pass []string) error {
	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	for _, w := range pass {
		// prepareOverlays made every one: one that cannot be opened now is
		// refused, never left private, which would discard what it declared.
		fd, err := unix.Open(w, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return &passthroughError{path: w, err: err}
		}
		fds = append(fds, fd)
	}
	if err := mountOverlay(dir, work); err != nil {
		return err
	}
	for i, w := range pass {
		if err := unix.Mount("/proc/self/fd/"+strconv.Itoa(fds[i]), w, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("keeping %s real inside the private %s: %w", w, dir, err)
		}
	}
	return nil
}

// reportDiscarded tells the command, on its stderr, what it wrote under each
// Notice overlay and lost when it exited. runDir is the run directory as the
// init still reaches it.
func reportDiscarded(p Plan, runDir string) {
	for i, o := range p.Overlay {
		if !o.Notice {
			continue
		}
		upper := filepath.Join(runDir, "o"+strconv.Itoa(i), "u")
		n, first := 0, ""
		_ = filepath.WalkDir(upper, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if n == 0 {
				first, _ = filepath.Rel(upper, path)
			}
			n++
			return nil
		})
		if n == 0 {
			continue
		}
		fmt.Fprintf(os.Stderr, "credsandbox: discarded %d file(s) this credentials-none command wrote under %s (%s, ...): "+
			"a credentials-none command's writes there are its own. A run node keeps a directory there by naming it in writable_checkouts.\n",
			n, o.Path, filepath.Join(o.Path, first))
	}
}

// mountOverlay mounts an overlay on dir whose lower layer is dir itself and
// whose upper layer is under work, so the command reads what dir holds and
// what it writes there lands in work.
func mountOverlay(dir, work string) error {
	if strings.ContainsAny(dir+work, ",:\\") {
		return fmt.Errorf("%s: a path an overlay option cannot name", dir)
	}
	upper, wd := filepath.Join(work, "u"), filepath.Join(work, "w")
	for _, d := range []string{upper, wd} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	opts := "lowerdir=" + dir + ",upperdir=" + upper + ",workdir=" + wd + ",userxattr"
	if err := unix.Mount("overlay", dir, "overlay", 0, opts); err != nil {
		return fmt.Errorf("overlay on %s: %w", dir, err)
	}
	return nil
}

// probeOverlay tries one overlay mount in dir and a write through it.
func probeOverlay(dir string) error {
	l, m := filepath.Join(dir, "l"), filepath.Join(dir, "m")
	for _, d := range []string{l, m} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(l, "f"), []byte("x"), 0o600); err != nil {
		return err
	}
	if err := unix.Mount(l, m, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	if err := mountOverlay(m, filepath.Join(dir, "x")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m, "f"), []byte("y"), 0o600); err != nil {
		return fmt.Errorf("writing through an overlay: %w", err)
	}
	if b, err := os.ReadFile(filepath.Join(l, "f")); err != nil || string(b) != "x" {
		return errors.New("a write through the overlay reached its lower layer")
	}
	return nil
}

// hideAndSeal makes Hide's paths absent and rebuilds the directory above each
// absent Seal path. It returns what it masked: the directories it rebuilt and
// the paths it covered.
func hideAndSeal(p Plan, mounts []mountInfo) ([]string, error) {

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
		if isNullDevice(fi) {
			continue // covered already, by an enclosing sandbox or the host: it hides nothing
		}
		if fi.Mode()&os.ModeSocket != 0 || scratchDirs[parent] {
			cover = append(cover, t)
			continue
		}
		rebuild[parent] = append(rebuild[parent], filepath.Base(t))
	}
	var sealed []string
	for _, s := range p.Seal {
		sealed = append(sealed, canonical(s))
	}
	for _, s := range dedupe(append(sealed, aliasPaths(sealed, mounts)...)) {
		if _, err := os.Lstat(s); err == nil {
			continue // it exists: ReadOnly freezes it
		}
		d := filepath.Dir(s)
		for d != "/" {
			if _, err := os.Lstat(d); err == nil {
				break
			}
			d = filepath.Dir(d)
		}
		// Never "/" or a top-level directory but the home, and never a shared
		// scratch directory (sealable). prepareOverlays refused a plan with
		// one the daemon's user could write (checkSeals).
		if !sealable(d) {
			continue
		}
		if _, ok := rebuild[d]; !ok {
			rebuild[d] = nil
		}
	}
	// A directory holding a link to a trusted path is rebuilt whole: the link
	// is recreated in the tmpfs, so replacing it changes nothing outside the
	// command, and its target is made read-only below.
	for _, d := range dedupe(append(append([]string(nil), p.LinkParents...), aliasPaths(p.LinkParents, mounts)...)) {
		if !sealable(d) {
			continue // checkLinkParents refused one the command could write
		}
		if _, ok := rebuild[d]; !ok {
			rebuild[d] = nil
		}
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
			return nil, err
		}
	}
	for _, c := range cover {
		var err error
		if fi, lerr := os.Lstat(c); lerr == nil && fi.IsDir() {
			// A directory (GH_CONFIG_DIR in /tmp) is covered by an empty,
			// read-only tmpfs: the null device cannot be mounted on one.
			err = unix.Mount("tmpfs", c, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=4k,mode=000")
		} else {
			err = unix.Mount("/dev/null", c, "", unix.MS_BIND, "")
		}
		if err != nil {
			// Gone, or the null device by now (a host without the socket, or
			// one that covered it itself): there is nothing to hide. Anything
			// else still there is refused.
			if fi, lerr := os.Lstat(c); errors.Is(lerr, os.ErrNotExist) || (lerr == nil && isNullDevice(fi)) {
				continue
			}
			return nil, fmt.Errorf("covering %s: %w", c, err)
		}
	}
	return append(parents, cover...), nil
}

// isNullDevice reports whether fi is the null device: something already put
// over a path to hide it.
func isNullDevice(fi os.FileInfo) bool {
	if fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	null, err := os.Stat("/dev/null")
	if !ok || err != nil {
		return false
	}
	nst, ok := null.Sys().(*syscall.Stat_t)
	return ok && st.Rdev == nst.Rdev
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

// readOnlyPaths is every path the read-only entries must be made read-only
// through: each entry as named (canonical), what it resolves to when it is a
// link, and the paths both have through other mounts of their filesystem.
// makeReadOnly follows a link to its target, so the target is what a write
// reaches: its aliases are computed from its own path, not the link's (a
// ~/.config/git/config linked to /srv/git.conf is also
// /mnt/workspace-disk/srv/git.conf when /srv is a mount of that disk). The
// link's own spelling and its aliases stay, as before; its directory is
// rebuilt as a LinkParent (hideAndSeal).
func readOnlyPaths(readOnly []string, mounts []mountInfo) []string {
	var ro []string
	for _, r := range readOnly {
		c := canonical(r)
		ro = append(ro, c)
		if real, err := filepath.EvalSymlinks(c); err == nil && real != c {
			ro = append(ro, real)
		}
	}
	return dedupe(append(ro, aliasPaths(ro, mounts)...))
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
	// Every mount at or below path, not only the top one: a directory the
	// sandbox rebuilt to hide an entry (~/.config/forge without its
	// credentials file) is a tmpfs holding a bind mount per entry, and a bind
	// is read-only only when it is remounted so itself. The last entry for a
	// mount point is the one on top, which the remount reaches.
	options := map[string]string{}
	var points []string
	for _, m := range mounts {
		if !under(m.mountPoint, path) {
			continue
		}
		if _, seen := options[m.mountPoint]; !seen {
			points = append(points, m.mountPoint)
		}
		options[m.mountPoint] = m.options
	}
	if len(points) == 0 {
		points, options[path] = []string{path}, ""
	}
	for _, mp := range points {
		flags := uintptr(unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY) | mountFlags(options[mp])
		if err := unix.Mount("", mp, "", flags, ""); err != nil {
			return fmt.Errorf("remounting %s read-only: %w", mp, err)
		}
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
