package deploytarget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeCP is a control plane that answers with canned JSON and records every
// call in order. It round-trips request bodies through JSON so assertions see
// exactly the wire document, not the Go value.
type fakeCP struct {
	mu    sync.Mutex
	calls []fakeCall
	// status returns the GetStatus body for the nth GetStatus call.
	status func(n int) string
	envs   string
	proms  string
	nStat  int
}

type fakeCall struct {
	Proc string
	Body map[string]any
}

func (f *fakeCP) Call(_ context.Context, proc string, req, out any) error {
	raw, _ := json.Marshal(req)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Proc: proc, Body: body})
	f.mu.Unlock()
	short := proc[strings.LastIndex(proc, "/")+1:]
	var reply string
	switch short {
	case "EnsureEnvironment":
		// NO push base in the reply. The server does not advertise one;
		// forge composes it from the env's declaration and carries it on
		// the group (HostedTarget.PushBase).
		reply = `{"environment":{"id":"env-1","name":"prod","namespace":"env-env-1"},"created":true}`
	case "GetStatus":
		f.mu.Lock()
		f.nStat++
		n := f.nStat
		f.mu.Unlock()
		reply = f.status(n)
	case "ListEnvironments":
		reply = f.envs
	case "ListPromotions":
		reply = f.proms
	default:
		return fmt.Errorf("unexpected procedure %s", proc)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(reply), out)
}

func (f *fakeCP) procs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Proc[strings.LastIndex(c.Proc, "/")+1:])
	}
	return out
}

func readyStatus(digest string) func(int) string {
	return func(int) string {
		return fmt.Sprintf(`{"environmentVerdict":"DEPLOY_VERDICT_CONVERGING","deployments":[
		 {"deployment":{"id":"dep-api","name":"api","observed":{"state":"DEPLOY_OBSERVED_STATE_READY","imageDigest":%q}},"verdict":"DEPLOY_VERDICT_CONVERGING","desiredDigest":%q},
		 {"deployment":{"id":"dep-orders","name":"orders","observed":{"state":"DEPLOY_OBSERVED_STATE_READY"}},"verdict":"DEPLOY_VERDICT_CONVERGED"}]}`, digest, digest)
	}
}

// hostedGroup's declared push base. The group's images sit under it, so a
// test that does not care about the boundary check gets a passing one.
const testDeclaredPushBase = "ghcr.io/acme"

func hostedGroup(release string, digests map[string]string, resources v1alpha1.Resources) ServiceGroup {
	return hostedGroupWithPushBase(release, digests, resources, testDeclaredPushBase)
}

// hostedGroupWithPushBase is hostedGroup with the DECLARED base stated, for
// the tests that exercise checkImagePushBase. "" is an env whose org was not learned
// organization.
func hostedGroupWithPushBase(release string, digests map[string]string, resources v1alpha1.Resources, pushBase string) ServiceGroup {
	return ServiceGroup{
		Env:        "prod",
		ProviderID: HostedProviderID,
		Hosted:     &HostedTarget{Endpoint: "https://cp.example", Release: release, Digests: digests, PushBase: pushBase},
		Services: []ResolvedService{
			{Name: "api", Hosted: &HostedWorkload{Tier: HostedTierWorkload, Workload: &v1alpha1.WorkloadSpec{
				Kind: v1alpha1.KindService, Image: "ghcr.io/acme/api:v1", Args: []string{"api"},
				Ports: []v1alpha1.Port{{Name: "http", Port: 8080, Expose: true}}, Probes: &v1alpha1.Probes{}, Resources: resources,
			}}},
			{Name: "orders", Hosted: &HostedWorkload{Tier: HostedTierDatabase, Database: &v1alpha1.ManagedDatabaseSpec{}}},
		},
	}
}

