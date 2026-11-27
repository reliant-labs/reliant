// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// cgroupBounds lowers the cpu.weight and memory.high of the daemon's command
// cgroup C (cmd-<daemon pid>-<n>, internal/proctree) while a slotted command
// runs in it, and puts back what it found when the command has exited.
//
// On C itself, not on the capped cgroup cpuCap makes under it: cpu.weight
// only divides CPU between siblings, and the capped cgroup's one sibling is
// the caller cgroup holding this wrapper, which never wants any. C's siblings
// are every other command the daemon runs — the agents' shells, their dev
// servers, other slotted commands — which is the contention the weight is
// for:
//
//	<delegated>/self          the daemon
//	<delegated>/commands/     every command, together
//	    cmd-<pid>-<n>         an interactive command, cpu.weight 100
//	    cmd-<pid>-<m>         this command line, cpu.weight lowered here
//	        heavy-<pid>-caller
//	        heavy-<pid>-command   cpu.max = the share
//
// memory.high goes on C too: C holds the command and this wrapper, a few MiB,
// and so it binds the command whether or not cpuCap made a cgroup for it.
//
// So, unlike cpu.max, these are files of the cgroup the wrapper runs in. That
// is safe because they are only ever lowered: a value already at or below the
// bound (one the daemon's operator set, or a nested wrapper's) is left alone.
// Holders share the originals under the cgroup lock; only the last release
// restores a value, and only if it is still as a wrapper wrote it. Only in a
// daemon command cgroup, for the reason cpuCap is: one that holds this
// command line and nothing else. Where a file is missing (no cpu or memory
// controller enabled for the command cgroups, cgroup v1, not Linux) or not
// writable, that bound is not set, and nothing says so on the command's
// output: a missing limiter is not the command's failure.
type cgroupBounds struct {
	// dir is C; "" when no holder was registered or after release.
	dir string
	// lowered is each file written, with the value found and the value it
	// read back after the write (the kernel rounds memory.high to a page).
	lowered []loweredFile
	// lease is flocked until release or process death (and is close-on-exec).
	lease *os.File
}

type loweredFile struct{ name, was, now string }

// lowerBounds lowers this process's command cgroup to b.
func lowerBounds(b bounds) *cgroupBounds {
	return lowerBoundsAt("/proc/self/cgroup", cgroupFS, b)
}

// lowerBoundsAt is lowerBounds reading this process's cgroup from selfCgroup
// and the cgroup tree under fsRoot (tests).
func lowerBoundsAt(selfCgroup, fsRoot string, b bounds) *cgroupBounds {
	c := &cgroupBounds{}
	if b.weight < 1 && b.memoryHigh == 0 {
		return c
	}
	rel, why := ownCgroup(selfCgroup)
	if why != "" || !isCommandCgroupName(filepath.Base(rel)) {
		return c
	}
	dir := filepath.Join(fsRoot, rel)
	unlock, err := lockCgroup(dir)
	if err != nil {
		return c
	}
	defer unlock()
	state, path, err := readBoundsState(dir)
	if err != nil {
		return c
	}
	if len(state.Holders) == 0 {
		restoreBounds(dir, state)
		state.Files = make(map[string][2]string)
	}
	lease, err := os.CreateTemp(filepath.Dir(path), "holder-*")
	if err != nil {
		return c
	}
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lease.Close()
		_ = os.Remove(lease.Name())
		return c
	}
	c.lease = lease
	c.dir = dir
	if b.weight > 0 {
		c.lower("cpu.weight", uint64(b.weight))
	}
	if b.memoryHigh > 0 {
		c.lower("memory.high", b.memoryHigh)
	}
	for _, f := range c.lowered {
		if original, ok := state.Files[f.name]; ok {
			state.Files[f.name] = [2]string{original[0], f.now}
		} else {
			state.Files[f.name] = [2]string{f.was, f.now}
		}
	}
	state.Holders = append(state.Holders, filepath.Base(lease.Name()))
	if err := writeBoundsState(path, state); err != nil {
		for _, f := range c.lowered {
			_ = writeCgroupFile(dir, f.name, f.was)
		}
		_ = lease.Close()
		_ = os.Remove(lease.Name())
		c.lease = nil
		c.dir = ""
	}
	return c
}

