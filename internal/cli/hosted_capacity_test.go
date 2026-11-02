package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

type capacityFake struct {
	capacity map[string]any
	capErr   error
	calls    []string
}

func (f *capacityFake) Call(_ context.Context, proc string, _, out any) error {
	f.calls = append(f.calls, proc)
	var reply any
	switch proc {
	case "controlplane.v1.DeployService/ListEnvironments":
		reply = map[string]any{"environments": []any{map[string]any{"id": "env-1", "name": "prod", "project": ""}}}
	case "controlplane.v1.DeployService/CheckDeployCapacity":
		if f.capErr != nil {
			return f.capErr
		}
		reply = f.capacity
	default:
		return errors.New("a side-effecting RPC was called before the capacity pre-flight finished: " + proc)
	}
	return decodeFake(reply, out)
}

type wireCoded struct{ code string }

func (e wireCoded) Error() string         { return "wire " + e.code }
func (e wireCoded) HasCode(c string) bool { return c == e.code }

func houndersEntities() *KCLEntities {
	job := hostedWL("migrate", func(w *WorkloadEntity) {
		w.Kind = "job"
		w.Spec.Kind = deployv1alpha1.KindJob
		w.Spec.Ports, w.Spec.Probes = nil, nil
		w.Spec.Args = []string{"db", "migrate", "up"}
	})
	return &KCLEntities{
		ControlPlane: &ControlPlaneEntity{Type: "control_plane", Endpoint: "https://cp.example"},
		Workloads:    []WorkloadEntity{hostedWL("api"), job},
		Databases:    []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted, Spec: deployv1alpha1.ManagedDatabaseSpec{StorageGiB: 1}}},
		Frontends:    []FrontendEntity{{Name: "web", Image: "ghcr.io/acme/web", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}}},
	}
}

func withCapacityFake(t *testing.T, f *capacityFake) {
	t.Helper()
	t.Setenv(cloud.DefaultTokenEnv, "rlat_test")
	withProjectName(t, "hounders")
	prev := hostedDeployClient
	hostedDeployClient = func(cloud.Endpoint, cloud.Credential) deploytarget.HostedCaller { return f }
	t.Cleanup(func() { hostedDeployClient = prev })
}

func TestHostedCapacityPreflight_RefusedBlocksWithReasonAndFix(t *testing.T) {
	f := &capacityFake{capacity: map[string]any{
		"allowed": false, "code": "DEPLOY_CAPACITY_CODE_NO_COMPUTE_PLAN",
		"reason": "this runs compute and the organization has no active compute plan",
		"fix":    "subscribe to a Reliant Compute plan in billing settings, then retry",
	}}
	withCapacityFake(t, f)
	var out strings.Builder
	_, err := hostedCapacityPreflight(context.Background(), "prod", houndersEntities(), 1, &out)
	var refused *deploytarget.CapacityRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *CapacityRefusedError", err)
	}
	for _, s := range []string{"no active compute plan", "subscribe to a Reliant Compute plan", "2 workload(s), 1 database(s), 1 build(s), 1 static site(s)"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error missing %q:\n%s", s, err)
		}
	}
	for _, c := range f.calls {
		if !strings.HasSuffix(c, "ListEnvironments") && !strings.HasSuffix(c, "CheckDeployCapacity") {
			t.Errorf("unexpected side-effecting call %s", c)
		}
	}
}

func TestHostedCapacityPreflight_AllowedProceeds(t *testing.T) {
	withCapacityFake(t, &capacityFake{capacity: map[string]any{"allowed": true, "code": "DEPLOY_CAPACITY_CODE_OK", "hasComputePlan": true,
		"ceilingCpuMillicores": "8000", "ceilingMemoryBytes": "17179869184", "ceilingStorageGib": "100"}})
	var out strings.Builder
	capacity, err := hostedCapacityPreflight(context.Background(), "prod", houndersEntities(), 1, &out)
	if err != nil {
		t.Fatal(err)
	}
	renderCapacitySection(&out, capacity)
	for _, want := range []string{"Capacity  OK", "demand    ", "ceiling   8000m CPU, 16.0 GiB memory, 100 GiB storage"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("capacity section lacks %q:\n%s", want, out.String())
		}
	}
}

func TestHostedCapacityPreflight_UnimplementedWarnsAndProceeds(t *testing.T) {
	withCapacityFake(t, &capacityFake{capErr: wireCoded{"unimplemented"}})
	var out strings.Builder
	if _, err := hostedCapacityPreflight(context.Background(), "prod", houndersEntities(), 1, &out); err != nil {
		t.Fatalf("unimplemented must continue, got %v", err)
	}
	if !strings.Contains(out.String(), "WARNING: capacity could not be pre-checked") {
		t.Errorf("output = %q", out.String())
	}
}

