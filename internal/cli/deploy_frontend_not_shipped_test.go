package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// failingBuildRunner fails every `npm run build` and runs nothing else.
type failingBuildRunner struct{ calls []string }

func (r *failingBuildRunner) record(name string, args []string) error {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, cmd)
	if cmd == "npm run build" {
		return errors.New("npm run build: exit status 1")
	}
	return nil
}

func (r *failingBuildRunner) Run(_ context.Context, name string, args ...string) error {
	return r.record(name, args)
}

func (r *failingBuildRunner) RunWithEnv(_ context.Context, _ map[string]string, name string, args ...string) error {
	return r.record(name, args)
}

func (r *failingBuildRunner) RunInDir(_ context.Context, _ string, _ map[string]string, name string, args ...string) error {
	return r.record(name, args)
}

func (r *failingBuildRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	return nil, r.record(name, args)
}

func (r *failingBuildRunner) OutputWithEnv(_ context.Context, _ map[string]string, name string, args ...string) ([]byte, error) {
	return nil, r.record(name, args)
}

// 2026-10-19, control-plane prod: reliant-web's `npm run build` failed and the
// deploy reported "PARTLY [firebase] site=reliant-prod: reliant-web — failed
// part-way; some of it may be live". Nothing had reached Firebase. Through the
// deploy's own dispatch and ship log, a Firebase frontend that fails in its
// build is a stage that shipped nothing.
func TestFrontendDispatch_FirebaseBuildFailureShipsNothing(t *testing.T) {
	runner := &failingBuildRunner{}
	registry := deploytarget.NewRegistry()
	registry.Register(deploytarget.FirebaseProvider{ProjectDir: t.TempDir(), Runner: runner, StagingRoot: filepath.Join(t.TempDir(), "public")})
	group := deploytarget.ServiceGroup{
		Env: "prod", ProviderID: deploytarget.FirebaseProvider{}.Name(),
		Frontends: []deploytarget.FirebaseFrontend{{
			Name: "reliant-web", Path: "reliant-web", DevRunner: "npm",
			Spec: deploytarget.FirebaseHostingSpec{Project: "reliant-prod", Site: "reliant-prod", PublicDir: "dist"},
		}},
	}

	var ship shipLog
	err := ship.run([]string{deploytarget.FormatGroupSummary(group)}, func() error {
		return dispatchDeployGroups(context.Background(), registry, []deploytarget.ServiceGroup{group})
	})
	if err == nil || !strings.Contains(err.Error(), "npm run build: exit status 1") {
		t.Fatalf("want the build failure, got %v", err)
	}
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "firebase ") {
			t.Fatalf("firebase ran after the build failed: %v", runner.calls)
		}
	}
	if ship.changedAnything() {
		t.Fatalf("a build that failed before any upload was recorded as live: shipped=%v partial=%v", ship.Shipped(), ship.Partial())
	}
}
