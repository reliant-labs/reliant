// Copyright (c) 2026 Reliant Labs

// Package procguard keeps a machine from running out of processes: it counts
// them against the limit, refuses new background heavy commands once the count
// passes 70% of it, and reports whose process trees are the problem.
//
// Why. On 2026-10-30 a cloud machine reached its process limit (~5,200 bash,
// load ~1,900, fork EAGAIN everywhere) and had to be restarted, which killed
// every agent's work. Nothing noticed on the way up: agents kept starting gates
// and test lanes into a machine that was already failing to fork. With this the
// daemon stops adding heavy work at 70%, and the person's inbox names the
// largest process trees and the agent that started each, so the one at fault
// can be stopped (internal/proctree bounds each command; this bounds the
// machine).
//
// The count is the daemon user's tasks — processes and threads, which is what
// RLIMIT_NPROC counts — against that limit; where the kernel's threads-max is
// tighter, all tasks against it. Trees are process groups: every command the
// daemon starts leads one (proctree), so a group the daemon started is
// attributed to its agent; any other group (started outside the daemon, or
// detached with setsid) is listed as untracked, by its command line.
//
// Refusal has hysteresis: it starts at 70% and ends below 60%, so a count
// hovering at the line does not flap. A refusal never stops a command that is
// running, and never refuses a foreground or a light command: it stops the
// kind of work that piled up.
//
//forge:exclude-contract: one concrete policy over the machine's own /proc; its only consumers are the heavy-command gate (heavyjob.Admission) and the heartbeat, and tests drive it through a fake /proc
package procguard

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/proctree"
)

const (
	// RefuseFraction is the share of the limit past which new background
	// heavy commands are refused.
	RefuseFraction = 0.70
	// ClearFraction is the share it must fall below for refusals to stop.
	ClearFraction = 0.60
	// DefaultInterval is how often the guard counts. A /proc scan of a few
	// thousand processes takes milliseconds.
	DefaultInterval = 10 * time.Second
	// TopTrees is how many trees a report names.
	TopTrees = 5
	// commandMax bounds a reported command line.
	commandMax = 120
)

// Config is the guard's policy. The zero value is the default.
type Config struct {
	// ProcRoot is where /proc is; tests point it at a fake.
	ProcRoot string
	// UID is whose tasks count against RLIMIT_NPROC; -1 (or zero with
	// ProcRoot unset) is this process's.
	UID int
	// NprocLimit is RLIMIT_NPROC; 0 reads this process's (and treats
	// unlimited as no limit).
	NprocLimit uint64
	// ThreadsMax is the kernel's limit on all tasks; 0 reads
	// /proc/sys/kernel/threads-max.
	ThreadsMax uint64
	// Interval is how often Run counts; 0 is DefaultInterval.
	Interval time.Duration
	// Owners lists the daemon's process trees; nil is proctree.Live.
	Owners func() []proctree.Info
}

// Tree is one process group on the machine.
type Tree struct {
	Pgid    int
	ChatID  string
	Command string
	Tasks   uint64
	Tracked bool
}

// Sample is one count of the machine's tasks.
type Sample struct {
	// Count and Limit are the tasks and the limit of whichever measure is
	// closest to its limit.
	Count, Limit uint64
	Top          []Tree
	At           time.Time
}

// Fraction is Count / Limit, 0 without a limit.
func (s Sample) Fraction() float64 {
	if s.Limit == 0 {
		return 0
	}
	return float64(s.Count) / float64(s.Limit)
}

// Guard counts the machine's processes and refuses heavy background work near
// the limit. A nil *Guard refuses nothing and reports nothing.
type Guard struct {
	cfg     Config
	changed chan struct{}

	mu       sync.Mutex
	last     Sample
	sampled  bool
	refusing bool
}

// New builds a guard. It does not count until Check or Run.
func New(cfg Config) *Guard {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Owners == nil {
		cfg.Owners = proctree.Live
	}
	return &Guard{cfg: cfg, changed: make(chan struct{}, 1)}
}

