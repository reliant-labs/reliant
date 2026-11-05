package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
)

// TestApplyDeployGroupsLifecycleNotice proves the notice reaches the APPLY,
// not just that the predicate is right.
//
// The predicate table (env_lifecycle_test.go) tests a pure function; it would
// still pass if nothing ever called it, or if the notice were wired to a
// branch a real deploy never takes. This drives applyDeployGroups — the one
// place a direct cluster apply happens — and reads stdout.
//
// Both directions matter, and the negative is the one that would rot: an env
// that HAS declared itself local must stay silent, or the notice becomes
// noise on every inner-loop deploy and people learn to ignore it before it
// ever means anything.
func TestApplyDeployGroupsLifecycleNotice(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	ctx := context.Background()
	dir := workloadURLProject(t, "dev", crossClusterBundle)
	entities, err := RenderKCL(ctx, dir, "dev")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	groups, err := buildDeployGroups("dev", entities, "acme-dev")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}

	for _, tc := range []struct {
		name     string
		allowed  bool
		wantLine bool
	}{
		{
			name:     "an env that declares neither lifecycle nor applier is told direct apply will be refused",
			allowed:  false,
			wantLine: true,
		},
		{
			name:     "a declared local env stays silent",
			allowed:  true,
			wantLine: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeCrossClusterKubectl(t)
			out := captureStdout(t, func() {
				if err := applyDeployGroups(ctx, deployApplyInput{
					groups: groups, entities: entities, hasK8sServices: true,
					mainK:    filepath.Join(dir, "deploy", "kcl", "dev", "main.k"),
					imageTag: "t", namespace: "acme-dev", envName: "dev",
					rollout:            cluster.RolloutPolicy{Timeout: 5 * time.Second},
					directApplyAllowed: tc.allowed,
				}); err != nil {
					t.Fatalf("applyDeployGroups: %v", err)
				}
			})
			got := strings.Contains(out, lifecycleNoticeLine)
			if got != tc.wantLine {
				t.Errorf("notice printed = %v, want %v\nstdout:\n%s", got, tc.wantLine, out)
			}
			// The notice is ONE line, once — an apply that printed it per
			// cluster group would bury the applies it annotates.
			if tc.wantLine && strings.Count(out, lifecycleNoticeLine) != 1 {
				t.Errorf("notice printed %d times, want exactly 1\nstdout:\n%s",
					strings.Count(out, lifecycleNoticeLine), out)
			}
		})
	}
}
