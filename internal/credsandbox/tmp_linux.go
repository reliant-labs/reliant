// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Every credentials-none command gets a /tmp and a /var/tmp of its own, and
// TMPDIR=/tmp. The machine's are where a credentialed step keeps what it is
// about to run (a `mktemp -d` it fetches a script into, then executes): seen by
// a credentials-none command running beside it, they are a race the command can
// win, by writing the script between the fetch and the run (Reliant bug
// e433fa60). The private ones are directories of the command's run directory
// on the daemon's disk, not a tmpfs: a build or a test suite writes gigabytes
// of work directories and binaries under TMPDIR, which a tmpfs would hold in
// memory. They go when the command exits, with the run directory (or when the
// daemon sweeps the run directory of an init that was killed).
//
// They cover every path the machine's /tmp and /var/tmp have, so a cloud
// machine's /tmp is no more reachable as /mnt/workspace-disk/tmp. One thing of
// the machine's /tmp stays: the directory the command was started in, when it
// is under one (a work directory made with mktemp), is carried into the
// private one at the same path, with everything mounted below it.

// ownTmpDir is the directory under target (/tmp) that holds the command's
// working directory cwd and is carried into its private one, relative to
// target, or "": the topmost directory holding cwd below tmpRoot, the daemon's
// temp directory, when that is under target, and below target otherwise. One
// directory a step was given (mktemp's), never the temp directory every step
// shares.
func ownTmpDir(target, tmpRoot, cwd string) string {
	base := target
	if tmpRoot != target && under(tmpRoot, target) && under(cwd, tmpRoot) && cwd != tmpRoot {
		base = tmpRoot
	}
	rel, err := filepath.Rel(base, cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	own := filepath.Join(base, strings.SplitN(rel, string(filepath.Separator), 2)[0])
	rel, _ = filepath.Rel(target, own)
	return rel
}

// privateTmpDirs are the shared scratch directories that are private to the
// command, and the run directory's entry that stands in for each.
var privateTmpDirs = []struct{ path, entry string }{
	{"/tmp", "tmp"},
	{"/var/tmp", "var-tmp"},
}

// makePrivateTmpDirs makes the run directory's stand-ins, sticky and writable
// by everyone, as the directories they replace are.
func makePrivateTmpDirs(runDir string) error {
	for _, d := range privateTmpDirs {
		p := filepath.Join(runDir, d.entry)
		if err := os.Mkdir(p, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(p, 0o1777); err != nil {
			return err
		}
	}
	return nil
}

// privateTmpPath is where a file the daemon prepares in the command's private
// /tmp is on the daemon's disk, and where the command sees it.
func privateTmpPath(runDir, name string) (host, inSandbox string) {
	return filepath.Join(runDir, "tmp", name), filepath.Join("/tmp", name)
}

// privateTmp mounts the run directory's stand-ins over /tmp and /var/tmp, and
// over every other path the same directories have (mounts, the mount table as
// it was before the sandbox changed it). Every source is opened before the
// first mount covers it. cwd is the command's working directory and tmpRoot
// the daemon's temp directory (ownTmpDir).
func privateTmp(runDir, cwd, tmpRoot string, mounts []mountInfo) error {
	type cover struct {
		targets []string
		src     int    // the stand-in
		carry   int    // the command's own directory under it, or -1
		name    string // carry's name in it
	}
	var covers []cover
	defer func() {
		for _, c := range covers {
			_ = unix.Close(c.src)
			if c.carry >= 0 {
				_ = unix.Close(c.carry)
			}
		}
	}()
	seen := map[string]bool{}
	for _, d := range privateTmpDirs {
		target, err := filepath.EvalSymlinks(d.path)
		if err != nil || seen[target] {
			continue // absent, or a link to one covered already (/var/tmp -> /tmp)
		}
		if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
			continue
		}
		seen[target] = true
		src, err := unix.Open(filepath.Join(runDir, d.entry), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("opening the private %s: %w", d.path, err)
		}
		c := cover{targets: dedupe(append([]string{target}, aliasPaths([]string{target}, mounts)...)), src: src, carry: -1}
		if name := ownTmpDir(target, canonicalDir(tmpRoot), canonicalDir(cwd)); name != "" {
			fd, err := unix.Open(filepath.Join(target, name), unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("opening the command's directory %s: %w", filepath.Join(target, name), err)
			}
			c.carry, c.name = fd, name
		}
		covers = append(covers, c)
	}
	for _, c := range covers {
		from := "/proc/self/fd/" + strconv.Itoa(c.src)
		for _, t := range c.targets {
			if fi, err := os.Stat(t); err != nil || !fi.IsDir() {
				continue
			}
			if err := unix.Mount(from, t, "", unix.MS_BIND, ""); err != nil {
				return fmt.Errorf("mounting a private %s: %w", t, err)
			}
		}
		if c.carry < 0 {
			continue
		}
		to := filepath.Join(c.targets[0], c.name)
		if err := os.MkdirAll(to, 0o700); err != nil {
			return fmt.Errorf("carrying the command's directory into the private %s: %w", c.targets[0], err)
		}
		if err := unix.Mount("/proc/self/fd/"+strconv.Itoa(c.carry), to, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("carrying the command's directory %s: %w", to, err)
		}
	}
	return nil
}