// TestHostedDeployRecordsTheBundleAndWritesNoDeployment pins the contract: a
// hosted deploy ensures the environment, records the bundle through the
// RecordBundle hook, then waits — and makes NO EnsureDeployment or
// PublishDeploymentConfig call. Mutation: re-adding either RPC fails the call
// list; dropping the hook fails the recorded flag.
func TestHostedDeployRecordsTheBundleAndWritesNoDeployment(t *testing.T) {
	cp := &fakeCP{status: readyStatus(digestA)}
	var envSeen string
	recordedFor := ""
	var outcomes []cluster.RolloutObservation
	p := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		OnEnvironment: func(id string) { envSeen = id },
		RecordBundle:  func(_ context.Context, id string) (string, error) { recordedFor = id; return "", nil },
		OnRollout:     func(o cluster.RolloutObservation) { outcomes = append(outcomes, o) }}
	if err := p.Deploy(context.Background(), hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{})); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	want := []string{"EnsureEnvironment", "GetStatus"}
	if got := cp.procs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("call order = %v, want %v (no per-deployment RPC)", got, want)
	}
	if recordedFor != "env-1" {
		t.Errorf("the bundle was recorded for %q, want env-1", recordedFor)
	}
	if got := cp.calls[1].Body["environmentId"]; got != "env-1" {
		t.Errorf("GetStatus must be env-scoped, got %v", cp.calls[1].Body)
	}
	if envSeen != "env-1" {
		t.Errorf("OnEnvironment got %q", envSeen)
	}
	if len(outcomes) != 2 || outcomes[0].State != cluster.RolloutStateReady {
		t.Errorf("rollout outcomes = %+v", outcomes)
	}
}

// TestHostedRecordsPinTheBoundDigestAndCarryNoIdentity: what a bundle carries
// is the plan's pinned spec, as a forge.dev record with NO namespace and NO
// identity labels — those are the platform's to stamp.
func TestHostedRecordsPinTheBoundDigestAndCarryNoIdentity(t *testing.T) {
	recs, err := HostedRecords(hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{}))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, r := range recs {
		kinds[r.Name] = r.Kind
		doc := string(r.YAML)
		for _, forbidden := range []string{"namespace:", "forge.dev/org-id", "forge.dev/deployment-id", "forge.dev/environment-id", "status:"} {
			if strings.Contains(doc, forbidden) {
				t.Errorf("%s record carries %q:\n%s", r.Name, forbidden, doc)
			}
		}
	}
	if kinds["api"] != "Workload" || kinds["orders"] != "ManagedDatabase" {
		t.Errorf("kinds = %v, want api=Workload orders=ManagedDatabase", kinds)
	}
	for _, r := range recs {
		if r.Name == "api" && !strings.Contains(string(r.YAML), "ghcr.io/acme/api@"+digestA) {
			t.Errorf("api record is not pinned to the bound digest:\n%s", r.YAML)
		}
	}
}

// TestHostedOffBandRefusedWithZeroRPCs: a spec off the 4 GiB/vCPU band is
// refused BEFORE any write. Mutation: moving CheckShapeBand after
// EnsureEnvironment makes the call count non-zero.
func TestHostedOffBandRefusedWithZeroRPCs(t *testing.T) {
	cp := &fakeCP{status: readyStatus(digestA)}
	offBand := v1alpha1.Resources{CPURequestMillicores: 500, MemoryRequestBytes: 1 << 30}
	err := HostedProvider{Client: cp}.Deploy(context.Background(), hostedGroup("v1", map[string]string{"api": digestA}, offBand))
	if err == nil || !strings.Contains(err.Error(), "shape band") {
		t.Fatalf("err = %v, want a shape-band refusal", err)
	}
	if n := len(cp.procs()); n != 0 {
		t.Fatalf("%d RPC(s) made before the refusal: %v", n, cp.procs())
	}
}

