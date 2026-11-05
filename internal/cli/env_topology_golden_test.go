package cli

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"

	"github.com/reliant-labs/forge/pkg/release"
)

var updateTopologyGoldens = flag.Bool("update-topology", false, "rewrite the forge env status (all-envs) golden files")

// The all-environments `forge env status` document is a contract the console
// and the control plane (through forge/pkg/topology) both read. These goldens
// pin it BYTE FOR BYTE — JSON and text, ledger-only and --verify — across
// every row state the assembly distinguishes: current, behind, dirty,
// unbound, undeclared, a release cut elsewhere, a failed ledger read, a
// failed declaration read, a hosted env with its own release list, workloads
// and a rollout phase, a hosted env whose rollout read failed, and a failed
// apply. A refactor of the assembly that changes one byte of any of them
// fails here, which is what lets the assembly move between packages safely.
//
// Two values are normalized before comparing, because they are not the
// assembly's output: generated_at (the clock) and the temp directories the
// fixture lives in.

// goldenLifecycleResolver is a target resolver that also answers the
// declared lifecycle, so the golden covers a non-empty lifecycle.
type goldenLifecycleResolver struct{ stubResolver }

func (goldenLifecycleResolver) Lifecycle(_ context.Context, _, env string) string {
	if env == "preprod" {
		return "ephemeral"
	}
	return ""
}

// goldenTopologyOpts stages the fixture project and wires every seam.
func goldenTopologyOpts(t *testing.T) (envTopologyOptions, []string) {
	t.Helper()
	dir := t.TempDir()

	stageReleaseLedger(t, dir, "v1.3.0", "2026-07-01T00:25:07Z", true, map[string]string{
		"control-plane": dg("aaa3"), "reliant": dg("bbb3"),
	})
	stageReleaseLedger(t, dir, "v1.4.0", "2026-07-20T00:00:00Z", false, map[string]string{"control-plane": dg("aaa4")})
	stageReleaseLedger(t, dir, "v1.5.15", "2026-09-10T13:53:22Z", false, map[string]string{
		"control-plane": dg("aaa15"), "reliant": dg("bbb15"), "internal-console": dg("ccc15"),
	})
	for _, env := range []string{"prod", "staging", "preprod", "broken", "branchy", "cloud", "cloud2", "failed", "unreadable"} {
		declareEnvDir(t, dir, env)
	}

	files := testReleases(t, dir)
	store := newMemBindingStore(map[string]release.Promotion{
		"prod": {
			ID:         "prm-prod-1",
			Release:    "v1.5.15",
			FromEnv:    "staging",
			PromotedAt: mustTime(t, "2026-09-10T13:53:22Z"),
			Gates:      []release.Gate{{Name: "test", Status: release.GateStatusPassed}, {Name: "lint", Status: release.GateStatusFailed}},
			Resolved: map[string]string{
				"control-plane": dg("aaa15"), "reliant": dg("bbb15"), "internal-console": dg("ccc15"),
			},
		},
		"staging": {
			Release:    "v1.3.0",
			PromotedAt: mustTime(t, "2026-07-01T00:25:15Z"),
			Resolved:   map[string]string{"control-plane": dg("aaa3"), "reliant": dg("bbb3")},
		},
		"hotfix": {
			Release:    "v1.4.0",
			PromotedAt: mustTime(t, "2026-07-21T00:00:00Z"),
			Resolved:   map[string]string{"control-plane": dg("aaa4")},
		},
		"branchy": {
			Release:    "v9.9.9-branch",
			PromotedAt: mustTime(t, "2026-10-01T00:00:00Z"),
			Resolved:   map[string]string{"control-plane": dg("branchy")},
		},
		"failed": {
			ID:            "prm-failed-1",
			Release:       "v1.4.0",
			PromotedAt:    mustTime(t, "2026-07-22T00:00:00Z"),
			RecordedGates: []release.Gate{{Name: applyGateName, Status: release.GateStatusFailed, Summary: "apply: deployment/api: ImagePullBackOff"}},
			Resolved:      map[string]string{"control-plane": dg("aaa4")},
		},
		"cloud": {
			ID:         "prm-cloud-7",
			Release:    "20261014.042859-cd678a0d6531",
			PromotedAt: mustTime(t, "2026-10-14T05:00:00Z"),
			Resolved:   map[string]string{"web": dg("eee1")},
		},
		"cloud2": {
			ID:         "prm-cloud2-1",
			Release:    "20261014.042859-cd678a0d6531",
			PromotedAt: mustTime(t, "2026-10-14T06:00:00Z"),
			Resolved:   map[string]string{"web": dg("eee1")},
		},
	})
	hostedReleases := newMemReleaseLedger(
		release.Release{Version: "20261013.002646-aaaaaaaaaaaa", Git: release.Git{Commit: "c-13"}, CreatedAt: mustTime(t, "2026-10-13T00:26:46Z")},
		release.Release{Version: "20261014.042859-cd678a0d6531", Git: release.Git{Commit: "c-14", Tag: "v2.0.0"}, CreatedAt: mustTime(t, "2026-10-14T04:28:59Z")},
	)

	opts := envTopologyOptions{
		ProjectDir: dir,
		Resolver:   goldenLifecycleResolver{stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}}},
		Ledgers: func(_ context.Context, env string) (envLedger, error) {
			switch env {
			case "broken":
				return envLedger{}, errors.New("control plane https://cp.example.test: 401 unauthenticated")
			case "unreadable":
				return envLedger{Bindings: &memBindingStore{err: context.DeadlineExceeded}, Releases: files}, nil
			case "cloud", "cloud2":
				return envLedger{Bindings: store, Releases: hostedReleases, Hosted: true}, nil
			}
			return envLedger{Bindings: store, Releases: files}, nil
		},
		Destinations: func(_ context.Context, _, env string) envDestination {
			switch env {
			case "cloud":
				return envDestination{
					Destination:      destinationHosted,
					ControlPlaneKind: "persistent",
					Endpoint:         "https://cp.example.test",
					EnvironmentID:    "env-123",
					Verdict:          "converged",
					Workloads: []deploytarget.HostedWorkloadStatus{{
						Name: "web", Tier: "workload", URL: "https://web.example.test", Hostname: "web.example.test",
						Verdict: "converged", ObservedState: "ready", ObservedDigest: dg("eee1"), DesiredDigest: dg("eee1"),
						Domains: []deploytarget.HostedCustomDomain{{
							Domain: "www.example.test", State: "pending_dns",
							RequiredRecords: []deploytarget.HostedDNSRecord{{Type: "CNAME", Name: "www", Value: "edge.example.test"}},
						}},
					}},
					Placements: []partPlacement{{Name: "web", Kind: "workload", Placement: placementHosted, Actuator: actuatorPlatform}},
				}
			case "cloud2":
				return envDestination{Destination: destinationMixed, ControlPlaneKind: "persistent", Endpoint: "https://cp.example.test",
					Placements: []partPlacement{
						{Name: "db", Kind: "database", Placement: placementCluster, Cluster: "gke-east", Actuator: actuatorForge},
						{Name: "web", Kind: "workload", Placement: placementHosted, Actuator: actuatorPlatform},
					}}
			case "branchy":
				return envDestination{Note: "could not render this environment to resolve where it runs: boom"}
			}
			return envDestination{Destination: destinationCluster, Placements: []partPlacement{
				{Name: "api", Kind: "workload", Placement: placementCluster, Cluster: "gke-east", Actuator: actuatorHub},
				{Name: "site", Kind: "frontend", Placement: placementBucket, Actuator: actuatorForge},
			}}
		},
		Rollouts: func(_ context.Context, env, _ string) (wireRollout, error) {
			if env == "cloud2" {
				return wireRollout{}, errors.New("deadline exceeded")
			}
			return wireRollout{Phase: "ROLLOUT_PHASE_SUCCEEDED"}, nil
		},
	}
	envs := []string{"prod", "staging", "preprod", "hotfix", "broken", "branchy", "cloud", "cloud2", "failed", "unreadable"}
	return opts, envs
}

