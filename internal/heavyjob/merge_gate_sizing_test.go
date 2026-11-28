// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeGateQuotaSizing(t *testing.T) {
	data, err := os.ReadFile("../../scripts/merge-gate.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	start := strings.Index(script, "cpus=$(nproc")
	end := strings.Index(script, "# ── the gate's memory budget")
	if start < 0 || end <= start {
		t.Fatal("CPU sizing section not found")
	}
	for _, tc := range []struct {
		name, child, parent, want string
	}{
		{"uncapped", "max 100000", "max 100000", "32 32 16 8"},
		{"four CPUs", "400000 100000", "max 100000", "4 4 2 2"},
		{"tighter ancestor", "800000 100000", "200000 100000", "2 2 2 2"},
		{"fractional", "150000 100000", "max 100000", "1 1 2 2"},
		{"unavailable", "", "", "32 32 16 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for path, value := range map[string]string{"parent/child/cpu.max": tc.child, "parent/cpu.max": tc.parent} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
					t.Fatal(err)
				}
				if value != "" {
					if err := os.WriteFile(filepath.Join(root, path), []byte(value+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			section := strings.ReplaceAll(script[start:end], "/sys/fs/cgroup", root)
			section = strings.ReplaceAll(section, "cg=$(sed -n 's/^0:://p' /proc/self/cgroup 2>/dev/null)", "cg=/parent/child")
			section = strings.ReplaceAll(section, "load=$(cut -d' ' -f1 /proc/loadavg 2>/dev/null | cut -d. -f1)", "load=0")
			cmd := exec.Command("bash", "-c", "set -u; unset MG_CPUS; nproc() { echo 32; }; "+section+"\nprintf '%s %s %s %s' \"$cpus\" \"$gate_cpus\" \"$half\" \"$quarter\"")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sizing: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("sizing = %q, want %q", got, tc.want)
			}
		})
	}
}
