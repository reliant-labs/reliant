// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"os"
	"path/filepath"
	"testing"
)

// The wrapper caps its own command cgroup at its share, and nothing else.
func TestLimitCPU_CapsOnlyTheDaemonsCommandCgroup(t *testing.T) {
	root := t.TempDir()
	self := filepath.Join(t.TempDir(), "cgroup")
	for _, tc := range []struct {
		cgroup string
		capped bool
	}{
		{"/daemon/commands/cmd-268-7", true},
		{"/daemon/self", false},
		{"/user.slice/session-1.scope", false},
	} {
		dir := filepath.Join(root, tc.cgroup)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cpuMax := filepath.Join(dir, "cpu.max")
		if err := os.WriteFile(cpuMax, []byte("max 100000"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(self, []byte("0::"+tc.cgroup+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		why := limitCPUAt(self, root, 5)
		got, _ := os.ReadFile(cpuMax)
		if tc.capped && (why != "" || string(got) != "500000 100000") {
			t.Errorf("%s: cpu.max %q (%s), want a 5-CPU cap", tc.cgroup, got, why)
		}
		if !tc.capped && string(got) != "max 100000" {
			t.Errorf("%s: cpu.max set to %q outside a command cgroup", tc.cgroup, got)
		}
	}
	// No cpu controller: no cpu.max file, nothing set, nothing fails.
	dir := filepath.Join(root, "daemon", "commands", "cmd-268-8")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(self, []byte("0::/daemon/commands/cmd-268-8\n"), 0o644)
	if why := limitCPUAt(self, root, 5); why == "" {
		t.Error("reported a cap where there is no cpu.max")
	}
}
