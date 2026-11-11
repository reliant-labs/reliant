// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
//
// Measured on a cloud machine (kernel 6.18, 31 CPUs, this repository's warm
// shared cache), `go build -o /dev/null`, seconds:
//
//	                         credentialed  overlay (warm)  no overlay (cold)
//	./internal/credsandbox        1.8–2.1        2.9–3.7              93
//	./internal/daemon             1.1–1.6        1.9–2.4             131
//	./cmd/reliant (links)          48–56          43–44                —
//
// So the overlay costs under a second a command (the namespaces and the
// overlay mounts; noise next to a link), and the fallback a cold build every
// command. That is why the overlay is the default and the empty private caches
// only where the kernel refuses it, which the daemon logs once.

// runDirPrefix names a command's run directory under the temp directory.
const runDirPrefix = "reliant-credsandbox-run-"

// runDirOwnerFile, inside a run directory, names the process that owns it:
// the daemon that made it, then the init that uses it. Its pid in the
// daemon's PID namespace and its start time.
const runDirOwnerFile = "owner"

// sweepInterval is how often newRunDir looks for run directories whose owner
// is gone.
const sweepInterval = 5 * time.Minute

var sweepState struct {
	sync.Mutex
	last time.Time
}

// prepareOverlays drops the overlays that would do nothing and creates the
// missing caches, so a command cannot be the one to create them. A PATH
// directory the daemon's user cannot write needs no overlay and is made
// read-only instead; one that does not exist yet is sealed. It also creates the hand-off
// directories that stay real inside an overlay, and the hooks directory of a
// shared git directory the command may write, so the command cannot create
// either. Anything it cannot make is an error: the command would run with
// less protection, or less than it declared, than its plan says.
func prepareOverlays(p Plan) (Plan, error) {
	var keep []Overlay
	for _, o := range p.Overlay {
		fi, err := os.Stat(o.Path)
		if err != nil && o.Cache {
			if mkErr := os.MkdirAll(o.Path, 0o755); mkErr != nil {
				return p, fmt.Errorf("making the cache %s the command reads privately: %w", o.Path, mkErr)
			}
			fi, err = os.Stat(o.Path)
		}
		switch {
		case err != nil && o.Cache:
			return p, fmt.Errorf("the cache %s: %w", o.Path, err)
		case err != nil:
			// A PATH directory that does not exist yet, wherever it is: a
			// command that made it could leave a program there that a later
			// command runs. Sealed (checkSeals refuses one that cannot be).
			p.Seal = append(p.Seal, o.Path)
			continue
		case !fi.IsDir() && o.Cache:
			return p, fmt.Errorf("the cache %s is not a directory", o.Path)
		case !fi.IsDir():
			// A PATH entry that is a file: read-only, so it cannot be replaced
			// by a directory of programs.
			p.ReadOnly = append(p.ReadOnly, o.Path)
			continue
		case !o.Cache && unix.Access(o.Path, unix.W_OK) != nil:
			// A PATH directory the daemon's user cannot write needs no
			// overlay, but its files may still be the user's to overwrite
			// in place (a 0555 directory of 0755 programs), and the user
			// may chmod a directory it owns. Read-only, recursively.
			p.ReadOnly = append(p.ReadOnly, o.Path)
			continue
		}
		keep = append(keep, o)
	}
	p.Overlay = keep

	// A hand-off directory the run node named, made now if it is missing:
	// made by the command, inside the overlay, it would be discarded. Made
	// from its cache root down without following a link, so a link planted
	// on the way cannot send it elsewhere.
	for _, w := range p.Passthrough {
		root := ""
		for _, o := range p.Overlay {
			if w != o.Path && under(w, o.Path) && len(o.Path) > len(root) {
				root = o.Path
			}
		}
		if root == "" {
			continue // its root is read-only or the command's own: nothing to keep real
		}
		if err := mkdirBeneath(root, w); err != nil {
			return p, fmt.Errorf("making the hand-off directory %s: %w", w, err)
		}
	}

	if err := checkSeals(p.Seal); err != nil {
		return p, err
	}
	if err := checkLinkParents(p.LinkParents); err != nil {
		return p, err
	}

	writable := map[string]bool{}
	for _, w := range p.Writable {
		writable[w] = true
	}
	for _, r := range p.ReadOnly {
		if filepath.Base(r) == "hooks" && writable[filepath.Dir(r)] {
			if err := os.MkdirAll(r, 0o755); err != nil {
				return p, fmt.Errorf("making %s, which the command must not create: %w", r, err)
			}
		}
	}
	return p, nil
}

