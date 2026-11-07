// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The shared caches (Plan.Overlay) are private to each credentials-none
// command: an overlay whose lower layer is the shared directory and whose upper
// layer is the command's run directory, removed when it exits. The command
// reads a warm cache and its writes reach no one else, so nothing it plants in
// the Go build cache, the module cache or a tool cache is ever read by a later,
// credentialed build. What it costs: a credentials-none build adds nothing to
// the shared cache, so its second run rebuilds what its first one did, where
// the shared cache did not have it already.

// runDirPrefix names a command's run directory under the temp directory.
const runDirPrefix = "reliant-credsandbox-run-"

// runDirOwnerFile, inside a run directory, names the init that uses it: its
// pid in the daemon's PID namespace and its start time.
const runDirOwnerFile = "owner"

// staleRunDirAge is how long a run directory with no owner is left alone: the
// window between the daemon making it and the init recording itself.
const staleRunDirAge = 10 * time.Minute

var sweepState struct {
	sync.Mutex
	last time.Time
}

// prepareOverlays drops the overlays that would do nothing and creates the
// missing caches, so a command cannot be the one to create them. A PATH
// directory the daemon's user cannot write needs no overlay; one under home
// that does not exist yet is sealed instead. It also creates the hooks
// directory of a shared git directory the command may write, so the command
// cannot create it.
func prepareOverlays(p Plan) Plan {
	var keep []Overlay
	for _, o := range p.Overlay {
		fi, err := os.Stat(o.Path)
		if err != nil && o.Cache {
			if mkErr := os.MkdirAll(o.Path, 0o755); mkErr == nil {
				fi, err = os.Stat(o.Path)
			}
		}
		switch {
		case err != nil:
			if o.Sealed {
				p.Seal = append(p.Seal, o.Path)
			}
			continue
		case !fi.IsDir():
			continue
		case !o.Cache && unix.Access(o.Path, unix.W_OK) != nil:
			continue
		}
		keep = append(keep, o)
	}
	p.Overlay = keep

	writable := map[string]bool{}
	for _, w := range p.Writable {
		writable[w] = true
	}
	for _, r := range p.ReadOnly {
		if filepath.Base(r) == "hooks" && writable[filepath.Dir(r)] {
			_ = os.MkdirAll(r, 0o755)
		}
	}
	return p
}

// withoutOverlays is the plan where the kernel allows no unprivileged overlay:
// every overlaid directory is read-only, and each one a variable names points
// at a private, empty directory in the command's private /tmp instead. The
// module cache is seeded: its downloads are the first GOPROXY, so a module the
// shared cache holds is extracted rather than fetched, and still checked
// against go.sum.
func withoutOverlays(p Plan, env []string, runDir string) (Plan, []string) {
	for _, o := range p.Overlay {
		p.ReadOnly = append(p.ReadOnly, o.Path)
		if o.Env == "" {
			continue
		}
		host, private := privateTmpPath(runDir, ".credsandbox-"+strings.ToLower(o.Env))
		if err := os.MkdirAll(host, 0o700); err != nil {
			continue
		}
		env = setEnv(env, o.Env, private)
		if o.Env == "GOMODCACHE" {
			// A private module cache is removed with the run directory, which
			// needs its read-only module directories writable.
			env = setEnv(env, "GOFLAGS", strings.TrimSpace(envValue(env, "GOFLAGS")+" -modcacherw"))
			proxy := envValue(env, "GOPROXY")
			if proxy == "" {
				proxy = "https://proxy.golang.org,direct"
			}
			env = setEnv(env, "GOPROXY", "file://"+filepath.Join(o.Path, "cache", "download")+","+proxy)
		}
	}
	p.ReadOnly = cleanAbs(p.ReadOnly)
	p.Overlay = nil
	return p, env
}

func setEnv(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return append(out, name+"="+value)
}

