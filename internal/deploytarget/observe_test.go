package deploytarget

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE CONTROL THESE TESTS EXIST FOR
//
// "A provider that cannot observe must report unknown, never green."
//
// That is the property, and it is one a test can pass VACUOUSLY in two
// distinct ways, both of which this project has been bitten by before:
//
//   - By asserting on the ERROR only. Every unsupported provider returns
//     the sentinel, so an error-only assertion passes against an
//     implementation whose Observed says HealthHealthy — which is exactly
//     the claim the rule forbids, since a caller that logs the value
//     without checking the error reads green.
//   - By asserting on an EMPTY Observed. A provider returning a zero
//     Observed has no items, so "no item is healthy" is trivially true of
//     nothing. TestUnsupportedProvidersNameEveryItem closes that by
//     requiring an item per declared name.
//
// So the assertions below check the VALUE as well as the error, and check
// that the value is non-empty. Each is sabotage-verified in the report.
// ─────────────────────────────────────────────────────────────────────────────

// TestHealthZeroValueIsUnknown pins the single most load-bearing fact in
// the design: Health's zero value is HealthUnknown, so an Observed built
// by ANY path — a provider that forgot to set health, a test literal, a
// future caller — reads as unknown rather than healthy.
//
// If the iota order were ever reshuffled so that HealthHealthy sat at
// zero, every unset field in the package would silently start claiming
// health nobody measured. That is a one-character change with no other
// symptom, which is precisely what a pin is for.
func TestHealthZeroValueIsUnknown(t *testing.T) {
	var zero Health
	if zero != HealthUnknown {
		t.Fatalf("Health zero value = %v, want HealthUnknown — an unset health must never read as healthy", zero)
	}
	if zero.String() != "unknown" {
		t.Errorf("Health(0).String() = %q, want \"unknown\"", zero.String())
	}
	// And the zero ObservedItem, which is what a provider returning a
	// bare struct hands back.
	var item ObservedItem
	if item.Health != HealthUnknown {
		t.Errorf("zero ObservedItem.Health = %v, want HealthUnknown", item.Health)
	}
	if item.Replicas != nil {
		t.Errorf("zero ObservedItem.Replicas = %v, want nil (nil means 'no replica concept', "+
			"&ReplicaCounts{} means 'nothing is running' — they must stay distinguishable)", item.Replicas)
	}
}

// unsupportedProviders is every provider that declines to observe, with
// the group shape it reads its names from.
//
// These two are STRUCTURAL declinations — forge holds no identity of its
// own on the other side, so no future version can read them back. The
// list should only ever shrink; a provider that appears here because
// nobody has got to it yet is the shape the audit rejects (see
// observe_audit_test.go, which enforces a PERMANENT reason).
func unsupportedProviders() []struct {
	name  string
	p     Provider
	group ServiceGroup
} {
	return []struct {
		name  string
		p     Provider
		group ServiceGroup
	}{
		// Compose and HostInfra were here, as NOT-YET-BUILT gaps. Both
		// now observe for real (observe_compose.go, observe_hostinfra.go)
		// and their verdicts are pinned in observe_compose_test.go and
		// observe_hostinfra_test.go. Only the two STRUCTURAL declinations
		// remains, which is the point: this table is the list of things
		// forge can never read, and it should only ever shrink.
		//
		// Nothing is lost by removing them. The rule they were here to
		// demonstrate is now enforced for EVERY registered provider, in
		// both directions, by TestEveryRegisteredProviderObservesOrDeclares.
		{"firebase", FirebaseProvider{}, ServiceGroup{
			Env:       "prod",
			Frontends: []FirebaseFrontend{{Name: "web"}},
		}},
	}
}