func TestHostedCapacityPreflight_NetworkErrorFails(t *testing.T) {
	withCapacityFake(t, &capacityFake{capErr: wireCoded{"unavailable"}})
	if _, err := hostedCapacityPreflight(context.Background(), "prod", houndersEntities(), 1, &strings.Builder{}); err == nil {
		t.Fatal("a failed check must fail the deploy, not fall open")
	}
}

func TestHostedCapacityPreflight_PlanUnknownFailsWithRetry(t *testing.T) {
	withCapacityFake(t, &capacityFake{capacity: map[string]any{"allowed": false, "code": "DEPLOY_CAPACITY_CODE_PLAN_UNKNOWN", "reason": "lookup failed", "fix": "retry"}})
	_, err := hostedCapacityPreflight(context.Background(), "prod", houndersEntities(), 0, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "retry") || !strings.Contains(err.Error(), "PLAN_UNKNOWN") {
		t.Fatalf("err = %v", err)
	}
}

// The release deploy must refuse before ensureHostedReleaseBundle / promote:
// any other RPC reaching the fake fails the test.
func TestDispatchReleaseDeploy_RefusedBeforeAnySideEffect(t *testing.T) {
	f := &capacityFake{capacity: map[string]any{"allowed": false, "code": "DEPLOY_CAPACITY_CODE_NO_COMPUTE_PLAN", "reason": "no plan", "fix": "subscribe"}}
	withCapacityFake(t, f)
	dir := t.TempDir()
	fixture := filepath.Join(dir, "render.json")
	if err := os.WriteFile(fixture, contractJSON(t, houndersEntities()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)
	_, err := hostedCapacityPreflightForEnv(context.Background(), dir, "prod", false, &strings.Builder{})
	var refused *deploytarget.CapacityRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 2 {
		t.Errorf("calls = %v, want only the lookup and the check", f.calls)
	}
}

func decodeFake(reply any, out any) error {
	b, _ := json.Marshal(reply)
	return json.Unmarshal(b, out)
}

// ─── the REAL entry points ───────────────────────────────────────────────────
//
// Each drives the command a user types, with a REFUSED capacity answer, and
// asserts that no side-effecting seam ran: the control plane saw only the
// read-only lookup + check, and the build / bundle-record / promote seams were
// never reached.

var refusedCapacity = map[string]any{
	"allowed": false, "code": "DEPLOY_CAPACITY_CODE_NO_COMPUTE_PLAN",
	"reason": "this runs compute and the organization has no active compute plan",
	"fix":    "subscribe to a Reliant Compute plan in billing settings, then retry",
}

// trackSideEffects stubs every seam a build / record / promote reaches and
// returns a pointer to the names that fired.
func trackSideEffects(t *testing.T) *[]string {
	t.Helper()
	var fired []string
	boom := errors.New("side-effect seam reached")
	prevBuild, prevStorage, prevBundles, prevProv := runDeployBuild, checkBuildStorageFn, writeBundlesFn, captureReleaseProvenance
	runDeployBuild = func(context.Context, buildOptions) error { fired = append(fired, "build"); return boom }
	checkBuildStorageFn = func(string) error { fired = append(fired, "build-start"); return boom }
	writeBundlesFn = func(context.Context, string, []string, bundleBuildInputs) ([]bundleWriteOutcome, error) {
		fired = append(fired, "record-bundle")
		return nil, boom
	}
	captureReleaseProvenance = func(context.Context, string) release.Provenance {
		return release.Provenance{Commit: strings.Repeat("c", 40), Tree: strings.Repeat("a", 40)}
	}
	t.Cleanup(func() {
		runDeployBuild, checkBuildStorageFn, writeBundlesFn, captureReleaseProvenance = prevBuild, prevStorage, prevBundles, prevProv
	})
	return &fired
}

func assertRefusedBeforeSideEffects(t *testing.T, err error, fired *[]string, f *capacityFake) {
	t.Helper()
	var refused *deploytarget.CapacityRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want the capacity refusal", err)
	}
	if !strings.Contains(err.Error(), "no active compute plan") || !strings.Contains(err.Error(), "subscribe to a Reliant Compute plan") {
		t.Errorf("refusal lacks reason+fix:\n%s", err)
	}
	if len(*fired) != 0 {
		t.Errorf("side-effect seams fired after a refusal: %v", *fired)
	}
	for _, c := range f.calls {
		if !strings.HasSuffix(c, "ListEnvironments") && !strings.HasSuffix(c, "CheckDeployCapacity") {
			t.Errorf("control plane saw %s", c)
		}
	}
}

func TestEnvDeploy_NoVersion_RefusedBeforeBuild(t *testing.T) {
	planProject(t, hostedStaticPlanFixture)
	f := &capacityFake{capacity: refusedCapacity}
	withCapacityFake(t, f)
	fired := trackSideEffects(t)
	err := dispatchDeployCmd(context.Background(), "prod", deployCmdFlags{})
	assertRefusedBeforeSideEffects(t, err, fired, f)
}

