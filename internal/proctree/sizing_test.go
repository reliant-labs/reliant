// Copyright (c) 2026 Reliant Labs
package proctree

import (
	"strings"
	"testing"
)

// The limits scale with the CPU count and are bounded by the machine's thread
// budget, not by a constant.
func TestSize(t *testing.T) {
	tests := []struct {
		name                   string
		cpus, budget, set      int
		wantShared, wantPerCmd int
		wantBasis              string
	}{
		// A 2xl cloud workspace: 31 CPUs, RLIMIT_NPROC 7650 (kata's guest
		// kernel default, threads-max/2). Was 2000 per command.
		{"cloud 31 CPUs", 31, 7650, 0, 6502, 3008, "1024 + 64 per CPU x 31 CPUs"},
		// A laptop-class Linux box: 8 CPUs, 16 GiB (threads-max 131072, so
		// RLIMIT_NPROC 65536). Was 2000 per command.
		{"laptop 8 CPUs", 8, 65536, 0, 55705, 1536, "1024 + 64 per CPU x 8 CPUs"},
		// Many CPUs on the same budget: one command still leaves half the
		// shared limit to the others.
		{"64 CPUs, kata budget", 64, 7650, 0, 6502, 3251, "half the 6502 all commands share"},
		{"tiny budget", 1, 400, 0, 340, 256, "the minimum"},
		{"unknown budget", 2, 0, 0, 0, 1152, "1024 + 64 per CPU x 2 CPUs"},
		{"no CPU count", 0, 0, 0, 0, 1088, "1024 + 64 per CPU x 1 CPUs"},
		// command_threads is taken as set, even past half the shared limit:
		// the shared limit still binds all commands together.
		{"configured", 31, 7650, 5000, 6502, 5000, "set by " + CommandThreadsSetting},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := Size(tc.cpus, tc.budget, tc.set)
			if s.Shared != tc.wantShared || s.PerCommand != tc.wantPerCmd {
				t.Errorf("Size(%d, %d, %d) = shared %d, per command %d; want %d, %d",
					tc.cpus, tc.budget, tc.set, s.Shared, s.PerCommand, tc.wantShared, tc.wantPerCmd)
			}
			if got := s.perCommandBasis(); got != tc.wantBasis {
				t.Errorf("basis %q, want %q", got, tc.wantBasis)
			}
		})
	}
}

// The budget is the user's RLIMIT_NPROC, unless the kernel's threads-max (all
// users together) is lower or the rlimit does not apply.
func TestThreadBudget(t *testing.T) {
	tests := []struct{ nproc, threadsMax, want int }{
		{7650, 15300, 7650},
		{7650, 5000, 5000},
		{0, 15300, 15300}, // root: RLIMIT_NPROC does not apply
		{7650, 0, 7650},   // threads-max unreadable
		{0, 0, 0},
	}
	for _, tc := range tests {
		if got := threadBudget(tc.nproc, tc.threadsMax); got != tc.want {
			t.Errorf("threadBudget(%d, %d) = %d, want %d", tc.nproc, tc.threadsMax, got, tc.want)
		}
	}
}

// A command refused threads is told which limit, in threads, and what raises
// it; one refused nothing is told nothing.
func TestLimitMessage(t *testing.T) {
	s := Size(31, 7650, 0)
	if got := limitMessage(s, refused{}); got != "" {
		t.Errorf("nothing refused: %q", got)
	}
	tests := []struct {
		name string
		r    refused
		want []string
		not  []string
	}{
		{"own", refused{own: 7}, []string{"This command reached its thread limit (3008 threads", "refused it 7 new threads", "pthread_create", CommandThreadsSetting}, []string{"shared thread limit"}},
		{"shared", refused{shared: 3}, []string{"shared thread limit (6502 threads, 85% of the user's budget of 7650", "refused 3 new threads", "shell_kill"}, []string{"This command reached"}},
		{"either", refused{either: 2}, []string{"refused this command 2 new threads", "its own (3008 threads", "(6502 threads", CommandThreadsSetting}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := limitMessage(s, tc.r)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("message lacks %q:\n%s", w, got)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("message has %q:\n%s", n, got)
				}
			}
		})
	}
}

// The startup line shows the computed numbers.
func TestLimiterString(t *testing.T) {
	l := &Limiter{root: "/cg", commands: "/cg/commands", size: Size(31, 7650, 0), nproc: 6502}
	got := l.String()
	for _, w := range []string{"under /cg/commands, limited to 3008 threads (1024 + 64 per CPU x 31 CPUs)", "all commands together to 6502 threads", "RLIMIT_NPROC 6502 instead"} {
		if !strings.Contains(got, w) {
			t.Errorf("String() lacks %q:\n%s", w, got)
		}
	}
	l = &Limiter{why: "not on cgroup v2", nproc: 6502}
	if got := l.String(); !strings.Contains(got, "not on cgroup v2") || !strings.Contains(got, "RLIMIT_NPROC 6502 per command") {
		t.Errorf("String() = %q", got)
	}
}