// Run counts every Interval until ctx ends. Where processes cannot be counted
// (no /proc) it returns at once.
func (g *Guard) Run(ctx context.Context) {
	if g == nil || !g.Check() {
		return
	}
	t := time.NewTicker(g.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Check()
		}
	}
}

// Changed receives a token when refusal starts or stops, so the heartbeat can
// carry it at once. Nil-safe.
func (g *Guard) Changed() <-chan struct{} {
	if g == nil {
		return nil
	}
	return g.changed
}

// Check counts now and updates the refusal state. False when processes
// cannot be counted here.
func (g *Guard) Check() bool {
	s, ok := g.sample()
	if !ok {
		return false
	}
	g.mu.Lock()
	was := g.refusing
	switch f := s.Fraction(); {
	case f >= RefuseFraction:
		g.refusing = true
	case f < ClearFraction:
		g.refusing = false
	}
	now := g.refusing
	g.last, g.sampled = s, true
	g.mu.Unlock()
	if now != was {
		if now {
			logging.Warn("procguard: machine near its process limit; refusing new background heavy commands",
				"tasks", s.Count, "limit", s.Limit, "top", describeTop(s.Top, 3))
		} else {
			logging.Info("procguard: process count back below the clear line; heavy commands accepted again",
				"tasks", s.Count, "limit", s.Limit)
		}
		select {
		case g.changed <- struct{}{}:
		default:
		}
	}
	return true
}

// HeavyRefusal is why a new background heavy command may not start now, or ""
// when it may (heavyjob.Admission).
func (g *Guard) HeavyRefusal() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	refusing, s := g.refusing, g.last
	g.mu.Unlock()
	if !refusing {
		return ""
	}
	return fmt.Sprintf("This machine is near its process limit: %d of %d tasks (%.0f%%; new background heavy commands are refused from %.0f%% until it falls below %.0f%%). "+
		"Largest process trees: %s. Stop what is not needed (shell_kill), wait for it to finish, or run the command in the foreground. "+
		"Never detach long work with nohup, & or setsid: run_in_background keeps it bounded and killable.",
		s.Count, s.Limit, 100*s.Fraction(), 100*RefuseFraction, 100*ClearFraction, describeTop(s.Top, TopTrees))
}

// Report is the last count for the heartbeat; nil before the first or where
// processes cannot be counted.
func (g *Guard) Report() *reliantv1.ProcessReport {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.sampled {
		return nil
	}
	rep := &reliantv1.ProcessReport{Count: g.last.Count, Limit: g.last.Limit, RefusingHeavy: g.refusing}
	for _, t := range g.last.Top {
		rep.Top = append(rep.Top, &reliantv1.ProcessTree{ChatId: t.ChatID, Command: t.Command, Tasks: t.Tasks, Tracked: t.Tracked})
	}
	return rep
}

// describeTop names up to n trees: "1,840 tasks: `bash scripts/ci-gate.sh all`
// (agent 2ee014da)".
func describeTop(top []Tree, n int) string {
	if len(top) == 0 {
		return "none counted"
	}
	parts := make([]string, 0, n)
	for i, t := range top {
		if i == n {
			break
		}
		who := "not started by an agent's tracked command"
		if t.ChatID != "" {
			who = "agent " + shortID(t.ChatID)
		} else if t.Tracked {
			who = "a daemon command"
		}
		parts = append(parts, fmt.Sprintf("%d tasks: `%s` (%s)", t.Tasks, t.Command, who))
	}
	return strings.Join(parts, "; ")
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// shorten bounds a command line for a report.
func shorten(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if len(cmd) > commandMax {
		return cmd[:commandMax-1] + "…"
	}
	return cmd
}

// rank sorts trees largest first and keeps TopTrees.
func rank(trees []Tree) []Tree {
	sort.Slice(trees, func(i, j int) bool {
		if trees[i].Tasks != trees[j].Tasks {
			return trees[i].Tasks > trees[j].Tasks
		}
		return trees[i].Pgid < trees[j].Pgid
	})
	if len(trees) > TopTrees {
		trees = trees[:TopTrees]
	}
	return trees
}