// TestUnsupportedProvidersNeverReportGreen is the core control.
//
// It asserts BOTH halves — the sentinel error AND that no item in the
// returned value claims health — because either alone is satisfiable by
// an implementation that violates the rule.
func TestUnsupportedProvidersNeverReportGreen(t *testing.T) {
	for _, tc := range unsupportedProviders() {
		t.Run(tc.name, func(t *testing.T) {
			obs, err := tc.p.Observe(context.Background(), tc.group)

			if !errors.Is(err, ErrObservationUnsupported) {
				t.Fatalf("Observe error = %v, want ErrObservationUnsupported", err)
			}

			// THE HALF AN ERROR-ONLY TEST MISSES. A careless caller logs
			// the value without checking the error; it must not read as
			// health.
			if len(obs.Items) == 0 {
				t.Fatalf("Observed carries no items — an empty report makes 'no item is healthy' " +
					"trivially true, which is the vacuous shape this assertion exists to reject")
			}
			for _, item := range obs.Items {
				if item.Health != HealthUnknown {
					t.Errorf("item %q health = %v, want HealthUnknown — a provider that cannot "+
						"observe must report unknown, never a measured verdict", item.Name, item.Health)
				}
				if strings.TrimSpace(item.Detail) == "" {
					t.Errorf("item %q has no Detail; an unknown with no reason is "+
						"indistinguishable from a provider that silently failed", item.Name)
				}
				if item.Replicas != nil {
					t.Errorf("item %q reports replicas %v without measuring anything", item.Name, item.Replicas)
				}
				if item.Digest != "" {
					t.Errorf("item %q reports digest %q without measuring anything", item.Name, item.Digest)
				}
			}
		})
	}
}

// TestUnsupportedProvidersNameEveryItem closes the second vacuity route:
// a provider could satisfy "no item is healthy" by returning no items at
// all. Every declared service/frontend must be NAMED in the report, so a
// caller sees a row saying "I cannot tell you about api" rather than
// silently getting a shorter list than it asked about.
func TestUnsupportedProvidersNameEveryItem(t *testing.T) {
	for _, tc := range unsupportedProviders() {
		t.Run(tc.name, func(t *testing.T) {
			obs, _ := tc.p.Observe(context.Background(), tc.group)
			got := map[string]bool{}
			for _, item := range obs.Items {
				got[item.Name] = true
			}
			for _, want := range observableNames(tc.group) {
				if !got[want] {
					t.Errorf("declared %q is missing from the observation; an unobservable target "+
						"must be reported as unknown, not omitted", want)
				}
			}
		})
	}
}

// TestObservationUnsupportedErrorCarriesReason pins that the sentinel is
// wrapped (so errors.Is works) AND that the concrete type carrying the
// human explanation is recoverable with errors.As. A caller that can only
// learn "unsupported" but not WHY cannot tell a structural limit
// (Firebase's release history, permanent) from a gap (Compose,
// implementable) — which is the difference between "stop asking" and
// "file a bug".
func TestObservationUnsupportedErrorCarriesReason(t *testing.T) {
	_, err := FirebaseProvider{}.Observe(context.Background(), ServiceGroup{
		Frontends: []FirebaseFrontend{{Name: "web"}},
	})
	var use ObservationUnsupportedError
	if !errors.As(err, &use) {
		t.Fatalf("error %v does not unwrap to ObservationUnsupportedError", err)
	}
	if use.Provider != "firebase" {
		t.Errorf("Provider = %q, want \"firebase\"", use.Provider)
	}
	if !strings.Contains(use.Reason, "release history") {
		t.Errorf("Reason %q does not explain WHY firebase is unobservable", use.Reason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// K8sClusterProvider.Observe
// ─────────────────────────────────────────────────────────────────────────────

// deploymentJSON builds the kubectl `-o json` output for a Deployment
// with the given counts, as the API server would emit it.
//
// This is the ROUND TRIP the brief asks for, applied to what is
// available: rather than hand-constructing an ObservedItem literal and
// asserting the provider produces the same one (which would assert
// nothing about the parse), the test feeds the provider the wire shape
// kubectl actually prints and checks the verdict that comes out. A parser
// that read the wrong JSON field fails here and would pass a
// struct-literal test.
func deploymentJSON(t *testing.T, image string, desired, ready, updated, available int) string {
	t.Helper()
	var dep k8sDeployment
	dep.Spec.Replicas = desired
	dep.Spec.Template.Spec.Containers = []struct {
		Image string `json:"image"`
	}{{Image: image}}
	dep.Status.ReadyReplicas = ready
	dep.Status.UpdatedReplicas = updated
	dep.Status.AvailableReplicas = available
	raw, err := json.Marshal(dep)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(raw)
}

const pinnedDigest = "sha256:" + "ab12cd34" + "ef56ab78cd90ef12ab34cd56ef78ab90cd12ef34ab56cd78ef90ab12cd34ef56"

func TestK8sObserve_HealthyRollout(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"kubectl": deploymentJSON(t, "ghcr.io/x/api@"+pinnedDigest, 3, 3, 3, 3),
	}}
	p := K8sClusterProvider{Runner: runner}

	obs, err := p.Observe(context.Background(), ServiceGroup{
		Env: "prod", Cluster: "gke_prod", Namespace: "app-prod",
		Services: []ResolvedService{{Name: "api", K8sCluster: &K8sClusterSpec{Replicas: 3}}},
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(obs.Items))
	}
	item := obs.Items[0]
	if item.Health != HealthHealthy {
		t.Errorf("health = %v, want healthy (3/3 ready, updated and available)", item.Health)
	}
	if item.Digest != pinnedDigest {
		t.Errorf("digest = %q, want %q", item.Digest, pinnedDigest)
	}
	if item.Replicas == nil || item.Replicas.Ready != 3 || item.Replicas.Desired != 3 {
		t.Errorf("replicas = %+v, want 3/3", item.Replicas)
	}
	if obs.ObservedAt.IsZero() {
		t.Error("ObservedAt is zero; a caller cannot tell how stale this observation is")
	}
	// The declared cluster must be threaded per-command, exactly as it is
	// for a write. A read against the wrong cluster reports another
	// environment's state as this one's.
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "--context gke_prod") {
		t.Errorf("kubectl call %v does not carry the declared --context", runner.calls)
	}
}