// checkSeals refuses a plan with a path to seal that the sandbox could not
// seal (hideAndSeal rebuilds the nearest directory above a missing one, but
// never "/", a top-level directory or a shared scratch directory) while the
// daemon's user could create it there: the command would be free to make it.
// One under /tmp or /var/tmp is private to the command anyway.
func checkSeals(seal []string) error {
	for _, s := range seal {
		s = canonical(s)
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
		if under(d, "/tmp", "/var/tmp") || unix.Access(d, unix.W_OK) != nil {
			continue
		}
		if !sealable(d) {
			return fmt.Errorf("%s must not come to exist, and its directory %s cannot be sealed: point the variable that names it somewhere deeper", s, d)
		}
	}
	return nil
}

// checkLinkParents refuses a plan with a directory holding a link to a trusted
// path that the sandbox cannot rebuild (sealable) while the daemon's user can
// write it: the command could replace the link for real.
func checkLinkParents(dirs []string) error {
	for _, d := range dirs {
		if under(d, "/tmp", "/var/tmp") || unix.Access(d, unix.W_OK) != nil || sealable(d) {
			continue
		}
		return fmt.Errorf("%s holds a link to a file a credentialed git trusts, and it cannot be sealed: replace the link with the file it names", d)
	}
	return nil
}

// sealable reports whether hideAndSeal may rebuild directory d: never "/", a
// top-level directory (the user's home, /root for a daemon run as root,
// excepted) or a shared scratch directory.
func sealable(d string) bool {
	if d == "/" || scratchDirs[d] {
		return false
	}
	if depthOf(d) >= 2 {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && home != "" && canonicalDir(home) == d
}

// mkdirBeneath makes dir, inside root, one component at a time from root and
// never through a link: every component is opened O_NOFOLLOW.
func mkdirBeneath(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is not inside %s", dir, root)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			if mkErr := unix.Mkdirat(fd, name, 0o755); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return mkErr
			}
			next, err = unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fd = next
	}
	return unix.Close(fd)
}