// TestHostedImagePushBase: the env's DECLARED push base decides, BEFORE any
// EnsureDeployment or publish, whether a bound backend image is one the
// platform will publish.
//
// The base is forge's own composition from the declaration, carried on the
// group — the server advertises none. This check is a PRE-FLIGHT for the real
// boundary (ociregistry.Admit and the registry realm), and it earns its keep
// by costing zero writes when it refuses.
//
// MUTATIONS VERIFIED RED:
//   - deleting the checkImagePushBase call from Deploy → "foreign" and
//     "no base" make EnsureDeployment/Publish calls;
//   - moving it after p.publish's EnsureDeployment loop → non-zero writes;
//   - a bare strings.HasPrefix(repo, base) without the "/" → the
//     adjacent-prefix case ("ghcr.io/acme-evil") is admitted;
//   - dropping the `base == ""` refusal → "no base" publishes.
func TestHostedImagePushBase(t *testing.T) {
	// A "write" is the one thing that reaches the platform's ledger: the
	// bundle record. (EnsureEnvironment precedes the push-base check by
	// design and is idempotent.)
	recorded := map[*fakeCP]int{}
	writes := func(cp *fakeCP) []string {
		out := make([]string, recorded[cp])
		for i := range out {
			out[i] = "RecordBundle"
		}
		return out
	}
	deploy := func(base string) (*fakeCP, error) {
		cp := &fakeCP{status: readyStatus(digestA)}
		err := HostedProvider{Client: cp, PollInterval: time.Millisecond,
			RecordBundle: func(context.Context, string) (string, error) { recorded[cp]++; return "", nil }}.Deploy(context.Background(),
			hostedGroupWithPushBase("v1", map[string]string{"api": digestA}, v1alpha1.Resources{}, base))
		return cp, err
	}

	t.Run("foreign registry refused with zero writes", func(t *testing.T) {
		cp, err := deploy("registry.reliant.dev/org-1")
		if err == nil {
			t.Fatal("an image outside the push base was published")
		}
		for _, want := range []string{
			"ghcr.io/acme/api@" + digestA, // the image
			"registry.reliant.dev/org-1",  // the base
			// The fix, as of ADR-0003 F1: the author does not push this
			// image anywhere themselves and does not name the registry —
			// they drop the host and forge composes the platform's base.
			"drop the registry host",
			"registry.reliant.dev/org-1/api",
			"forge env deploy",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal does not name %q:\n%v", want, err)
			}
		}
		if w := writes(cp); len(w) != 0 {
			t.Fatalf("writes before the push-base refusal: %v", w)
		}
	})

	t.Run("adjacent prefix refused", func(t *testing.T) {
		cp, err := deploy("ghcr.io/ac")
		if err == nil || len(writes(cp)) != 0 {
			t.Fatalf("ghcr.io/acme/api admitted under base ghcr.io/ac (err=%v, writes=%v)", err, writes(cp))
		}
	})

	// An env whose credential's org was not learned composes no base, so there is no
	// address any image could be under. Refused, and the refusal names the
	// credential to supply rather than a server setting the author cannot reach.
	t.Run("no resolved push base refused", func(t *testing.T) {
		// The remedy names the credential source the HOST offers: with a
		// credential helper configured (Reliant sets one) it names the
		// helper instead of `forge login`. Pin the bare-host case, so the
		// verdict does not depend on the machine running the test.
		t.Setenv(cloudcred.HelperEnv, "")
		cp, err := deploy("")
		if err == nil || !strings.Contains(err.Error(), "resolved no image push base") {
			t.Fatalf("err = %v, want the no-push-base refusal", err)
		}
		if !strings.Contains(err.Error(), "forge login") {
			t.Errorf("the refusal must name the credential remedy:\n%v", err)
		}
		if w := writes(cp); len(w) != 0 {
			t.Fatalf("writes with no push base: %v", w)
		}
	})

	t.Run("in-base image accepted", func(t *testing.T) {
		cp, err := deploy("ghcr.io/acme")
		if err != nil {
			t.Fatalf("an image under the push base was refused: %v", err)
		}
		if w := writes(cp); len(w) != 1 {
			t.Fatalf("writes = %v, want exactly one bundle record (the bundle carries the tiers)", w)
		}
	})
}

// TestHostedForgeBuiltBackendPinsTheRecordedRegistry: a backend whose image
// THIS project builds declares it registry-less (`image = "api"`): the registry
// is declared once, on the env's forge.ControlPlane, and `forge build <env>
// --push` records where the bytes went. The pin must use that record.
// Before, the pin re-derived the repository from the bare spec image and the
// deploy refused "hounders@sha256:… must name its registry host explicitly".
func TestHostedForgeBuiltBackendPinsTheRecordedRegistry(t *testing.T) {
	// The declared base is where the recorded registry points, since that is
	// the address the build pushed to and the one the pre-flight judges.
	group := func(registries map[string]string) ServiceGroup {
		g := hostedGroupWithPushBase("v1", map[string]string{"api": digestA}, v1alpha1.Resources{}, "localhost:5051/org-1")
		g.Hosted.Registries = registries
		g.Services[0].Hosted.Workload.Image = "api"
		return g
	}

	t.Run("pinned under the release's registry", func(t *testing.T) {
		want := "localhost:5051/org-1/api@" + digestA
		if got := recordedBackendImage(t, group(map[string]string{"api": "localhost:5051/org-1"}), "api"); got != want {
			t.Fatalf("published image = %q, want %q", got, want)
		}
	})

	t.Run("no recorded registry is refused with the fix", func(t *testing.T) {
		cp := &fakeCP{status: readyStatus(digestA)}
		err := HostedProvider{Client: cp, PollInterval: time.Millisecond}.Deploy(context.Background(), group(nil))
		// The fix is `forge env deploy <env>`, not `--push` and not
		// `--no-build`: this release recorded no registry for the artifact,
		// so there are no pushed bytes to cut a release over, and the only
		// honest remedy is the one verb that builds, pushes and cuts.
		if err == nil || !strings.Contains(err.Error(), "forge env deploy prod") {
			t.Fatalf("err = %v, want a refusal naming forge env deploy prod", err)
		}
		if len(cp.procs()) != 0 {
			t.Fatalf("RPCs made: %v", cp.procs())
		}
	})

	t.Run("an explicit registry in the spec wins", func(t *testing.T) {
		g := group(map[string]string{"api": "localhost:5051/org-1"})
		g.Services[0].Hosted.Workload.Image = "ghcr.io/acme/api:v1"
		// An explicit host is used verbatim, so the declared base must be
		// the one that host sits under or the pre-flight refuses it.
		g.Hosted.PushBase = testDeclaredPushBase
		if got := recordedBackendImage(t, g, "api"); got != "ghcr.io/acme/api@"+digestA {
			t.Fatalf("published image = %q: a declared registry was overridden", got)
		}
	})
}

