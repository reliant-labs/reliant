package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// firstCutCP answers CutRelease NotFound until an environment was ensured,
// as the control plane does for a project nothing has deployed yet.
type firstCutCP struct {
	calls   []string
	ensured map[string]any
}

func (f *firstCutCP) Call(_ context.Context, proc string, req, out any) error {
	f.calls = append(f.calls, proc[strings.LastIndex(proc, "/")+1:])
	switch {
	case strings.HasSuffix(proc, "/EnsureEnvironment"):
		f.ensured, _ = req.(map[string]any)
		return json.Unmarshal([]byte(`{"environment":{"id":"env_1","name":"prod","project":"status"},"created":true}`), out)
	case strings.HasSuffix(proc, "/CutRelease"):
		if f.ensured == nil {
			return &cloud.Error{Code: cloud.CodeNotFound, Message: "release, environment or deployment not found"}
		}
		return json.Unmarshal([]byte(`{"created":true}`), out)
	}
	return &cloud.Error{Code: cloud.CodeUnimplemented, Message: proc}
}

func firstCutRelease() release.Release {
	return release.Release{Version: "v0.1.0", Project: "status", RecordOnly: true}
}

// `env build prod --release v0.1.0` before anything deployed prod: the cut
// ensures the env it was opened for and retries, so the scaffold's own
// first-ship path works (obs db0e1f64).
func TestHostedCut_FirstCutEnsuresTheEnvironment(t *testing.T) {
	cp := &firstCutCP{}
	l := hostedLedgerForEnv(cp, "https://cp.example", deploytarget.HostedEnvRef{Project: "status", Name: "prod", Kind: deploytarget.HostedEnvPersistent})
	created, err := l.Releases.(*hostedStore).Cut(context.Background(), firstCutRelease())
	if err != nil || !created {
		t.Fatalf("Cut = %v, %v; want created after ensuring the env (calls %v)", created, err, cp.calls)
	}
	if got := strings.Join(cp.calls, ","); got != "CutRelease,EnsureEnvironment,CutRelease" {
		t.Errorf("calls = %s", got)
	}
	if b, _ := json.Marshal(cp.ensured); !strings.Contains(string(b), `"prod"`) || !strings.Contains(string(b), `"status"`) {
		t.Errorf("ensured %s, want env prod of project status", b)
	}
}

// A ledger opened for no env (a project-wide read) never creates one: the
// NotFound is returned as it was.
func TestHostedCut_NoEnvKeepsTheNotFound(t *testing.T) {
	cp := &firstCutCP{}
	l := hostedLedger(cp, "https://cp.example", "status", deploytarget.HostedEnvPersistent)
	if _, err := l.Releases.(*hostedStore).Cut(context.Background(), firstCutRelease()); err == nil || !hostedErrorHasCode(err, cloud.CodeNotFound) {
		t.Fatalf("err = %v, want the NotFound unchanged", err)
	}
	for _, c := range cp.calls {
		if c == "EnsureEnvironment" {
			t.Errorf("a ledger with no env ensured one: %v", cp.calls)
		}
	}
}
