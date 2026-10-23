package cli

import (
	"context"
	"testing"
)

// The cut's OUTCOME, which `forge env build <env> --release <v>` renders and
// release.yml depends on: a re-run of the same tag over the same artifacts is
// exit 0 with Created false — a different FACT from a fresh cut, the same
// verdict. A pipeline re-run of the release job must not be an error.
func TestReleaseCutOutcome_CreatedThenIdempotent(t *testing.T) {
	dir := useTestLedger(t, t.TempDir())
	t.Chdir(dir)
	if err := WriteBuildState(dir, "staging", BuildState{
		Image: "demo", Tag: "v1.0.0", Pushed: true, PushedAt: nowRFC3339(), Digest: sha("a"),
	}); err != nil {
		t.Fatal(err)
	}
	// No deploy/kcl/staging, no entities: ledgerFor answers the project's
	// files, which is the backend a test can read back.
	opts := buildOptions{run: runOptions{None: true}}
	first, err := cutReleaseFromBuildState(context.Background(), dir, "staging", "v1.0.0", "", nil, opts)
	if err != nil {
		t.Fatalf("first cut: %v", err)
	}
	if !first.Created || first.Images != 1 || first.Release.Artifacts["demo"].Digests == nil || first.Ledger == "" {
		t.Fatalf("first cut outcome = %+v", first)
	}

	again, err := cutReleaseFromBuildState(context.Background(), dir, "staging", "v1.0.0", "", nil, opts)
	if err != nil {
		t.Fatalf("an identical re-cut must succeed: %v", err)
	}
	if again.Created {
		t.Fatalf("a re-cut must report Created false, got %+v", again)
	}
	if again.Release.Version != "v1.0.0" {
		t.Errorf("a re-cut must still report the release it found: %+v", again.Release)
	}
}

// Hosted for CI = a control plane that RUNS something. A LOCAL control plane
// (secret store only) and a plain cluster env are not.
func TestCIEnvIsHosted(t *testing.T) {
	t.Parallel()
	cp := &ControlPlaneEntity{Type: "control_plane", Endpoint: "https://cp.example"}
	hostedWorkload := WorkloadEntity{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHosted}}
	clusterWorkload := WorkloadEntity{Name: "worker", Runtime: RuntimeEntity{Type: RuntimeCluster}}
	cases := map[string]struct {
		e           *KCLEntities
		hosted, mix bool
	}{
		"hosted":           {&KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{hostedWorkload}}, true, false},
		"mixed":            {&KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{hostedWorkload, clusterWorkload}}, true, true},
		"local cp only":    {&KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{clusterWorkload}}, false, true},
		"no control plane": {&KCLEntities{Workloads: []WorkloadEntity{hostedWorkload}}, false, false},
	}
	for name, tc := range cases {
		if got := ciEnvIsHosted(tc.e); got != tc.hosted {
			t.Errorf("%s: ciEnvIsHosted = %v, want %v", name, got, tc.hosted)
		}
		if tc.hosted {
			if got := envAppliesLocally(tc.e); got != tc.mix {
				t.Errorf("%s: mixed = %v, want %v", name, got, tc.mix)
			}
		}
	}
}
