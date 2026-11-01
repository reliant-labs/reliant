//go:build linux

package worktreereclaim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/procledger"
)

// An undumpable process (ssh-agent, gpg-agent, a keyring daemon,
// credsandbox's init) keeps its /proc/<pid> directory owned by its user, but
// its cwd, exe, fd/ and maps read EACCES to everyone, the daemon included. If
// every such process held, ONE ssh-agent on the machine would hold every
// whole-worktree retirement forever (reliant#1998). So an uninspectable
// same-owner process is NOT counted as using the workspace when either:
//
//  1. the daemon accounts for it: it is a credsandbox init this daemon started,
//     entered in internal/procledger with every path it holds, and pid,
//     process group and start time all match, so a recycled pid cannot pass
//     for it. Its recorded paths are then checked like a readable cwd; or
//  2. it is one of uninspectableAgents, programs that never hold a project
//     worktree, recognised by exact name in what /proc still shows of it.
//
// Every other uninspectable process still holds, as before.
//
// The names in 2 are what the process says it is: a same-owner process can
// call itself ssh-agent. That is within the threat this check guards against.
// It stops a removal from pulling a directory out from under the user's own
// tools by accident; a hostile process running as the user could delete the
// workspace itself.

// uninspectableAgent is a program allowed to be uninspectable: name is its
// executable's base name, and arg, when set, an argument it must carry.
type uninspectableAgent struct{ name, arg string }

// uninspectableAgents are the undumpable programs that run as the user and
// never have a working directory or an open file inside a project worktree.
var uninspectableAgents = []uninspectableAgent{
	{name: "ssh-agent"},
	{name: "gpg-agent"},
	{name: "gnome-keyring-daemon"},
	{name: "dbus-daemon"},
	{name: "dbus-broker"},
	// The per-user service manager; PID 1's systemd is not the user's.
	{name: "systemd", arg: "--user"},
}

// taskCommLen is the kernel's TASK_COMM_LEN less its NUL: a longer program
// name is cut to this many bytes in stat's comm and status's Name.
const taskCommLen = 15

// procIdentity is what /proc shows of a process whatever its dumpability:
// stat, status and cmdline are readable for an undumpable process too.
type procIdentity struct {
	comm  string   // stat's comm, field 2
	name  string   // status's Name
	argv  []string // cmdline; nil when it could not be read
	pgrp  int      // stat field 5
	start uint64   // stat field 22, clock ticks from boot
}

func readProcIdentity(base string) (procIdentity, error) {
	var id procIdentity
	stat, err := readProcFile(filepath.Join(base, "stat"))
	if err != nil {
		return id, err
	}
	open, end := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return id, errors.New("malformed process stat")
	}
	id.comm = string(stat[open+1 : end])
	// Fields 3 onward: state ppid pgrp ... starttime (field 22).
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return id, errors.New("short process stat")
	}
	if id.pgrp, err = strconv.Atoi(fields[2]); err != nil {
		return id, fmt.Errorf("process group: %w", err)
	}
	if id.start, err = strconv.ParseUint(fields[19], 10, 64); err != nil {
		return id, fmt.Errorf("start time: %w", err)
	}
	status, err := readProcFile(filepath.Join(base, "status"))
	if err != nil {
		return id, err
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if name, ok := strings.CutPrefix(line, "Name:\t"); ok {
			id.name = name
			break
		}
	}
	if cmdline, err := readProcFile(filepath.Join(base, "cmdline")); err == nil {
		id.argv = strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
		if len(cmdline) == 0 {
			id.argv = []string{}
		}
	}
	return id, nil
}

func readProcFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("%s exceeds its budget", filepath.Base(path))
	}
	return data, nil
}

// agent returns the allowlisted program id is, matching names exactly, never
// as a substring: comm and Name must both be the program's (cut to the
// kernel's comm length), and when cmdline is readable its argv[0] base name
// must be the whole name and it must carry the required argument.
func (id procIdentity) agent() (string, bool) {
	for _, a := range uninspectableAgents {
		comm := a.name
		if len(comm) > taskCommLen {
			comm = comm[:taskCommLen]
		}
		if id.comm != comm || id.name != comm {
			continue
		}
		if id.argv == nil {
			// cmdline unreadable: comm and Name are all there is, and they
			// cannot show a required argument.
			return a.name, a.arg == ""
		}
		if len(id.argv) == 0 || filepath.Base(id.argv[0]) != a.name {
			continue
		}
		if a.arg != "" && !slices.Contains(id.argv[1:], a.arg) {
			continue
		}
		return a.name, true
	}
	return "", false
}

// judgeUninspectable decides a same-owner process pid (its /proc directory
// base) whose state `what` could not be read for cause. nil means it is not
// using roots and the caller moves on to the next process; an error holds.
func judgeUninspectable(ctx context.Context, base, pid, what string, roots []string, cause error) error {
	hold := fmt.Errorf("process %s %s unreadable: %w", pid, what, cause)
	if !errors.Is(cause, fs.ErrPermission) {
		return hold
	}
	id, err := readProcIdentity(base)
	if err != nil {
		if processDone(base) {
			return nil
		}
		slog.DebugContext(ctx, "worktreereclaim: uninspectable process holds: its identity is unreadable",
			"pid", pid, "unreadable", what, "error", err)
		return hold
	}
	hold = fmt.Errorf("process %s (%s) %s unreadable: %w", pid, id.comm, what, cause)
	n, err := strconv.Atoi(pid)
	if err != nil {
		return hold
	}
	if p, ok := procledger.Lookup(n); ok && p.Pgid == id.pgrp && p.Start == id.start {
		for _, path := range p.Paths {
			if processPathUses(path, roots) {
				slog.DebugContext(ctx, "worktreereclaim: daemon-accounted process holds: it uses the workspace",
					"pid", pid, "comm", id.comm, "path", path)
				return fmt.Errorf("workspace in use by process %s (a sandboxed command's init, which holds %s)", pid, path)
			}
		}
		slog.DebugContext(ctx, "worktreereclaim: uninspectable process is not use: the daemon accounts for it",
			"pid", pid, "comm", id.comm, "unreadable", what, "holds", p.Paths)
		return nil
	}
	if agent, ok := id.agent(); ok {
		slog.DebugContext(ctx, "worktreereclaim: uninspectable process is not use: a known agent",
			"pid", pid, "agent", agent, "comm", id.comm, "argv0", argv0(id.argv), "unreadable", what)
		return nil
	}
	slog.DebugContext(ctx, "worktreereclaim: uninspectable process holds: neither accounted for nor a known agent",
		"pid", pid, "comm", id.comm, "name", id.name, "argv0", argv0(id.argv), "unreadable", what)
	return hold
}

func argv0(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}