var generatedAtRE = regexp.MustCompile(`("generated_at": )"[^"]*"`)

// normalizeTopologyOutput replaces what is not the assembly's output: the
// clock and the fixture's temp directories.
func normalizeTopologyOutput(out, projectDir string) string {
	out = generatedAtRE.ReplaceAllString(out, `$1"<generated_at>"`)
	out = strings.ReplaceAll(out, projectDir, "<project>")
	// The machine ledger lives under a per-test ledger home.
	if home, err := ledgerHome(); err == nil {
		out = strings.ReplaceAll(out, home, "<ledger-home>")
	}
	return out
}

func assertTopologyGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "topology", name)
	if *updateTopologyGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update-topology to create it): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("%s changed. The all-envs `forge env status` document is a byte-for-byte contract.\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

func TestTopologyGolden(t *testing.T) {
	cases := []struct {
		name   string
		json   bool
		verify bool
	}{
		{"ledger.json.golden", true, false},
		{"ledger.txt.golden", false, false},
		{"verify.json.golden", true, true},
		{"verify.txt.golden", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, envs := goldenTopologyOpts(t)
			opts.JSON = tc.json
			if tc.verify {
				opts.Verify = true
				opts.Lister = &recordingLister{images: []cluster.WorkloadImage{
					deployImage("control-plane", "ghcr.io/acme/control-plane@"+dg("aaa15")),
					deployImage("reliant", "ghcr.io/acme/reliant@"+dg("wrong")),
				}}
			}
			var err error
			out := captureStdout(t, func() {
				err = runEnvTopology(context.Background(), envs, opts)
			})
			out += "exit: " + strconv.Itoa(exitCodeOf(t, err)) + "\n"
			assertTopologyGolden(t, tc.name, normalizeTopologyOutput(out, opts.ProjectDir))
		})
	}
}
