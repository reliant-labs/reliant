// Copyright (c) 2025 Reliant Labs
package runcgate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"

	"github.com/reliant-labs/reliant/internal/gatetest"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun/gatehost"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

type copyFetcher struct{ src string }

func (c copyFetcher) Fetch(_ context.Context, spec gatehost.FetchSpec) error {
	return os.CopyFS(spec.Dir, os.DirFS(c.src))
}

// TestRealRuncGate runs a gate through the Runner on the machine's docker
// engine with plain runc: the stages run in order inside a container that
// has no host socket, no daemon variable and a read-only root, the verdict
// comes back through docker cp, a pass is never landable, and teardown
// leaves nothing labelled with the run. It needs docker and an image with sh
// already present (RELIANT_GATE_TEST_IMAGE, default alpine:3.24); it pulls
// nothing.
//
// The sysbox backend's real test arrives with its adapter, once the
// credsandbox gate backend is back on main.
func TestRealRuncGate(t *testing.T) {
	testkit.Slow(t, "starts docker containers")
	image := os.Getenv("RELIANT_GATE_TEST_IMAGE")
	if image == "" {
		image = "alpine:3.24"
	}
	b := New()
	ctx := context.Background()
	if a := b.Detect(ctx); !a.Available {
		gatetest.DockerUnavailable(t, "no docker engine: %s", a.Reason)
	}
	if err := b.Preflight(ctx); err != nil {
		t.Skipf("docker has no runc runtime: %v", err)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("image %s is not present (this test pulls nothing)", image)
	}
	t.Setenv("RELIANT_DAEMON_SECRET_FOR_TEST", "must-not-leak")

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &gatehost.Runner{
		NotIsolated: b, Fetcher: copyFetcher{src: src}, WorkRoot: t.TempDir(), Image: image,
		Limits: gatehost.Limits{CPUs: 1, MemoryBytes: 512 << 20, Pids: 256, DiskBytes: 1 << 30},
	}
	sel, err := r.Select(ctx, true)
	if err != nil || sel != gatehost.Backend(b) {
		t.Fatalf("select: %v %v", sel, err)
	}

	stage := func(n config.StageName, cmd string) gaterun.PlanStage {
		return gaterun.PlanStage{Name: n, State: config.StageOn, Kind: config.StageCommand, Command: cmd}
	}
	run := func(test string) gaterun.GateResult {
		var id [4]byte
		_, _ = rand.Read(id[:])
		req := gaterun.StartRequest{
			GateID: "gate-test-" + hex.EncodeToString(id[:]), Repo: "acme/widgets", Ref: "reliant-queue/b1",
			SHA: strings.Repeat("a", 40), TimeoutMs: (2 * time.Minute).Milliseconds(),
			Stages: []gaterun.PlanStage{
				stage(config.StageLint, `test -f README && test ! -e /var/run/docker.sock && test ! -e /root/.ssh`),
				stage(config.StageTest, test),
				stage(config.StageBuild, `echo built >"$RELIANT_GATE_ARTIFACTS/app"`),
				{Name: config.StageSBOMScan, State: config.StageOff},
			},
		}
		g := r.Run(ctx, b, req)
		out, err := exec.Command("docker", "ps", "-aq", "--filter", "label="+LabelRunID+"="+req.GateID).Output()
		vols, verr := exec.Command("docker", "volume", "ls", "-q", "--filter", "label="+LabelRunID+"="+req.GateID).Output()
		if err != nil || verr != nil || strings.TrimSpace(string(out)+string(vols)) != "" {
			t.Fatalf("teardown left %q %q (%v %v)", out, vols, err, verr)
		}
		return g
	}

	g := run(`! env | grep -q RELIANT_DAEMON_SECRET && touch built-here && ! touch /etc/gate 2>/dev/null && echo tested`)
	if g.Class != gaterun.ClassPass {
		t.Fatalf("contained gate: %s %s %s\n%+v", g.Class, g.ReasonCode, g.Reason, g.StageList())
	}
	if g.Isolation != gaterun.IsolationRunc || g.Landable() {
		t.Fatalf("a runc pass: isolation %s landable %v", g.Isolation, g.Landable())
	}
	if !strings.Contains(g.Stages[config.StageTest].LogTail, "tested") {
		t.Fatalf("test log tail %q", g.Stages[config.StageTest].LogTail)
	}
}
