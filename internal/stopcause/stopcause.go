// Copyright (c) 2025 Reliant Labs

// Package stopcause records what the machine looked like at the moment a
// process was told to stop, so a clean exit nobody asked for can be traced.
//
// A daemon that exits 0 on SIGTERM looks, from outside, exactly like one that
// was shut down on purpose: kubelet reports the container "Completed" and
// restarts it, and every command in flight fails. The sender's pid would
// settle it, but a Go program cannot read it: the runtime owns the signal
// handlers (os/signal hands over the signal number alone), and it unblocks
// SIGINT and SIGTERM on every thread it starts, so a signalfd never sees them.
// What can be read is the process table at the moment of delivery. A signal
// sent by an agent's `pkill` or `kill` arrives while the command that sent it
// is still running as one of the daemon's own descendants, and a signal aimed
// at PID 1 (kubelet, `docker stop`, `kill 1`) is announced by the init that
// relays it (internal/pid1). Between them, the sender is nearly always named.
package stopcause

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Proc is one process as /proc described it at capture time.
type Proc struct {
	PID     int
	PPID    int
	Age     time.Duration // 0 when the start time could not be read
	Cmdline string
}

func (p Proc) String() string {
	age := "?"
	if p.Age > 0 {
		age = p.Age.Truncate(time.Second).String()
	}
	return fmt.Sprintf("pid=%d ppid=%d age=%s cmd=%q", p.PID, p.PPID, age, p.Cmdline)
}

// Report is what a stop looked like. Descendants are this process's own
// process tree — its in-flight commands; Suspects are processes anywhere in
// view whose command line could send a signal.
type Report struct {
	Signal      string
	Uptime      time.Duration
	Parent      Proc
	Descendants []Proc
	Suspects    []Proc
	// Truncated counts descendants left out past maxDescendants.
	Truncated int
}

const (
	maxDescendants = 40
	maxCmdline     = 240
)

// killer matches a command line that can deliver a stop signal.
var killer = regexp.MustCompile(`(^|[\s/;&|(])(kill|pkill|killall)(\s|$)|daemon\s+stop`)

// Capture reads the process table under procRoot ("/proc" in production)
// and describes the stop of process self, started at started. It never
// fails: whatever cannot be read is left out, and on a system without /proc
// the report carries the signal, the uptime and the parent pid alone.
func Capture(sig os.Signal, procRoot string, self int, started, now time.Time) Report {
	r := Report{Uptime: now.Sub(started)}
	if sig != nil {
		r.Signal = sig.String()
	}
	procs := readProcs(procRoot, now)
	byPID := make(map[int]Proc, len(procs))
	children := make(map[int][]int)
	for _, p := range procs {
		byPID[p.PID] = p
		children[p.PPID] = append(children[p.PPID], p.PID)
	}

	ppid := os.Getppid()
	if me, ok := byPID[self]; ok {
		ppid = me.PPID
	}
	r.Parent = Proc{PID: ppid}
	if p, ok := byPID[ppid]; ok {
		r.Parent = p
	}

	// Breadth-first, so the commands the daemon started directly come first
	// when the list is cut.
	queue := append([]int(nil), children[self]...)
	sort.Ints(queue)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if len(r.Descendants) < maxDescendants {
			r.Descendants = append(r.Descendants, byPID[pid])
		} else {
			r.Truncated++
		}
		kids := append([]int(nil), children[pid]...)
		sort.Ints(kids)
		queue = append(queue, kids...)
	}

	for _, p := range procs {
		if p.PID != self && killer.MatchString(p.Cmdline) {
			r.Suspects = append(r.Suspects, p)
		}
	}
	return r
}

// String renders the report as the lines a person reads in the daemon log.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Stop signal %s after %s up; sent by an unknown process (Go cannot read the sender's pid). Evidence:\n",
		orUnknown(r.Signal), r.Uptime.Truncate(time.Second))
	fmt.Fprintf(&b, "  parent: %s\n", r.Parent)
	if len(r.Suspects) == 0 {
		b.WriteString("  processes that could have sent it: none still running\n")
	} else {
		b.WriteString("  processes that could have sent it:\n")
		for _, p := range r.Suspects {
			fmt.Fprintf(&b, "    %s\n", p)
		}
	}
	if len(r.Descendants) == 0 {
		b.WriteString("  in-flight commands: none\n")
	} else {
		fmt.Fprintf(&b, "  in-flight commands (%d):\n", len(r.Descendants)+r.Truncated)
		for _, p := range r.Descendants {
			fmt.Fprintf(&b, "    %s\n", p)
		}
		if r.Truncated > 0 {
			fmt.Fprintf(&b, "    … and %d more\n", r.Truncated)
		}
	}
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// readProcs lists every process under procRoot. A process that exits while
// it is read is skipped.
func readProcs(procRoot string, now time.Time) []Proc {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	boot := bootTime(procRoot)
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		ppid, startTicks, ok := parseStat(stat)
		if !ok {
			continue
		}
		p := Proc{PID: pid, PPID: ppid}
		if !boot.IsZero() && startTicks > 0 {
			if age := now.Sub(boot.Add(time.Duration(startTicks) * time.Second / clockTicks)); age > 0 {
				p.Age = age
			}
		}
		cmd, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		p.Cmdline = cmdline(cmd, stat)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// clockTicks is USER_HZ, the unit of /proc/<pid>/stat's start time. It is
// 100 on every Linux architecture Reliant ships for, and reading it properly
// (sysconf) needs cgo.
const clockTicks = 100

// parseStat reads the parent pid and start time from /proc/<pid>/stat. The
// command name in field 2 may hold spaces and parentheses, so the fields are
// counted from the last ')'.
func parseStat(stat []byte) (ppid int, startTicks int64, ok bool) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(string(stat[i+1:]))
	// fields[0] is the state (field 3); ppid is field 4, starttime field 22.
	if len(fields) < 20 {
		return 0, 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	startTicks, _ = strconv.ParseInt(fields[19], 10, 64)
	return ppid, startTicks, true
}

// cmdline joins a NUL-separated argv, falling back to the bracketed command
// name for kernel threads and zombies, and bounds its length.
func cmdline(raw, stat []byte) string {
	s := strings.TrimSpace(strings.ReplaceAll(string(bytes.TrimRight(raw, "\x00")), "\x00", " "))
	if s == "" {
		if i, j := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')'); i >= 0 && j > i {
			s = "[" + string(stat[i+1:j]) + "]"
		}
	}
	if len(s) > maxCmdline {
		s = s[:maxCmdline] + "…"
	}
	return s
}

// bootTime reads the btime line of /proc/stat.
func bootTime(procRoot string) time.Time {
	data, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			if sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
				return time.Unix(sec, 0)
			}
		}
	}
	return time.Time{}
}
