// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/reliant-labs/reliant/internal/osutil"
)

// Each command's own GOTMPDIR.
//
// The go command builds in a work directory under GOTMPDIR (go-build*, ~300
// MB for a linked binary) and removes it when it exits, but not when it is
// SIGKILLed, and it dies of SIGTERM without removing it too. In a shared
// /tmp nothing can tell such a directory from a live build's, so it stayed:
// on 2026-11-03 one machine held ~1,500 of them, 222 GB, and its disk filled.
//
// So every command the daemon starts gets a GOTMPDIR of its own,
// <temp dir>/reliant-gotmp-<uid>/gotmp-<daemon pid>-<n>, and the daemon removes
// it when the command's processes are all gone: ownership is a fact, not a
// guess from an mtime. One the command left processes running in stays; the
// storage reaper removes it once no live process holds it, which every one of
// its processes does in its environment (internal/diskwatch, gowork.go).
//
// Only GOTMPDIR. TMPDIR is left alone: a file an agent makes with mktemp in
// one command is routinely read by its next one, and removing it with the
// command would break that. The linker's go-link-* (TMPDIR) are reclaimed by
// the reaper once proven dead.

var goTmpSeq atomic.Uint64

// prepareGoTmp gives cmd its own GOTMPDIR, unless its environment sets one.
// It never stops the command from starting: without the root it builds
// where it would have.
func (t *Tree) prepareGoTmp(cmd *exec.Cmd) {
	if cmd.Env == nil {
		cmd.Env = os.Environ() //nolint:forbidigo // a command with no environment of its own inherits this process's; written down to add GOTMPDIR
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GOTMPDIR=") {
			return
		}
	}
	base := filepath.Join(os.TempDir(), fmt.Sprintf("%s%d", GoTmpBasePrefix, os.Getuid()))
	if err := os.Mkdir(base, 0o700); err != nil && !os.IsExist(err) {
		return
	}
	// The base must be ours and a real directory: /tmp is shared, and a
	// directory another user made there is a way to read our builds.
	if fi, err := os.Lstat(base); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || !ownedByUs(fi) {
		return
	}
	dir := filepath.Join(base, fmt.Sprintf("%s%d-%d", GoTmpPrefix, os.Getpid(), goTmpSeq.Add(1)))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return
	}
	cmd.Env = append(cmd.Env, "GOTMPDIR="+dir)
	t.goTmp = dir
}

// releaseGoTmp removes the command's GOTMPDIR once none of its processes is
// left: its cgroup is empty, or, with none, its process group is. In the
// background: a killed build's root holds hundreds of megabytes.
func (t *Tree) releaseGoTmp() {
	dir := t.goTmp
	if dir == "" {
		return
	}
	if t.dir != "" && populated(t.dir) {
		return
	}
	if t.dir == "" && t.pid > 0 {
		if members, ok := osutil.GroupMembers(t.pid); !ok || len(members) > 0 {
			return
		}
	}
	t.goTmp = ""
	go func() { _ = os.RemoveAll(dir) }()
}

// ownedByUs reports whether fi belongs to this process's user.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