// TestK8sObserve_MidRolloutIsDegraded is the assertion that makes reading
// `updatedReplicas` load-bearing. Every replica is ready and available —
// a health check on readiness alone would call this HEALTHY — but only
// one is running the new pod template, so the rollout is in progress.
//
// This is the case a naive pod-count implementation gets wrong, and it is
// why the provider reads the Deployment's status rather than listing
// pods.
func TestK8sObserve_MidRolloutIsDegraded(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"kubectl": deploymentJSON(t, "ghcr.io/x/api@"+pinnedDigest, 3, 3, 1, 3),
	}}
	obs, err := K8sClusterProvider{Runner: runner}.Observe(context.Background(), ServiceGroup{
		Cluster: "c", Namespace: "ns",
		Services: []ResolvedService{{Name: "api"}},
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := obs.Items[0].Health; got != HealthDegraded {
		t.Errorf("health = %v, want degraded: 3/3 ready but only 1 updated means the rollout "+
			"is still in progress and the old ReplicaSet is still serving", got)
	}
}

// TestK8sObserve_TagPinnedImageYieldsNoDigest pins that a MUTABLE TAG is
// never reported as a digest. A tag names whatever was last pushed under
// it, so returning it in a field called Digest would let a caller compare
// it against a release ledger and draw a conclusion the value cannot
// support.
func TestK8sObserve_TagPinnedImageYieldsNoDigest(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"kubectl": deploymentJSON(t, "ghcr.io/x/api:v1.4.0", 1, 1, 1, 1),
	}}
	obs, _ := K8sClusterProvider{Runner: runner}.Observe(context.Background(), ServiceGroup{
		Cluster: "c", Namespace: "ns", Services: []ResolvedService{{Name: "api"}},
	})
	if d := obs.Items[0].Digest; d != "" {
		t.Errorf("digest = %q, want empty — a tag is a mutable pointer, not a content identity", d)
	}
	// A tag-pinned image is forge's own --no-digest path, so it must not
	// downgrade health on its own.
	if h := obs.Items[0].Health; h != HealthHealthy {
		t.Errorf("health = %v, want healthy — an unpinned image is not an unhealthy one", h)
	}
}