func TestEnvDeploy_Promote_RefusedBeforeBundleAndPromotion(t *testing.T) {
	dir := planProject(t, hostedStaticPlanFixture)
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: pt\nmodule_path: example.com/pt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markServiceProject(t, dir) // deploy derives on for a service
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy", "kcl", "prod", "main.k"), []byte("# fixture; the render comes from FORGE_KCL_RENDER_FIXTURE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore(t, dir).CutRelease(ociRelease("v1.0.0", map[string]string{"api": sha("1")})); err != nil {
		t.Fatal(err)
	}
	f := &capacityFake{capacity: refusedCapacity}
	withCapacityFake(t, f)
	fired := trackSideEffects(t)
	cmdFlags := deployCmdFlags{}
	cmdFlags.promote.version = "v1.0.0"
	cmdFlags.promote.yes = true
	err := dispatchDeployCmd(context.Background(), "prod", cmdFlags)
	assertRefusedBeforeSideEffects(t, err, fired, f)
	if _, bound, _ := testLedger(t, dir).Bindings.Current(context.Background(), "prod"); bound {
		t.Error("a promotion was written after a refusal")
	}
}

func TestEnvBuildPush_RefusedBeforeBuild(t *testing.T) {
	planProject(t, hostedStaticPlanFixture)
	f := &capacityFake{capacity: refusedCapacity}
	withCapacityFake(t, f)
	fired := trackSideEffects(t)
	_, err := runBuildCommand(t, "prod", "--push", "--no-generate", "--tag", "t1")
	assertRefusedBeforeSideEffects(t, err, fired, f)
}

// --plan-only on the no-version deploy reaches the plan renderer with the
// pre-flight's verdict: OK + demand vs ceiling.
func TestPlanOnly_RendersCapacitySection(t *testing.T) {
	var out strings.Builder
	renderPromotePlanText(&out, promotePlan{Env: "prod", Capacity: &promotePlanCapacity{
		Verdict: deploytarget.CapacityVerdict{Allowed: true, HasComputePlan: true, CeilingCPU: 8000, CeilingMemory: 16 << 30, CeilingStorage: 100},
		Demand:  deploytarget.CapacityDemand{CPUMillicores: 1250, Workloads: 2},
	}})
	for _, want := range []string{"Capacity  OK", "demand    1250m CPU", "ceiling   8000m CPU"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	renderCapacitySection(&out, &promotePlanCapacity{Verdict: deploytarget.CapacityVerdict{Code: "NO_COMPUTE_PLAN", Reason: "no plan", Fix: "subscribe"}})
	for _, want := range []string{"REFUSED (NO_COMPUTE_PLAN): no plan", "fix       subscribe"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("refused section lacks %q:\n%s", want, out.String())
		}
	}
}

// A BUILD NEVER NEEDS A REACHABLE CONTROL PLANE (#404). The capacity pre-flight
// is an optimisation for `forge env build`, so when it cannot be DELIVERED — no
// credential, an unavailable control plane — the build warns and continues.
// An ANSWERED refusal still stops it: that is a fact about the org.
func TestBuildCapacityPreflight_UndeliverableWarnsAndContinues(t *testing.T) {
	for _, tc := range []struct {
		name string
		fake *capacityFake
		cred bool
	}{
		{name: "no credential", fake: &capacityFake{}, cred: false},
		{name: "unavailable", fake: &capacityFake{capErr: wireCoded{"unavailable"}}, cred: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCapacityFake(t, tc.fake)
			if !tc.cred {
				t.Setenv(cloud.DefaultTokenEnv, "")
				t.Setenv("FORGE_HOME", t.TempDir())
			}
			var out strings.Builder
			if _, err := buildCapacityPreflight(context.Background(), "prod", houndersEntities(), 1, &out); err != nil {
				t.Fatalf("an undeliverable pre-flight must not fail a build, got %v", err)
			}
			if !strings.Contains(out.String(), "capacity pre-flight skipped") {
				t.Errorf("want a skip warning, got %q", out.String())
			}
		})
	}
}

func TestBuildCapacityPreflight_RefusalStillFailsTheBuild(t *testing.T) {
	withCapacityFake(t, &capacityFake{capacity: map[string]any{
		"allowed": false, "code": "DEPLOY_CAPACITY_CODE_NO_COMPUTE_PLAN", "reason": "no plan", "fix": "subscribe",
	}})
	_, err := buildCapacityPreflight(context.Background(), "prod", houndersEntities(), 1, &strings.Builder{})
	var refused *deploytarget.CapacityRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *CapacityRefusedError", err)
	}
}