// TestHostedUnboundRefused: no promoted release means no digest to ship; the
// refusal names the deploy fix and makes no call.
func TestHostedUnboundRefused(t *testing.T) {
	cp := &fakeCP{}
	err := HostedProvider{Client: cp}.Deploy(context.Background(), hostedGroup("", nil, v1alpha1.Resources{}))
	if err == nil || !strings.Contains(err.Error(), "forge env deploy") {
		t.Fatalf("err = %v, want the forge env deploy fix", err)
	}
	if len(cp.procs()) != 0 {
		t.Fatalf("RPCs made for an unbound env: %v", cp.procs())
	}
}

// TestHostedReleaseMissingArtifactRefused: a release that pins no digest for a
// backend's image refuses, naming the artifact.
func TestHostedReleaseMissingArtifactRefused(t *testing.T) {
	cp := &fakeCP{}
	err := HostedProvider{Client: cp}.Deploy(context.Background(), hostedGroup("v1", map[string]string{"other": digestA}, v1alpha1.Resources{}))
	if err == nil || !strings.Contains(err.Error(), `pins no artifact "api"`) {
		t.Fatalf("err = %v", err)
	}
	if len(cp.procs()) != 0 {
		t.Fatalf("RPCs made: %v", cp.procs())
	}
}

// TestHostedReadinessTimeoutIsNotSuccess: a workload that never becomes ready
// fails the deploy as TIMED OUT. Mutation: returning nil at the deadline makes
// this pass wrongly — the test fails then.
func TestHostedReadinessTimeoutIsNotSuccess(t *testing.T) {
	cp := &fakeCP{status: func(int) string {
		return `{"environmentVerdict":"DEPLOY_VERDICT_CONVERGING","deployments":[
		 {"deployment":{"id":"dep-api","name":"api","observed":{"state":"DEPLOY_OBSERVED_STATE_PROGRESSING","imageDigest":""}},"verdict":"DEPLOY_VERDICT_CONVERGING"},
		 {"deployment":{"id":"dep-orders","name":"orders","observed":{"state":"DEPLOY_OBSERVED_STATE_READY"}},"verdict":"DEPLOY_VERDICT_CONVERGED"}]}`
	}}
	var states []cluster.RolloutState
	p := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		Rollout:   cluster.RolloutPolicy{Timeout: 20 * time.Millisecond},
		OnRollout: func(o cluster.RolloutObservation) { states = append(states, o.State) }}
	err := p.Deploy(context.Background(), hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{}))
	if err == nil || !strings.Contains(err.Error(), "TIMED OUT") || !strings.Contains(err.Error(), "api: verdict converging, observed progressing") {
		t.Fatalf("err = %v, want TIMED OUT naming api", err)
	}
	if len(states) != 2 || states[0] != cluster.RolloutStateTimedOut || states[1] != cluster.RolloutStateReady {
		t.Fatalf("outcomes = %v, want [timed_out ready]", states)
	}
}

// TestHostedReadyNeedsTheDesiredDigest: observed READY on the OLD digest is a
// rollout still in flight, not readiness.
func TestHostedReadyNeedsTheDesiredDigest(t *testing.T) {
	st := wireDeploymentStatus{Deployment: wireDeployment{Observed: &wireObserved{State: wireObservedReady, ImageDigest: digestB}}, Verdict: "DEPLOY_VERDICT_CONVERGING"}
	if hostedDeploymentReady(st, digestA) {
		t.Fatal("READY on a stale digest counted as ready")
	}
	st.Deployment.Observed.ImageDigest = digestA
	if !hostedDeploymentReady(st, digestA) {
		t.Fatal("READY on the desired digest not counted as ready")
	}
}