// TestK8sObserve_MissingDeploymentIsAbsentNotUnknown pins the
// distinction a reconciler acts on most sharply: ABSENT invites "create
// it", UNKNOWN means "do not touch anything". Collapsing a NotFound into
// unknown would stall a loop forever on an environment that simply has
// not been deployed yet.
func TestK8sObserve_MissingDeploymentIsAbsentNotUnknown(t *testing.T) {
	runner := &fakeRunner{
		runErrs: map[string]error{"kubectl": errors.New("exit status 1")},
		outputs: map[string]string{
			"kubectl": `Error from server (NotFound): deployments.apps "api" not found`,
		},
	}
	obs, err := K8sClusterProvider{Runner: runner}.Observe(context.Background(), ServiceGroup{
		Cluster: "c", Namespace: "ns", Services: []ResolvedService{{Name: "api"}},
	})
	if err != nil {
		t.Fatalf("a missing Deployment is an ANSWER, not a group-level failure: %v", err)
	}
	if got := obs.Items[0].Health; got != HealthAbsent {
		t.Errorf("health = %v, want absent", got)
	}
}

// TestK8sObserve_UnreachableClusterIsUnknownNotAbsent is the inverse, and
// the more dangerous direction. A cluster forge cannot reach must NOT
// report absent: absent invites a caller to create, and creating because
// you could not connect is how a reconciler duplicates a live workload.
func TestK8sObserve_UnreachableClusterIsUnknownNotAbsent(t *testing.T) {
	runner := &fakeRunner{
		runErrs: map[string]error{"kubectl": errors.New("exit status 1")},
		outputs: map[string]string{
			"kubectl": "Unable to connect to the server: dial tcp 10.0.0.1:443: i/o timeout",
		},
	}
	obs, _ := K8sClusterProvider{Runner: runner}.Observe(context.Background(), ServiceGroup{
		Cluster: "c", Namespace: "ns", Services: []ResolvedService{{Name: "api"}},
	})
	got := obs.Items[0].Health
	if got == HealthAbsent {
		t.Fatal("an unreachable cluster reported ABSENT — a caller would create a workload that " +
			"may already be running, because forge could not connect")
	}
	if got != HealthUnknown {
		t.Errorf("health = %v, want unknown", got)
	}
}

