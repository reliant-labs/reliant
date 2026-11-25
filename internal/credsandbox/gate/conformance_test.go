// Copyright (c) 2025 Reliant Labs
package gate

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// conformanceCase is one check every Backend must pass on a machine that can
// run it. The denial tests of the pipeline build plan (E9d: a planted host
// credential file, /proc/<daemon pid>/environ, `docker run -v /:/host`, a
// planted host loopback port) are cases of this table, so each backend — the
// sysbox one here, a vm one later — runs all of them through runConformance.
type conformanceCase struct {
	name    string
	command []string
	limits  func(*Limits)
	check   func(t *testing.T, res Result, stdout string)
}

func conformanceCases() []conformanceCase {
	wantClass := func(c Class) func(*testing.T, Result, string) {
		return func(t *testing.T, res Result, _ string) {
			if res.Class != c {
				t.Errorf("class = %s (%+v), want %s", res.Class, res, c)
			}
		}
	}
	return []conformanceCase{
		{name: "exit 0 passes", command: []string{"sh", "-c", "exit 0"}, check: wantClass(ClassPass)},
		{name: "exit 1 fails", command: []string{"sh", "-c", "exit 1"}, check: wantClass(ClassFail)},
		{name: "exit 2 is unproven", command: []string{"sh", "-c", "exit 2"}, check: wantClass(ClassUnproven)},
		{
			name:    "runs in the tree with the nested engine up",
			command: []string{"sh", "-c", "test -f marker && pwd && docker info --format '{{.ServerVersion}}'"},
			check: func(t *testing.T, res Result, out string) {
				if res.Class != ClassPass || !strings.Contains(out, TreeDir) {
					t.Errorf("result %+v, output %q", res, out)
				}
			},
		},
		{
			name:    "no variable from the daemon",
			command: []string{"env"},
			check: func(t *testing.T, res Result, out string) {
				if res.Class != ClassPass || strings.Contains(out, "RELIANT_GATE_CONFORMANCE_CANARY") || !strings.Contains(out, "RELIANT_GATE=1") {
					t.Errorf("result %+v, environment:\n%s", res, out)
				}
			},
		},
		{
			name:    "an exceeded wall time is unproven",
			command: []string{"sleep", "600"},
			limits:  func(l *Limits) { l.Wall = 5 * time.Second },
			check: func(t *testing.T, res Result, _ string) {
				if res.Class != ClassUnproven || !res.TimedOut {
					t.Errorf("result %+v, want unproven by wall time", res)
				}
			},
		},
	}
}

func runConformance(t *testing.T, b Backend, image string, allow []netip.Prefix) {
	t.Setenv("RELIANT_GATE_CONFORMANCE_CANARY", "daemon-secret")
	src := t.TempDir()
	if err := os.WriteFile(src+"/marker", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, c := range conformanceCases() {
		t.Run(c.name, func(t *testing.T) {
			spec := Spec{
				RunID:       "conformance-" + randomSuffix() + "-" + string(rune('a'+i)),
				Image:       image,
				Command:     c.command,
				SourceDir:   src,
				EgressAllow: allow,
				Limits:      Limits{CPUs: 2, MemoryBytes: 2 << 30, Pids: 2048, Wall: 3 * time.Minute},
			}
			if c.limits != nil {
				c.limits(&spec.Limits)
			}
			var out bytes.Buffer
			spec.Stdout, spec.Stderr = &out, &out
			t.Cleanup(func() {
				if err := b.Teardown(context.Background(), spec.RunID); err != nil {
					t.Errorf("teardown: %v", err)
				}
			})
			res, err := b.Run(context.Background(), spec)
			if err != nil {
				t.Fatalf("Run: %v\n%s", err, out.String())
			}
			c.check(t, res, out.String())
		})
	}
}

// TestSysboxConformance runs the conformance table against real sysbox. It
// needs a machine with sysbox-runc registered, the egress rules provisioned
// (ProvisionScript, run by gate-check), and a gate image:
// RELIANT_GATE_TEST_IMAGE (default docker:27-dind) for the gate,
// RELIANT_GATE_TEST_PROBE_IMAGE (default the same) for the probe. Elsewhere
// it skips and says which is missing.
func TestSysboxConformance(t *testing.T) {
	testkit.Slow(t, "starts sysbox containers, each running a nested dockerd")
	image := os.Getenv("RELIANT_GATE_TEST_IMAGE")
	if image == "" {
		image = "docker:27-dind"
	}
	probe := os.Getenv("RELIANT_GATE_TEST_PROBE_IMAGE")
	if probe == "" {
		probe = image
	}
	b := NewSysbox(SysboxConfig{ProbeImage: probe})
	ctx := context.Background()
	if a := b.Detect(ctx); !a.Available {
		t.Skipf("sysbox backend unavailable on this machine: %s", a.Reason)
	}
	r, err := b.Preflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Egress != nil {
		t.Skipf("the gate egress rules are not provisioned on this machine (run gate-check): %v", r.Egress)
	}
	runConformance(t, b, image, r.EgressAllow)
}