// newRunDir makes a command's run directory, and first removes those whose
// init is gone (at most once every few minutes).
func newRunDir() (string, error) {
	sweepState.Lock()
	if time.Since(sweepState.last) > staleRunDirAge/2 {
		sweepState.last = time.Now()
		sweepRunDirs(os.TempDir())
	}
	sweepState.Unlock()
	return os.MkdirTemp("", runDirPrefix)
}

// sweepRunDirs removes the run directories under tmp whose init is not
// running: one that was killed before it could remove its own.
func sweepRunDirs(tmp string) {
	matches, _ := filepath.Glob(filepath.Join(tmp, runDirPrefix+"*"))
	for _, dir := range matches {
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || !ownedByUs(fi) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, runDirOwnerFile))
		if err != nil {
			if time.Since(fi.ModTime()) > staleRunDirAge {
				_ = removeTree(dir)
			}
			continue
		}
		pid, start, ok := strings.Cut(strings.TrimSpace(string(raw)), " ")
		if ok && processStart(pid) == start {
			continue // its init is still running
		}
		_ = removeTree(dir)
	}
}

func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*unix.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}

// writeOwner records the calling process in runDir. It must run before the
// process mounts its own /proc, where it is pid 1.
func writeOwner(runDir string) error {
	self, err := os.Readlink("/proc/self")
	if err != nil {
		return err
	}
	start := processStart(self)
	if start == "" {
		return fmt.Errorf("reading the start time of pid %s", self)
	}
	return os.WriteFile(filepath.Join(runDir, runDirOwnerFile), []byte(self+" "+start+"\n"), 0o600)
}

// processStart is the start time of pid (/proc/<pid>/stat, field 22), or "".
func processStart(pid string) string {
	if _, err := strconv.Atoi(pid); err != nil {
		return ""
	}
	raw, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return ""
	}
	// The command name (field 2) is parenthesized and may hold spaces.
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return ""
	}
	return fields[19] // field 22 counts from 1 and from before the name
}

// removeRunDir is the init's removal of its run directory, as the command
// exits. What the sandbox mounted on stays until the namespace is gone (the
// directory carried into the private /tmp: the kernel refuses to remove a mount
// point), so the owner file goes last, and only once everything else did:
// until then it tells sweepRunDirs the init is gone, which removes the rest.
func removeRunDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	clean := true
	for _, e := range entries {
		if e.Name() == runDirOwnerFile {
			continue
		}
		if err := removeTree(filepath.Join(dir, e.Name())); err != nil {
			clean = false
		}
	}
	if clean {
		_ = removeTree(dir)
	}
}

// removeTree removes dir and everything below it, including the directories an
// overlay's work layer leaves with no permissions, without following a link.
func removeTree(dir string) error {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || !d.IsDir() {
			return nil
		}
		fd, openErr := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			fd, openErr = unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if openErr != nil {
				return nil
			}
			// fchmod needs a real descriptor; an O_PATH one goes through /proc.
			_ = unix.Chmod("/proc/self/fd/"+strconv.Itoa(fd), 0o700)
			_ = unix.Close(fd)
			return nil
		}
		_ = unix.Fchmod(fd, 0o700)
		_ = unix.Close(fd)
		return nil
	})
	return os.RemoveAll(dir)
}

// makeWritable makes path, inside a read-only bind mount, writable again with
// a bind mount of its own (one the sandbox made, so its read-only flag is not
// locked).
func makeWritable(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if err := unix.Mount(path, path, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("binding %s writable: %w", path, err)
	}
	mounts, err := readMounts()
	if err != nil {
		return err
	}
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT)
	for i := len(mounts) - 1; i >= 0; i-- {
		if mounts[i].mountPoint == path {
			flags |= mountFlags(mounts[i].options)
			break
		}
	}
	if err := unix.Mount("", path, "", flags, ""); err != nil {
		return fmt.Errorf("remounting %s writable: %w", path, err)
	}
	return nil
}