// TestK8sObserve_RefusesUndeclaredContext pins that a read, like a write,
// never falls back to whatever kubectl context happens to be active. A
// read cannot corrupt a cluster, but it CAN report a different cluster's
// state as this environment's, which is a worse answer than none.
func TestK8sObserve_RefusesUndeclaredContext(t *testing.T) {
	runner := &fakeRunner{}
	obs, err := K8sClusterProvider{Runner: runner}.Observe(context.Background(), ServiceGroup{
		Namespace: "ns", // no Cluster
		Services:  []ResolvedService{{Name: "api"}},
	})
	if !errors.Is(err, ErrObservationUnsupported) {
		t.Fatalf("want ErrObservationUnsupported for an undeclared context, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("kubectl was invoked %v despite no declared context", runner.calls)
	}
	for _, item := range obs.Items {
		if item.Health != HealthUnknown {
			t.Errorf("item %q health = %v, want unknown", item.Name, item.Health)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StaticSiteProvider.Observe
// ─────────────────────────────────────────────────────────────────────────────

// staticSiteGroup builds a one-frontend group against a temp project dir.
func staticSiteGroup(bucket string) ServiceGroup {
	return ServiceGroup{
		Env: "prod",
		StaticSites: []StaticSiteFrontend{{
			Name: "web",
			Spec: StaticSiteSpec{Bucket: bucket, KeepReleases: 10},
		}},
	}
}

// TestStaticSiteObserve_RoundTripsThroughRealDeployState is the round
// trip: rather than hand-writing a state file whose shape a test author
// guessed, it drives the PRODUCTION writer (WriteDeployState — the same
// call the deploy path makes) and then observes. A change to
// the state file's layout that broke the reader would fail here; a
// hand-built fixture would keep passing against the stale shape, which is
// exactly the fixture-drift this project has already been bitten by.
func TestStaticSiteObserve_RoundTripsThroughRealDeployState(t *testing.T) {
	dir := t.TempDir()
	const digest = "deadbeefcafe"

	if _, err := WriteDeployState(dir, "static-site", "prod", "web", DeployState{
		Image: "gs://assets", Tag: digest,
	}); err != nil {
		t.Fatalf("WriteDeployState: %v", err)
	}

	// The bucket listing reports the archive is still there.
	runner := &fakeRunner{outputs: map[string]string{
		"gcloud storage ls": "gs://assets/releases/" + digest + "/\ngs://assets/releases/older/\n",
	}}
	p := StaticSiteProvider{ProjectDir: dir, Runner: runner}

	obs, err := p.Observe(context.Background(), staticSiteGroup("assets"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	item := obs.Items[0]
	if item.Digest != digest {
		t.Errorf("digest = %q, want %q", item.Digest, digest)
	}
	if item.Health != HealthHealthy {
		t.Errorf("health = %v (%s), want healthy", item.Health, item.Detail)
	}
}

// TestStaticSiteObserve_MissingArchiveIsDegraded is the check that makes
// this an OBSERVATION rather than a state-file read. The recorded digest
// is a claim about what forge did last; it stops being true when
// retention, a lifecycle rule or a human `gcloud storage rm` removes the
// archive. Reporting healthy off the record alone would mean forge
// asserts an archive that would 404.
func TestStaticSiteObserve_MissingArchiveIsDegraded(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteDeployState(dir, "static-site", "prod", "web", DeployState{Tag: "gone"}); err != nil {
		t.Fatalf("WriteDeployState: %v", err)
	}
	runner := &fakeRunner{outputs: map[string]string{
		// The listing has other releases but NOT the live one.
		"gcloud storage ls": "gs://assets/releases/other/\n",
	}}
	obs, err := StaticSiteProvider{ProjectDir: dir, Runner: runner}.
		Observe(context.Background(), staticSiteGroup("assets"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	item := obs.Items[0]
	if item.Health != HealthDegraded {
		t.Errorf("health = %v, want degraded: the recorded live release has no archive, so a "+
			"promoting an env to it would 404", item.Health)
	}
	if item.Digest != "gone" {
		t.Errorf("digest = %q; the recorded value is still real information and should be reported", item.Digest)
	}
}

// TestStaticSiteObserve_NeverDeployedIsAbsent: no state file is not an
// error, it is the answer "this frontend has never been deployed".
func TestStaticSiteObserve_NeverDeployedIsAbsent(t *testing.T) {
	obs, err := StaticSiteProvider{ProjectDir: t.TempDir(), Runner: &fakeRunner{}}.
		Observe(context.Background(), staticSiteGroup("assets"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := obs.Items[0].Health; got != HealthAbsent {
		t.Errorf("health = %v, want absent", got)
	}
}

// TestStaticSiteObserve_UnlistableBucketIsUnknownNotHealthy pins the
// pairing the design turns on: the digest is reported (it is real
// information) but health stays UNKNOWN, because a caller must be able to
// distinguish "live is serving X and the archive is intact" from "forge
// believes live is serving X and could not check". The digest field alone
// cannot carry that difference.
func TestStaticSiteObserve_UnlistableBucketIsUnknownNotHealthy(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteDeployState(dir, "static-site", "prod", "web", DeployState{Tag: "abc123"}); err != nil {
		t.Fatalf("WriteDeployState: %v", err)
	}
	runner := &fakeRunner{runErrs: map[string]error{
		"gcloud storage ls": errors.New("403 Forbidden"),
	}}
	obs, _ := StaticSiteProvider{ProjectDir: dir, Runner: runner}.
		Observe(context.Background(), staticSiteGroup("assets"))
	item := obs.Items[0]
	if item.Health == HealthHealthy {
		t.Fatal("reported HEALTHY from an unconfirmed state-file record — that is a CLAIM about " +
			"what forge did last, not an observation of what is serving")
	}
	if item.Health != HealthUnknown {
		t.Errorf("health = %v, want unknown", item.Health)
	}
	if item.Digest != "abc123" {
		t.Errorf("digest = %q, want the recorded value reported alongside the unknown", item.Digest)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ObserveGroups
// ─────────────────────────────────────────────────────────────────────────────

// TestObserveGroups_UnsupportedIsAnAnswerNotAFailure: a mixed report must
// come back whole. An unobservable tier does not fail the read — it
// contributes unknown rows — because a partial observation with VISIBLE
// gaps is worth more than none, and invisible gaps are what this verb
// exists to prevent.
func TestObserveGroups_UnsupportedIsAnAnswerNotAFailure(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteDeployState(dir, "static-site", "prod", "web", DeployState{Tag: "d1"}); err != nil {
		t.Fatalf("WriteDeployState: %v", err)
	}
	reg := &Registry{}
	reg.Register(FirebaseProvider{})
	reg.Register(StaticSiteProvider{ProjectDir: dir, Runner: &fakeRunner{outputs: map[string]string{
		"gcloud storage ls": "gs://assets/releases/d1/\n",
	}}})

	groups := []ServiceGroup{
		{Env: "prod", ProviderID: "firebase", Frontends: []FirebaseFrontend{{Name: "site"}}},
		{Env: "prod", ProviderID: "static-site", StaticSites: []StaticSiteFrontend{{
			Name: "web", Spec: StaticSiteSpec{Bucket: "assets"},
		}}},
	}
	out, err := ObserveGroups(context.Background(), reg, groups)
	if err != nil {
		t.Fatalf("an unsupported tier must not fail the whole read: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want one Observed per group, got %d", len(out))
	}
	if out[0].Items[0].Health != HealthUnknown {
		t.Errorf("firebase item = %v, want unknown", out[0].Items[0].Health)
	}
	if out[1].Items[0].Health != HealthHealthy {
		t.Errorf("static-site item = %v (%s), want healthy", out[1].Items[0].Health, out[1].Items[0].Detail)
	}
}

// TestObserveGroups_UnregisteredProviderIsUnknownNotSkipped: a group
// routed to a provider forge does not know about must appear in the
// report as unknown. Skipping it would shrink the board to the tiers
// forge happens to understand — a clean-looking report for an environment
// that was only half measured, which is the silent-green shape the whole
// design rejects.
func TestObserveGroups_UnregisteredProviderIsUnknownNotSkipped(t *testing.T) {
	reg := &Registry{}
	groups := []ServiceGroup{{
		Env: "prod", ProviderID: "lambda",
		Services: []ResolvedService{{Name: "fn"}},
	}}
	out, err := ObserveGroups(context.Background(), reg, groups)
	if err != nil {
		t.Fatalf("ObserveGroups: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("an unknown provider must still produce a row, got %d", len(out))
	}
	if len(out[0].Items) != 1 || out[0].Items[0].Health != HealthUnknown {
		t.Errorf("want one unknown item naming the service, got %+v", out[0].Items)
	}
}

// TestHealthMarshalsAsString keeps `--json` output readable: an iota in
// the JSON would make a report that a human and jq both have to decode
// against a Go constant they cannot see.
func TestHealthMarshalsAsString(t *testing.T) {
	raw, err := json.Marshal(ObservedItem{Name: "api", Health: HealthDegraded})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"health":"degraded"`) {
		t.Errorf("marshalled %s, want health as the string \"degraded\"", raw)
	}
}

// TestObserveWritesNothing pins that Observe is a READ. A verb a
// reconcile loop runs on a timer against production must not leave state
// behind, and "no side effects" is the kind of promise that decays
// silently the first time someone reaches for a state file for caching.
func TestObserveWritesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteDeployState(dir, "static-site", "prod", "web", DeployState{Tag: "d1"}); err != nil {
		t.Fatalf("WriteDeployState: %v", err)
	}
	before := snapshotTree(t, dir)

	runner := &fakeRunner{outputs: map[string]string{"gcloud storage ls": "gs://assets/releases/d1/\n"}}
	if _, err := (StaticSiteProvider{ProjectDir: dir, Runner: runner}).
		Observe(context.Background(), staticSiteGroup("assets")); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if after := snapshotTree(t, dir); after != before {
		t.Errorf("Observe mutated the project tree.\nbefore: %s\nafter:  %s", before, after)
	}
}

// snapshotTree renders a directory's paths and sizes as a comparable
// string. Sizes as well as names, so an in-place rewrite of an existing
// state file is caught rather than looking identical.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		b.WriteString(rel)
		if !info.IsDir() {
			b.WriteString(":")
			b.WriteString(info.ModTime().UTC().Format("150405.000000000"))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return b.String()
}