// withoutOverlays is the plan where the kernel allows no unprivileged overlay:
// every overlaid directory is read-only, and each one a variable names points
// at a private, empty directory in the command's private /tmp instead. The
// module cache is seeded: its downloads are the first GOPROXY, so a module the
// shared cache holds is extracted rather than fetched, and still checked
// against go.sum.
//
// A private directory it cannot make is an error, never a variable left
// pointing at the shared cache.
func withoutOverlays(p Plan, env []string, runDir string) (Plan, []string, error) {
	for _, o := range p.Overlay {
		p.ReadOnly = append(p.ReadOnly, o.Path)
		if o.Env == "" {
			continue
		}
		host, private := privateTmpPath(runDir, ".credsandbox-"+strings.ToLower(o.Env))
		if err := os.MkdirAll(host, 0o700); err != nil {
			return p, env, fmt.Errorf("making the private %s for %s: %w", o.Env, o.Path, err)
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
	// What stays real inside an overlay is writable inside the read-only
	// directory instead.
	p.Writable = onlyUnderReadOnly(cleanAbs(append(p.Writable, p.Passthrough...)), p.ReadOnly)
	p.Overlay = nil
	return p, env, nil
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

// newRunDir makes a command's run directory and records the daemon as its
// owner before anything else can see it ownerless, and returns it open: from
// here on it is used through the descriptor, never re-resolved by name (the
// init receives the descriptor too). It first removes the run directories
// whose owner is gone (at most once every few minutes).
func newRunDir() (string, *os.File, error) {
	sweepState.Lock()
	if time.Since(sweepState.last) > sweepInterval {
		sweepState.last = time.Now()
		sweepRunDirs(os.TempDir())
	}
	sweepState.Unlock()
	dir, err := os.MkdirTemp("", runDirPrefix)
	if err != nil {
		return "", nil, err
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	f := os.NewFile(uintptr(fd), dir)
	if err := checkRunDir(fd); err != nil {
		_ = f.Close()
		return "", nil, err
	}
	if err := writeOwnerAt(fd, strconv.Itoa(os.Getpid())); err != nil {
		_ = f.Close()
		_ = removeTree(dir)
		return "", nil, fmt.Errorf("recording the run directory's owner: %w", err)
	}
	return dir, f, nil
}

// checkRunDir verifies that fd is a directory of this user's that no one else
// can enter: what MkdirTemp made, not something put in its place.
func checkRunDir(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Getuid() || st.Mode&0o077 != 0 {
		return fmt.Errorf("the run directory is not a private directory of this user (mode %o, uid %d)", st.Mode, st.Uid)
	}
	return nil
}

// sweepRunDirs removes the run directories under tmp whose owner is gone: the
// daemon that made one (until its init records itself) or the init that used
// it. A run directory with no owner record is left alone, whatever its age:
// newRunDir writes the record before returning, so one without it is in the
// middle of being made, and age says nothing about that.
func sweepRunDirs(tmp string) {
	matches, _ := filepath.Glob(filepath.Join(tmp, runDirPrefix+"*"))
	for _, dir := range matches {
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || !ownedByUs(fi) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, runDirOwnerFile))
		if err != nil {
			continue
		}
		pid, start, ok := strings.Cut(strings.TrimSpace(string(raw)), " ")
		if !ok || processStart(pid) == start {
			continue // its owner is still running, or the record is not one we wrote
		}
		_ = removeTree(dir)
	}
}

// ownedByUs reports whether fi is this user's. A FileInfo without an owner
// (never, from os.Lstat on Linux) is not.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// writeOwnerAt records pid, as this namespace's /proc names it, and its start
// time in the run directory open as dirFD, replacing any record (by rename,
// so a sweeper never reads half of one).
func writeOwnerAt(dirFD int, pid string) error {
	start := processStart(pid)
	if start == "" {
		return fmt.Errorf("reading the start time of pid %s", pid)
	}
	tmp := runDirOwnerFile + ".new"
	_ = unix.Unlinkat(dirFD, tmp, 0)
	fd, err := unix.Openat(dirFD, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	_, werr := unix.Write(fd, []byte(pid+" "+start+"\n"))
	cerr := unix.Close(fd)
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return unix.Renameat(dirFD, tmp, dirFD, runDirOwnerFile)
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
// exits: dir names it through the init's descriptor of it (runDirFD), and the
// directory itself is removed from parentFD by name only while that name is
// still the same directory. What the sandbox mounted on stays until the
// namespace is gone (the directory carried into the private /tmp: the kernel
// refuses to remove a mount point), so the owner file goes last, and only once
// everything else did: until then it tells sweepRunDirs the init is gone,
// which removes the rest.
func removeRunDir(dir string, runDirFD, parentFD int, name string) {
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
	if !clean || !sameDirAt(runDirFD, parentFD, name) {
		return
	}
	_ = unix.Unlinkat(runDirFD, runDirOwnerFile, 0)
	_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
}

// sameDir reports whether the descriptors a and b are one directory.
func sameDir(a, b int) bool {
	var x, y unix.Stat_t
	if unix.Fstat(a, &x) != nil || unix.Fstat(b, &y) != nil {
		return false
	}
	return x.Dev == y.Dev && x.Ino == y.Ino && x.Mode&unix.S_IFMT == unix.S_IFDIR
}

// sameDirAt reports whether name in the directory parentFD is the directory
// open as fd.
func sameDirAt(fd, parentFD int, name string) bool {
	var a, b unix.Stat_t
	if unix.Fstat(fd, &a) != nil || unix.Fstatat(parentFD, name, &b, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return false
	}
	return a.Dev == b.Dev && a.Ino == b.Ino && b.Mode&unix.S_IFMT == unix.S_IFDIR
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
