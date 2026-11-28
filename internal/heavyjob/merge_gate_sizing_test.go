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
	// holders is what the heavy-slot status read prints ("" when it fails);
	// env is the gate's environment beyond nproc()=32 and MG_MEM_GIB=mem.
	for _, tc := range []struct {
		name, child, parent, holders, mem, env, want, why string
	}{
		{"alone keeps the machine", "max 100000", "max 100000", "1", "128", "", "32 32 16 8", "share of 32 CPUs (nproc), 128 GiB among 1 heavy holders"},
		{"no holders counts itself", "max 100000", "max 100000", "0", "128", "", "32 32 16 8", "among 1 heavy holders"},
		{"four holders", "max 100000", "max 100000", "4", "64", "", "32 6 3 2", "share of 32 CPUs (nproc), 64 GiB among 4 heavy holders"},
		{"four holders, memory to spare", "max 100000", "max 100000", "4", "512", "", "32 12 6 3", "among 4 heavy holders"},
		{"status unreadable", "max 100000", "max 100000", "", "128", "", "32 16 8 4", "heavy-slot holders unreadable, assumed 3"},
		{"GOMAXPROCS wins", "max 100000", "max 100000", "4", "64", "GOMAXPROCS=10", "32 10 5 2", "GOMAXPROCS from the environment"},
		{"GOMAXPROCS above the machine", "max 100000", "max 100000", "1", "64", "GOMAXPROCS=99", "32 32 16 8", "GOMAXPROCS"},
		{"GOMAXPROCS not a count", "max 100000", "max 100000", "4", "64", "GOMAXPROCS=0", "32 6 3 2", "among 4 heavy holders"},
		{"MG_CPUS wins", "max 100000", "max 100000", "4", "64", "GOMAXPROCS=10 MG_CPUS=20", "32 20 10 5", "MG_CPUS"},
		{"four CPUs", "400000 100000", "max 100000", "1", "64", "", "4 4 2 2", "share of 4 CPUs (cgroup cpu.max)"},
		{"cpu.max lower than GOMAXPROCS", "400000 100000", "max 100000", "1", "64", "GOMAXPROCS=10", "4 4 2 2", "GOMAXPROCS"},
		{"tighter ancestor", "800000 100000", "200000 100000", "1", "64", "", "2 2 2 2", "share of 2 CPUs (cgroup cpu.max)"},
		{"fractional", "150000 100000", "max 100000", "1", "64", "", "1 1 2 2", "share of 1 CPUs (cgroup cpu.max)"},
		{"unavailable", "", "", "1", "128", "", "32 32 16 8", "share of 32 CPUs (nproc)"},
		{"memory unknown", "max 100000", "max 100000", "4", "0", "", "32 12 6 3", "0 GiB among 4 heavy holders"},
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
			section = strings.ReplaceAll(section, `holders=$( { "${slotforge[@]}" machine slot status --class heavy --json | jq -e '.holders | length'; } 2>/dev/null) || holders=`, "holders="+tc.holders)
			if section == script[start:end] || !strings.Contains(section, "holders="+tc.holders+"\n") {
				t.Fatal("the heavy-slot status read was not stubbed")
			}
			cmd := exec.Command("bash", "-c", "set -u; unset MG_CPUS GOMAXPROCS; export MG_MEM_GIB="+tc.mem+" "+tc.env+"; nproc() { echo 32; }; "+section+"\nprintf '%s %s %s %s' \"$cpus\" \"$gate_cpus\" \"$half\" \"$quarter\"")
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("sizing: %v\n%s%s", err, out, stderr.String())
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("sizing = %q, want %q\n%s", got, tc.want, stderr.String())
			}
			if !strings.Contains(stderr.String(), "merge-gate: CPU budget ") || !strings.Contains(stderr.String(), tc.why) {
				t.Fatalf("budget line %q does not say %q", stderr.String(), tc.why)
			}
		})
	}
}