// The cgroup flock protects the interfaces and ledger; each holder has a
// separate kernel-held lease, automatically unlocked even on SIGKILL.
// State lives outside cgroupfs, which cannot contain ordinary files.
type boundsState struct {
	Holders []string
	Files   map[string][2]string
}

func boundsStateRoot() (string, error) {
	root := filepath.Join(os.TempDir(), fmt.Sprintf("reliant-heavy-bounds-%d", os.Geteuid()))
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	// Lstat must not accept a symlink, even one pointing to a private directory.
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	var st unix.Stat_t
	if err := unix.Lstat(root, &st); err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || st.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("untrusted bounds directory %s", root)
	}
	return root, nil
}

func readBoundsState(dir string) (boundsState, string, error) {
	state := boundsState{Files: make(map[string][2]string)}
	root, err := boundsStateRoot()
	if err != nil {
		return state, "", err
	}
	path := filepath.Join(root, fmt.Sprintf("%x.json", sha256.Sum256([]byte(dir))))
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if os.IsNotExist(err) {
		return state, path, nil
	}
	if err != nil {
		return state, path, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return state, path, err
	}
	if state.Files == nil {
		state.Files = make(map[string][2]string)
	}
	live := state.Holders[:0]
	for _, name := range state.Holders {
		if filepath.Base(name) != name || !strings.HasPrefix(name, "holder-") {
			return state, path, fmt.Errorf("invalid bounds lease %q", name)
		}
		leasePath := filepath.Join(root, name)
		fd, err := unix.Open(leasePath, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return state, path, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		_ = unix.Close(fd)
		if err == unix.EWOULDBLOCK {
			live = append(live, name)
		} else if err != nil {
			return state, path, err
		} else {
			_ = os.Remove(leasePath)
		}
	}
	state.Holders = live
	return state, path, nil
}

func writeBoundsState(path string, state boundsState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// CreateTemp uses O_EXCL: neither a pre-planted symlink nor an interrupted
	// writer's temporary file can be followed or truncated.
	f, err := os.CreateTemp(filepath.Dir(path), ".bounds-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// lower writes want to dir's interface file name when it reads higher ("max"
// is higher than anything), and records what it replaced.
func (c *cgroupBounds) lower(name string, want uint64) {
	path := filepath.Join(c.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	was := strings.TrimSpace(string(data))
	if cur, ok := cgroupValue(was); !ok || cur <= want {
		return
	}
	if err := writeCgroupFile(c.dir, name, strconv.FormatUint(want, 10)); err != nil {
		return
	}
	now, err := os.ReadFile(path)
	if err != nil {
		return
	}
	c.lowered = append(c.lowered, loweredFile{name: name, was: was, now: strings.TrimSpace(string(now))})
}

// cgroupValue parses a single-value cgroup interface file: a number, or
// "max" for no limit.
func cgroupValue(s string) (uint64, bool) {
	if s == "max" {
		return math.MaxUint64, true
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// release puts back each value lower replaced, unless something else has
// changed it since. Best effort, and harmless twice: C is the daemon's, and
// removed with the command.
func (c *cgroupBounds) release() {
	if c.dir == "" {
		return
	}
	unlock, err := lockCgroup(c.dir)
	if err != nil {
		return
	}
	defer unlock()
	if c.lease != nil {
		_ = c.lease.Close()
		_ = os.Remove(c.lease.Name())
		c.lease = nil
	}
	state, path, err := readBoundsState(c.dir)
	if err != nil {
		return
	}
	if len(state.Holders) > 0 {
		if writeBoundsState(path, state) == nil {
			c.dir = ""
		}
		return
	}
	restoreBounds(c.dir, state)
	_ = os.Remove(path)
	c.dir = ""
	c.lowered = nil
}

func restoreBounds(dir string, state boundsState) {
	for name, values := range state.Files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || strings.TrimSpace(string(data)) != values[1] {
			continue
		}
		_ = writeCgroupFile(dir, name, values[0])
	}
}
