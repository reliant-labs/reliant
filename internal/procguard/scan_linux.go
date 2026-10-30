// Copyright (c) 2026 Reliant Labs
//go:build linux

package procguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/reliant/internal/proctree"
)

// sample reads /proc once: every task's owner, process group and thread
// count. The whole scan is one directory listing plus one stat and one small
// read per process — no process is forked to count processes.
func (g *Guard) sample() (Sample, bool) {
	root := g.cfg.ProcRoot
	if root == "" {
		root = "/proc"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return Sample{}, false
	}
	uid := g.cfg.UID
	if g.cfg.ProcRoot == "" && uid <= 0 {
		uid = os.Getuid()
	}

	type group struct {
		tasks  uint64
		member int // a live member, for the command line
	}
	var userTasks, allTasks uint64
	groups := map[int]*group{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, err := os.Stat(dir)
		if err != nil {
			continue // exited between the listing and the stat
		}
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		pgid, threads, ok := parseStat(string(stat))
		if !ok {
			continue
		}
		allTasks += threads
		st, _ := info.Sys().(*syscall.Stat_t)
		if st == nil || int(st.Uid) != uid {
			continue
		}
		userTasks += threads
		grp := groups[pgid]
		if grp == nil {
			grp = &group{member: pid}
			groups[pgid] = grp
		}
		grp.tasks += threads
		if pid == pgid {
			grp.member = pid
		}
	}

	s := Sample{At: time.Now()}
	nproc, threadsMax := g.limits(root)
	// Whichever measure is nearer its limit is the one that matters.
	if nproc > 0 {
		s.Count, s.Limit = userTasks, nproc
	}
	if threadsMax > 0 && (s.Limit == 0 || float64(allTasks)/float64(threadsMax) > s.Fraction()) {
		s.Count, s.Limit = allTasks, threadsMax
	}

	owners := map[int]proctree.Info{}
	for _, o := range g.cfg.Owners() {
		owners[o.Pgid] = o
	}
	trees := make([]Tree, 0, len(groups))
	for pgid, grp := range groups {
		t := Tree{Pgid: pgid, Tasks: grp.tasks}
		if o, ok := owners[pgid]; ok {
			t.Tracked, t.ChatID, t.Command = true, o.Owner.ChatID, shorten(o.Owner.Command)
		}
		trees = append(trees, t)
	}
	trees = rank(trees)
	for i := range trees {
		if trees[i].Command == "" {
			trees[i].Command = shorten(cmdline(root, groups[trees[i].Pgid].member))
		}
	}
	s.Top = trees
	// A daemon command whose group is gone, cgroup and all, is forgotten.
	proctree.Prune(func(pgid int) bool { return groups[pgid] != nil })
	return s, true
}

// limits is the user's RLIMIT_NPROC (0 when unlimited, or for root, which it
// does not bind) and the kernel's threads-max.
func (g *Guard) limits(root string) (nproc, threadsMax uint64) {
	nproc, threadsMax = g.cfg.NprocLimit, g.cfg.ThreadsMax
	if nproc == 0 && g.cfg.ProcRoot == "" && os.Geteuid() != 0 {
		var rl unix.Rlimit
		if unix.Getrlimit(unix.RLIMIT_NPROC, &rl) == nil && rl.Cur != unix.RLIM_INFINITY {
			nproc = rl.Cur
		}
	}
	if threadsMax == 0 {
		if b, err := os.ReadFile(filepath.Join(root, "sys", "kernel", "threads-max")); err == nil {
			threadsMax, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		}
	}
	return nproc, threadsMax
}

// parseStat reads a /proc/<pid>/stat line's process group (field 5) and
// thread count (field 20). The command name (field 2) may hold spaces and
// parentheses, so fields are counted from the last ')'.
func parseStat(stat string) (pgid int, threads uint64, ok bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, 0, false
	}
	f := strings.Fields(stat[end+1:])
	// f[0] is field 3 (state): field n is f[n-3].
	if len(f) < 18 || f[0] == "Z" {
		return 0, 0, false
	}
	pgid, err1 := strconv.Atoi(f[2])
	threads, err2 := strconv.ParseUint(f[17], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return pgid, threads, true
}

// cmdline is a process's command line, "" when it is gone.
func cmdline(root string, pid int) string {
	b, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}
