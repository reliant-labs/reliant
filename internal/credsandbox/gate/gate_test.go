// Copyright (c) 2025 Reliant Labs
package gate

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestClassifyExit(t *testing.T) {
	for code, want := range map[int]Class{0: ClassPass, 1: ClassFail, 2: ClassUnproven, 3: ClassFail, 125: ClassFail, 137: ClassFail} {
		if got := ClassifyExit(code); got != want {
			t.Errorf("ClassifyExit(%d) = %s, want %s", code, got, want)
		}
	}
	for c, want := range map[Class]int{ClassPass: 0, ClassFail: 1, ClassUnproven: 2, Class("bogus"): 2} {
		if got := c.ExitCode(); got != want {
			t.Errorf("%s.ExitCode() = %d, want %d", c, got, want)
		}
	}
}

func TestClassifyLimitsAreUnprovenNeverFail(t *testing.T) {
	cases := []struct {
		name          string
		exit          int
		oom, timedOut bool
		want          Class
	}{
		{"pass", 0, false, false, ClassPass},
		{"fail", 1, false, false, ClassFail},
		{"unproven exit", 2, false, false, ClassUnproven},
		{"wall time, killed", 137, false, true, ClassUnproven},
		{"wall time even if it had passed", 0, false, true, ClassUnproven},
		{"oom and failed", 1, true, false, ClassUnproven},
		{"oom killed init", 137, true, false, ClassUnproven},
		{"oom in a child but the suite passed", 0, true, false, ClassPass},
	}
	for _, c := range cases {
		got, reason := classify(c.exit, c.oom, c.timedOut, time.Minute)
		if got != c.want {
			t.Errorf("%s: classify = %s (%q), want %s", c.name, got, reason, c.want)
		}
		if got == ClassUnproven && reason == "" {
			t.Errorf("%s: an unproven verdict carries no reason", c.name)
		}
	}
}

func validSpec() Spec {
	return Spec{
		RunID:     "run-1",
		Image:     "gate:1",
		Command:   []string{"task", "ci:full"},
		SourceDir: "/src",
		Limits:    Limits{CPUs: 4, MemoryBytes: 8 << 30, Pids: 4096, DiskBytes: 50 << 30, Wall: 30 * time.Minute},
	}
}

func TestSpecValidate(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("valid spec: %v", err)
	}
	cases := map[string]func(*Spec){
		"empty run id":         func(s *Spec) { s.RunID = "" },
		"run id with a slash":  func(s *Spec) { s.RunID = "a/b" },
		"upper-case run id":    func(s *Spec) { s.RunID = "Run" },
		"no image":             func(s *Spec) { s.Image = "" },
		"no command":           func(s *Spec) { s.Command = nil },
		"no source":            func(s *Spec) { s.SourceDir = "" },
		"two sources":          func(s *Spec) { s.SourceTar = strings.NewReader("") },
		"no cpu limit":         func(s *Spec) { s.Limits.CPUs = 0 },
		"no memory limit":      func(s *Spec) { s.Limits.MemoryBytes = 0 },
		"no pids limit":        func(s *Spec) { s.Limits.Pids = 0 },
		"no wall limit":        func(s *Spec) { s.Limits.Wall = 0 },
		"no disk limit":        func(s *Spec) { s.Limits.DiskBytes = 0 },
		"negative disk":        func(s *Spec) { s.Limits.DiskBytes = -1 },
		"reserved env":         func(s *Spec) { s.Env = map[string]string{"HOME": "/root"} },
		"bad env name":         func(s *Spec) { s.Env = map[string]string{"A-B": "x"} },
		"ipv6 egress allow":    func(s *Spec) { s.EgressAllow = []netip.Prefix{netip.MustParsePrefix("fd00::/8")} },
		"invalid egress allow": func(s *Spec) { s.EgressAllow = []netip.Prefix{{}} },
	}
	for name, mutate := range cases {
		s := validSpec()
		mutate(&s)
		if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: Validate = %v, want ErrInvalidSpec", name, err)
		}
	}
}

func TestReportReady(t *testing.T) {
	r := Report{Availability: Availability{Available: true}, DiskLimit: true}
	if err := r.Ready(); err != nil {
		t.Fatalf("ready report: %v", err)
	}
	if err := (Report{Availability: Availability{Reason: "no runtime"}}).Ready(); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unavailable: %v", err)
	}
	r.Egress = errors.New("no rules")
	if err := r.Ready(); !errors.Is(err, ErrEgressNotEnforced) {
		t.Errorf("no egress rules: %v", err)
	}
	r.Egress, r.DiskLimit = nil, false
	if err := r.Ready(); !errors.Is(err, ErrDiskLimitNotEnforced) {
		t.Errorf("a disk limit the machine cannot enforce must refuse, not run unbounded: %v", err)
	}
}