// TestHostedObserveMapping pins the observation table.
func TestHostedObserveMapping(t *testing.T) {
	cases := []struct {
		name    string
		w       HostedWorkloadStatus
		present bool
		want    Health
	}{
		{"ready converged", HostedWorkloadStatus{ObservedState: "ready", Verdict: "converged", ObservedDigest: digestA}, true, HealthHealthy},
		{"ready converging", HostedWorkloadStatus{ObservedState: "ready", Verdict: "converging"}, true, HealthHealthy},
		{"progressing", HostedWorkloadStatus{ObservedState: "progressing", Verdict: "converging"}, true, HealthDegraded},
		{"pending", HostedWorkloadStatus{ObservedState: "pending", Verdict: "unknown"}, true, HealthDegraded},
		{"degraded", HostedWorkloadStatus{ObservedState: "degraded", Verdict: "degraded", LastError: "CrashLoopBackOff"}, true, HealthDegraded},
		{"suspended", HostedWorkloadStatus{ObservedState: "suspended"}, true, HealthAbsent},
		{"unknown", HostedWorkloadStatus{ObservedState: "unknown"}, true, HealthUnknown},
		{"absent", HostedWorkloadStatus{}, false, HealthAbsent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := observedItemFor("api", tc.w, tc.present)
			if item.Health != tc.want {
				t.Fatalf("health = %v, want %v", item.Health, tc.want)
			}
			if item.Health != HealthHealthy && item.Detail == "" {
				t.Fatal("non-healthy item without Detail")
			}
			if tc.w.LastError != "" && !strings.Contains(item.Detail, tc.w.LastError) {
				t.Fatalf("detail %q lost the last error", item.Detail)
			}
		})
	}
}

// TestHostedObserveReadsEnvStatus: end to end through ReadHostedStatus.
func TestHostedObserveReadsEnvStatus(t *testing.T) {
	cp := &fakeCP{envs: `{"environments":[{"id":"env-1","name":"prod"}]}`, status: readyStatus(digestA)}
	obs, err := HostedProvider{Client: cp}.Observe(context.Background(), hostedGroup("v1", nil, v1alpha1.Resources{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Items) != 2 || obs.Items[0].Health != HealthHealthy || obs.Items[0].Digest != digestA {
		t.Fatalf("items = %+v", obs.Items)
	}
	// Never deployed: absent, a real answer, no error.
	cp2 := &fakeCP{envs: `{"environments":[]}`}
	obs, err = HostedProvider{Client: cp2}.Observe(context.Background(), hostedGroup("v1", nil, v1alpha1.Resources{}))
	if err != nil || obs.Items[0].Health != HealthAbsent {
		t.Fatalf("never-deployed: %+v %v", obs.Items, err)
	}
	// A failed read is unknown with the reason, and an error.
	cp3 := errCaller{}
	obs, err = HostedProvider{Client: cp3}.Observe(context.Background(), hostedGroup("v1", nil, v1alpha1.Resources{}))
	if err == nil || obs.Items[0].Health != HealthUnknown || !strings.Contains(obs.Items[0].Detail, "boom") {
		t.Fatalf("failed read: %+v %v", obs.Items, err)
	}
}

type errCaller struct{}

func (errCaller) Call(context.Context, string, any, any) error { return errors.New("boom") }

// TestHostedArtifactName pins the key the cut and the deploy share.
func TestHostedArtifactName(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/acme/api:v1":            "api",
		"ghcr.io/acme/api@" + digestA:    "api",
		"localhost:5051/e2eh/run/whoami": "whoami",
		"registry.local:5000/x/y:t":      "y",
	} {
		if got := HostedArtifactName(in); got != want {
			t.Errorf("HostedArtifactName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := HostedImageRepository("localhost:5051/a/b:tag"); got != "localhost:5051/a/b" {
		t.Errorf("repository = %q", got)
	}
}

// recordedBackendImage is the spec.image the bundle's record for name carries.
func recordedBackendImage(t *testing.T, g ServiceGroup, name string) string {
	t.Helper()
	recs, err := HostedRecords(g)
	if err != nil {
		t.Fatalf("HostedRecords: %v", err)
	}
	for _, r := range recs {
		if r.Name != name {
			continue
		}
		for _, line := range strings.Split(string(r.YAML), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "image: "); ok {
				return rest
			}
		}
	}
	t.Fatalf("no record named %q", name)
	return ""
}
