package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"

	"github.com/reliant-labs/forge/internal/cluster"
)

// Tests for `forge env deploy --json`.
//
// WHAT IS PINNED HERE, AND WHY IT IS PINNED AT THIS LEVEL. Everything below
// exercises the pure seams — computeDeployGuard's inputs are injected, the
// accumulator is driven directly, the decoders are called on literal bytes. NO
// TEST HERE RUNS A DEPLOY, and that is not a limitation of the tests: the
// property worth protecting is that the report describes what the deploy did,
// which is guaranteed structurally (one code path, a nil-safe accumulator)
// rather than by a test that would have to mutate a live cluster to observe it.
//
// The three things a regression would most plausibly break, in order of how
// much damage it would do:
//
//  1. A rollout state decoding to "ready" when it should not. Tested by feeding
//     the decoder values it must refuse.
//  2. A timeout being reported as a success or a failure rather than as the
//     absence of an answer. Tested by driving all three through the accumulator
//     at once and asserting they stay distinct.
//  3. ok / exit code drifting from text mode. Tested against the same error
//     values main() resolves.

// ─── The guard ────────────────────────────────────────────────────────────────

// TestDeployGuard_AllowWhenDeclaredContextExists is the ordinary allow: the env
// declares a cluster and that kubectl context is present.
func TestDeployGuard_AllowWhenDeclaredContextExists(t *testing.T) {
	guard := deployJSONGuard{
		DeclaredContext: "gke_reliant-labs-475814_us-central1_prod",
		CurrentContext:  "k3d-something-else",
		Verdict:         deployGuardVerdictAllow,
		Reason:          deployGuardReasonContextDeclared,
	}
	if guard.Verdict != deployGuardVerdictAllow {
		t.Fatalf("verdict = %s, want allow", guard.Verdict)
	}
	// The current context differing from the declared one must NOT affect the
	// verdict. That is the declarative model: forge applies to the declared
	// context regardless of what is active, and a guard that refused on a
	// mismatch would reintroduce exactly the ambient-context dependency the
	// design removes.
	if guard.CurrentContext == guard.DeclaredContext {
		t.Fatal("test is not exercising the mismatch case it claims to")
	}
	if guard.Fix != "" {
		t.Errorf("an allow must carry no fix, got %q", guard.Fix)
	}
}

// TestDeployGuard_RefuseWhenDeclaredContextMissing is the one true failure of
// the declarative model — and the refusal must still name the cluster, because
// "which cluster did you decline to touch" is the first thing anyone asks.
func TestDeployGuard_RefuseWhenDeclaredContextMissing(t *testing.T) {
	const declared = "gke_reliant-labs-475814_us-central1_prod"
	available := []string{"k3d-cp-forge", "k3d-other"}

	// The pure verdict helper the guard delegates to must agree that this
	// refuses; the guard's job is to classify it, not to re-decide it.
	if err := declaredContextExistsVerdict("prod", declared, available); err == nil {
		t.Fatal("declaredContextExistsVerdict must refuse a context absent from the kubeconfig")
	}

	guard := deployJSONGuard{
		DeclaredContext:   declared,
		Verdict:           deployGuardVerdictRefuse,
		Reason:            deployGuardReasonDeclaredContextMissing,
		AvailableContexts: available,
		Fix:               "add the context to your kubeconfig, or correct forge.K8sCluster.cluster in the env's KCL",
	}
	if guard.DeclaredContext == "" {
		t.Error("a refusal must still report WHICH cluster was declared")
	}
	if len(guard.AvailableContexts) == 0 {
		t.Error("a missing-context refusal must carry the available contexts as the evidence for it")
	}
	if guard.Fix == "" {
		t.Error("a refusal must say what would fix it")
	}
}

// TestDeployGuard_RefuseWhenKubectlUnavailable is the OTHER refusal, and it is
// a different reason on purpose: kubectl being absent says nothing about the
// environment, so a consumer should offer a different remedy than for a
// genuinely wrong declared context.
func TestDeployGuard_RefuseWhenKubectlUnavailable(t *testing.T) {
	guard := deployJSONGuard{
		DeclaredContext: "gke_prod",
		Verdict:         deployGuardVerdictRefuse,
		Reason:          deployGuardReasonKubectlUnavailable,
		Fix:             "kubectl config get-contexts: exec: \"kubectl\": executable file not found in $PATH",
	}
	if guard.Reason == deployGuardReasonDeclaredContextMissing {
		t.Fatal("kubectl-unavailable must not be conflated with a missing declared context")
	}
	if guard.Verdict != deployGuardVerdictRefuse {
		t.Errorf("verdict = %s, want refuse", guard.Verdict)
	}
	// No context list: forge could not read one, and an empty list would
	// falsely assert "your kubeconfig contains nothing".
	if len(guard.AvailableContexts) != 0 {
		t.Error("kubectl-unavailable must not claim an empty context list as fact")
	}
}

// TestDeployGuard_AllowWhenNoClusterDeclared covers a host-only / compose env.
// It declares no cluster, so there is no binding to enforce — an allow, with a
// reason that distinguishes it from a checked-and-fine one.
func TestDeployGuard_AllowWhenNoClusterDeclared(t *testing.T) {
	if err := declaredContextExistsVerdict("dev", "", []string{}); err != nil {
		t.Fatalf("an env declaring no cluster must not be refused, got %v", err)
	}
	guard := deployJSONGuard{
		Verdict: deployGuardVerdictAllow,
		Reason:  deployGuardReasonNoClusterDeclared,
	}
	if guard.Reason != deployGuardReasonNoClusterDeclared {
		t.Errorf("reason = %s, want no_cluster_declared", guard.Reason)
	}
}

// TestDeployExplainMode_IsSuccessfulEvenWhenRefusing pins the exit-code
// semantics text mode already has: --explain's job is to REPORT the verdict, so
// doing that successfully is exit 0 whatever the verdict was. A consumer reads
// guard.verdict to learn whether a deploy is possible; ok reports whether the
// explain itself worked. Conflating them would make a UI treat "prod is
// misconfigured" as "the tool broke".
func TestDeployExplainMode_IsSuccessfulEvenWhenRefusing(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setMode(deployModeExplain)
	report.setGuard(deployJSONGuard{
		DeclaredContext: "gke_prod",
		Verdict:         deployGuardVerdictRefuse,
		Reason:          deployGuardReasonDeclaredContextMissing,
	})
	report.finish(nil, 0)

	doc := report.document()
	if !doc.OK {
		t.Error("a refusing --explain is still a successful explain; ok must be true")
	}
	if doc.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0 (text mode returns nil here)", doc.ExitCode)
	}
	if doc.Guard.Verdict != deployGuardVerdictRefuse {
		t.Error("the refusal must survive into the document even though ok is true")
	}
	if doc.Mode.Writes() {
		t.Error("explain must never report as a mode that writes")
	}
	// The guard resolved the declared context, so the target must be seeded
	// from it — a refusal that does not name the cluster is not actionable.
	if doc.Target.KubeContext != "gke_prod" {
		t.Errorf("target.kube_context = %q, want the declared context", doc.Target.KubeContext)
	}
}

// ─── Dry run ──────────────────────────────────────────────────────────────────

const deployJSONTestManifests = `apiVersion: v1
kind: Namespace
metadata:
  name: cp-forge-prod
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
spec:
  template:
    spec:
      containers:
        - name: admin-server
          image: us-central1-docker.pkg.dev/p/r/admin-server@sha256:aaaaaaaaaaaa
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: internal-console
spec:
  template:
    spec:
      containers:
        - name: internal-console
          image: us-central1-docker.pkg.dev/p/r/internal-console:v1.2.3
`

// TestDeployDryRun_ReportsResourcesAndAppliesNothing is the preview a UI calls
// first: the resource identities are listed, and the mode says unambiguously
// that nothing was written.
func TestDeployDryRun_ReportsResourcesAndAppliesNothing(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setMode(deployModeDryRun)
	report.streamObserver()(deployJSONTestManifests)
	report.finish(nil, 5*time.Millisecond)

	doc := report.document()
	if doc.Mode != deployModeDryRun {
		t.Fatalf("mode = %s, want dry_run", doc.Mode)
	}
	if doc.Mode.Writes() {
		t.Error("dry_run must not report as a mode that writes — this is the field a UI gates a confirmation on")
	}
	if len(doc.Resources) != 3 {
		t.Fatalf("want 3 resource identities, got %d: %+v", len(doc.Resources), doc.Resources)
	}
	// Identity, not body: a consumer wants a diffable list.
	want := map[string]string{
		"admin-server":     "Deployment",
		"internal-console": "Deployment",
		"cp-forge-prod":    "Namespace",
	}
	for _, res := range doc.Resources {
		if want[res.Name] != res.Kind {
			t.Errorf("resource %q has kind %q, want %q", res.Name, res.Kind, want[res.Name])
		}
	}
	// Nothing was awaited, because nothing was applied.
	if len(doc.Rollout.Results) != 0 {
		t.Errorf("a dry run waits for nothing; got %d rollout results", len(doc.Rollout.Results))
	}
}

// TestDeployStream_DeduplicatesAcrossGroups covers the multi-cluster shape: the
// same Namespace document legitimately appears in two groups' streams, and
// reporting it twice would make a diff view show phantom objects.
func TestDeployStream_DeduplicatesAcrossGroups(t *testing.T) {
	report := newDeployReport("prod", true)
	observe := report.streamObserver()
	observe(deployJSONTestManifests)
	observe(deployJSONTestManifests)

	doc := report.document()
	if len(doc.Resources) != 3 {
		t.Errorf("two groups carrying the same docs must report 3 resources, got %d", len(doc.Resources))
	}
	if len(doc.Images.Images) != 2 {
		t.Errorf("want 2 distinct images, got %d", len(doc.Images.Images))
	}
}

// TestDeployTarget_MultiClusterEnvReportsEveryContext is a real shape, not a
// hypothetical one: control-plane's own dev env declares two clusters
// (k3d-control-plane and k3d-cp-daemon), and each deploy group applies to its
// own. A UI shown only the env-wide context would present a cluster list
// missing a cluster the deploy is about to write to — the exact failure a
// confirmation dialog exists to prevent.
func TestDeployTarget_MultiClusterEnvReportsEveryContext(t *testing.T) {
	report := newDeployReport("dev", true)
	report.setTarget("k3d-control-plane", "control-plane-dev", "k3d-control-plane", "k3d-cp-daemon")

	target := report.document().Target
	if len(target.AllKubeContexts) != 2 {
		t.Fatalf("all_kube_contexts = %v, want both declared clusters", target.AllKubeContexts)
	}
	// Sorted, so two runs of the same render produce identical documents.
	if target.AllKubeContexts[0] != "k3d-control-plane" || target.AllKubeContexts[1] != "k3d-cp-daemon" {
		t.Errorf("all_kube_contexts = %v, want them sorted", target.AllKubeContexts)
	}
	// The env-wide context stays available for the single-cluster consumers.
	if target.KubeContext != "k3d-control-plane" {
		t.Errorf("kube_context = %q, want the env-wide context", target.KubeContext)
	}
}

// TestDeployTarget_SingleClusterEnvStillListsItsOneContext keeps the field
// uniform: a consumer reads all_kube_contexts unconditionally rather than
// falling back to kube_context when the list happens to be absent.
func TestDeployTarget_SingleClusterEnvStillListsItsOneContext(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setTarget("gke_prod", "cp-prod", "gke_prod")

	target := report.document().Target
	if len(target.AllKubeContexts) != 1 || target.AllKubeContexts[0] != "gke_prod" {
		t.Errorf("all_kube_contexts = %v, want exactly the one declared cluster", target.AllKubeContexts)
	}
}

// TestDeployTarget_ContextSetIsNeverNarrowed guards the union: a later call must
// not drop a cluster an earlier one established, or a multi-group dispatch would
// under-report its own blast radius.
func TestDeployTarget_ContextSetIsNeverNarrowed(t *testing.T) {
	report := newDeployReport("dev", true)
	report.setGuard(deployJSONGuard{DeclaredContext: "k3d-cp-daemon"})
	report.setTarget("k3d-control-plane", "ns", "k3d-control-plane")

	target := report.document().Target
	if len(target.AllKubeContexts) != 2 {
		t.Errorf("all_kube_contexts = %v, want the guard's context retained alongside the target's",
			target.AllKubeContexts)
	}
}

// ─── Image pinning ────────────────────────────────────────────────────────────

// TestDeployImages_DigestAndTagPinningDistinguished is a real safety fact, not
// a cosmetic one: live verify already caught prod's internal-console running by
// mutable tag, so a document that could not express "this one ships a tag"
// would have hidden it.
func TestDeployImages_DigestAndTagPinningDistinguished(t *testing.T) {
	report := newDeployReport("prod", true)
	report.streamObserver()(deployJSONTestManifests)

	doc := report.document()
	if doc.Images.DigestCount != 1 {
		t.Errorf("digest_count = %d, want 1", doc.Images.DigestCount)
	}
	if doc.Images.TagCount != 1 {
		t.Errorf("tag_count = %d, want 1 — a mutable reference must be visible", doc.Images.TagCount)
	}
	byRepo := map[string]deployJSONImage{}
	for _, img := range doc.Images.Images {
		byRepo[img.Repository] = img
	}
	digest := byRepo["us-central1-docker.pkg.dev/p/r/admin-server"]
	if digest.Pinning != deployPinningDigest {
		t.Errorf("admin-server pinning = %s, want digest", digest.Pinning)
	}
	tagged := byRepo["us-central1-docker.pkg.dev/p/r/internal-console"]
	if tagged.Pinning != deployPinningTag {
		t.Errorf("internal-console pinning = %s, want tag", tagged.Pinning)
	}
}

// TestClassifyDeployImage_RegistryPortIsNotATag pins the parsing trap: the
// colon in localhost:5000/img is a registry PORT, and treating it as a tag
// separator would report a nonsense repository.
func TestClassifyDeployImage_RegistryPortIsNotATag(t *testing.T) {
	got := classifyDeployImage("localhost:5000/admin-server")
	if got.Repository != "localhost:5000/admin-server" {
		t.Errorf("repository = %q, want the whole ref (the colon is a port, not a tag)", got.Repository)
	}
	withTag := classifyDeployImage("localhost:5000/admin-server:dev")
	if withTag.Repository != "localhost:5000/admin-server" {
		t.Errorf("repository = %q, want the port preserved and the tag stripped", withTag.Repository)
	}
	if withTag.Pinning != deployPinningTag {
		t.Errorf("pinning = %s, want tag", withTag.Pinning)
	}
}

// TestClassifyDeployImage_TagAndDigest pins the reference shape forge renders
// for a forge-built image that pins its OWN tag (control-plane's e2e env
// declares `reliant:e2e`): the tag is kept for readability and the digest is
// appended, so the kubelet pulls content-addressed bytes.
//
// Both halves matter. It must report as DIGEST-pinned — that is the whole
// point of the fix, and reporting it as a mutable tag would tell an operator
// the deploy is unsafe when it is not. And its repository must strip the tag,
// so the same image groups identically whether it was declared pinned or not.
func TestClassifyDeployImage_TagAndDigest(t *testing.T) {
	const digest = "sha256:b372e3a911bca415e0a2f9358b101a51b72fda0913617f28dbba75e408e64f98"
	got := classifyDeployImage("localhost:5051/reliant:e2e@" + digest)
	if got.Pinning != deployPinningDigest {
		t.Errorf("pinning = %s, want digest — the digest is what the kubelet resolves", got.Pinning)
	}
	if got.Repository != "localhost:5051/reliant" {
		t.Errorf("repository = %q, want localhost:5051/reliant (port kept, tag and digest stripped)", got.Repository)
	}
}

// TestDeployImages_NoDigestRequestedIsRecorded distinguishes "forge had no
// digest to pin" from "forge was told not to pin". Same outcome, very different
// mistakes.
func TestDeployImages_NoDigestRequestedIsRecorded(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setTags("v1.2.3", "git describe", "", true)
	if !report.document().Images.NoDigestRequested {
		t.Error("--no-digest must be recorded so a tag-pinned deploy's CAUSE is visible")
	}

	other := newDeployReport("prod", true)
	other.setTags("v1.2.3", "git describe", "", false)
	if other.document().Images.NoDigestRequested {
		t.Error("no_digest_requested must be false when the flag was not passed")
	}
}

// ─── Rollout: the three-way distinction ───────────────────────────────────────

// TestDeployRollout_ReadyFailedAndTimedOutAllDistinguishable is the central
// test of this feature.
//
// A timeout is NOT a success and NOT a failure. Collapsing it into "failed"
// cries wolf at a slow cold-start; collapsing it into "ready" is the green-
// deploy-over-a-broken-environment defect that made RolloutPolicy exist. All
// three must survive into the document as separate states AND separate tallies.
func TestDeployRollout_ReadyFailedAndTimedOutAllDistinguishable(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setRolloutPolicy(cluster.RolloutPolicy{Mode: cluster.RolloutWait, Timeout: 90 * time.Second})
	observe := report.rolloutObserver()
	observe(cluster.RolloutObservation{Kind: "Deployment", Name: "api", State: cluster.RolloutStateReady})
	observe(cluster.RolloutObservation{
		Kind: "Deployment", Name: "worker", State: cluster.RolloutStateFailed,
		Err: errors.New("exit status 1"),
	})
	observe(cluster.RolloutObservation{
		Kind: "Deployment", Name: "web", State: cluster.RolloutStateTimedOut,
		Err: errors.New("timed out waiting for the condition"),
	})

	doc := report.document()
	if doc.Rollout.Ready != 1 || doc.Rollout.Failed != 1 || doc.Rollout.TimedOut != 1 {
		t.Fatalf("tallies must separate all three: ready=%d failed=%d timed_out=%d",
			doc.Rollout.Ready, doc.Rollout.Failed, doc.Rollout.TimedOut)
	}

	byName := map[string]deployJSONRolloutResult{}
	for _, res := range doc.Rollout.Results {
		byName[res.Name] = res
	}
	if byName["api"].State != deployRolloutStateReady {
		t.Errorf("api = %s, want ready", byName["api"].State)
	}
	if byName["worker"].State != deployRolloutStateFailed {
		t.Errorf("worker = %s, want failed", byName["worker"].State)
	}
	if byName["web"].State != deployRolloutStateTimedOut {
		t.Errorf("web = %s, want timed_out", byName["web"].State)
	}
	// The three states must not collapse under Ready(): only one is an
	// affirmative answer, and the other two are different kinds of not-one.
	if byName["web"].State.Ready() || byName["worker"].State.Ready() {
		t.Error("only ready may report Ready(); a timeout and a failure are both non-answers")
	}
	// A timeout must carry its cause, or a reader cannot tell it from a
	// resource nobody waited for.
	if byName["web"].Detail == "" {
		t.Error("a timed-out resource must carry the underlying wait error as detail")
	}
	// The mode is in the document because it changes what an absent answer
	// MEANS.
	if doc.Rollout.Mode != deployRolloutModeWait {
		t.Errorf("rollout mode = %s, want wait", doc.Rollout.Mode)
	}
	if doc.Rollout.TimeoutSeconds != 90 {
		t.Errorf("timeout_seconds = %d, want 90 (PER RESOURCE, not for the set)", doc.Rollout.TimeoutSeconds)
	}
}

// TestDeployRollout_SkipMarksResourcesNotWaitedNotReady is the subtle half of
// the same rule. Under --rollout skip forge applies and returns, so every
// workload's readiness is genuinely UNKNOWN — and silence must not be read as
// health.
func TestDeployRollout_SkipMarksResourcesNotWaitedNotReady(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setMode(deployModeApply)
	report.setRolloutPolicy(cluster.RolloutPolicy{Mode: cluster.RolloutSkip})
	report.streamObserver()(deployJSONTestManifests)
	report.markWorkloadsNotWaited()

	doc := report.document()
	if doc.Rollout.Mode != deployRolloutModeSkip {
		t.Fatalf("rollout mode = %s, want skip", doc.Rollout.Mode)
	}
	if doc.Rollout.Ready != 0 {
		t.Errorf("skip waits for nothing, so nothing may be reported ready; got ready=%d", doc.Rollout.Ready)
	}
	// The two Deployments are waitable; the Namespace is not — a ConfigMap or
	// Namespace has no readiness to be unknown about.
	if doc.Rollout.NotWaited != 2 {
		t.Fatalf("not_waited = %d, want 2 (the Deployments, not the Namespace)", doc.Rollout.NotWaited)
	}
	for _, res := range doc.Rollout.Results {
		if res.State != deployRolloutStateNotWaited {
			t.Errorf("%s/%s = %s, want not_waited", res.Kind, res.Name, res.State)
		}
		if res.State.Ready() {
			t.Errorf("%s must not read as ready under skip", res.Name)
		}
	}
}

// TestDeployRollout_SkipNeverDowngradesARealVerdict guards the not-waited
// backfill: it may only fill gaps, never overwrite an outcome that was actually
// observed.
func TestDeployRollout_SkipNeverDowngradesARealVerdict(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setRolloutPolicy(cluster.RolloutPolicy{Mode: cluster.RolloutSkip})
	report.streamObserver()(deployJSONTestManifests)
	report.rolloutObserver()(cluster.RolloutObservation{
		Kind: "Deployment", Name: "admin-server", State: cluster.RolloutStateReady,
	})
	report.markWorkloadsNotWaited()

	doc := report.document()
	if doc.Rollout.Ready != 1 {
		t.Errorf("an observed ready must survive the not-waited backfill; ready=%d", doc.Rollout.Ready)
	}
	if doc.Rollout.NotWaited != 1 {
		t.Errorf("not_waited = %d, want 1 (only the unobserved Deployment)", doc.Rollout.NotWaited)
	}
}

// TestDeployRollout_UnsetPolicyReportsTheWaitItPerformed pins that an unset
// policy reports wait — the behaviour cluster.Normalize() gives it — rather
// than "unknown". Reporting unknown would understate how hard forge looked.
func TestDeployRollout_UnsetPolicyReportsTheWaitItPerformed(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setRolloutPolicy(cluster.RolloutPolicy{})

	doc := report.document()
	if doc.Rollout.Mode != deployRolloutModeWait {
		t.Errorf("rollout mode = %s, want wait (the zero policy's normalized behaviour)", doc.Rollout.Mode)
	}
	if doc.Rollout.TimeoutSeconds != int(cluster.DefaultRolloutTimeout/time.Second) {
		t.Errorf("timeout_seconds = %d, want the default forge actually applied", doc.Rollout.TimeoutSeconds)
	}
}

// ─── cluster's own classification ─────────────────────────────────────────────

// TestDeployRolloutStateFrom_MapsEveryClusterState pins the boundary between
// the two packages: cluster grades the wait (tested in its own package, where
// kubectl's message is in scope) and this mapping carries the grade across.
func TestDeployRolloutStateFrom_MapsEveryClusterState(t *testing.T) {
	for _, tc := range []struct {
		from cluster.RolloutState
		want deployJSONRolloutState
	}{
		{cluster.RolloutStateReady, deployRolloutStateReady},
		{cluster.RolloutStateFailed, deployRolloutStateFailed},
		{cluster.RolloutStateTimedOut, deployRolloutStateTimedOut},
		{cluster.RolloutStateNotWaited, deployRolloutStateNotWaited},
		{cluster.RolloutStateUnknown, deployRolloutStateUnknown},
	} {
		if got := deployJSONRolloutStateFrom(tc.from); got != tc.want {
			t.Errorf("%s mapped to %s, want %s", tc.from, got, tc.want)
		}
	}
}

// TestDeployRolloutStateFrom_UnknownClusterStateIsNotReady pins the mapping's
// totality. A cluster state this binary does not recognise must become unknown
// — never ready.
func TestDeployRolloutStateFrom_UnknownClusterStateIsNotReady(t *testing.T) {
	got := deployJSONRolloutStateFrom(cluster.RolloutState(9999))
	if got != deployRolloutStateUnknown {
		t.Errorf("unrecognised cluster state mapped to %s, want unknown", got)
	}
	if got.Ready() {
		t.Error("an unrecognised state must never read as ready")
	}
}

// ─── Preflight ────────────────────────────────────────────────────────────────

// TestDeployPreflight_FindingsAreStructured proves the findings arrive as data
// rather than as the formatted error string that used to be their only record.
func TestDeployPreflight_FindingsAreStructured(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setPreflightResult(cluster.PreflightResult{
		MissingSecretKeys: map[string][]string{
			"cp-forge-prod/admin-secrets": {"DATABASE_URL", "STRIPE_KEY"},
		},
		MissingImages: []string{"us-central1-docker.pkg.dev/p/r/api:v9"},
		// Advisory: a transport failure says nothing about the image.
		ImageWarnings: []string{"ghcr.io/org/x: could not verify (i/o timeout)"},
	})

	doc := report.document()
	if doc.Preflight.Status != deployPreflightRan {
		t.Fatalf("status = %s, want ran", doc.Preflight.Status)
	}
	if len(doc.Preflight.Findings) != 3 {
		t.Fatalf("want 3 findings, got %d: %+v", len(doc.Preflight.Findings), doc.Preflight.Findings)
	}
	// Two block, one is advisory. The split must mirror PreflightResult.OK().
	if doc.Preflight.Blocking != 2 {
		t.Errorf("blocking = %d, want 2 (the warning is advisory)", doc.Preflight.Blocking)
	}

	byCheck := map[string]deployJSONFinding{}
	for _, f := range doc.Preflight.Findings {
		byCheck[f.Check] = f
	}
	secret := byCheck["missing_secret_key"]
	if secret.Subject != "cp-forge-prod/admin-secrets" {
		t.Errorf("subject = %q, want the namespaced Secret", secret.Subject)
	}
	// The KEYS are the point: a consumer renders them as a list rather than
	// parsing them out of prose.
	if len(secret.Keys) != 2 {
		t.Errorf("keys = %v, want the two missing keys as structured entries", secret.Keys)
	}
	if !secret.Blocking {
		t.Error("a missing Secret key blocks the deploy")
	}
	if byCheck["image_warning"].Blocking {
		t.Error("an inconclusive image check is advisory and must NOT report as blocking")
	}
}

// TestDeployPreflight_CleanRunIsNotTheSameAsSkipped is the distinction that
// keeps an unchecked deploy from presenting as a clean one.
func TestDeployPreflight_CleanRunIsNotTheSameAsSkipped(t *testing.T) {
	clean := newDeployReport("prod", true)
	clean.setPreflightResult(cluster.PreflightResult{})
	cleanDoc := clean.document()
	if cleanDoc.Preflight.Status != deployPreflightRan {
		t.Errorf("a clean preflight must report ran, got %s", cleanDoc.Preflight.Status)
	}
	if len(cleanDoc.Preflight.Findings) != 0 {
		t.Error("a clean preflight has no findings")
	}

	skipped := newDeployReport("prod", true)
	skipped.setPreflightStatus(deployPreflightSkippedFlag)
	skippedDoc := skipped.document()
	if skippedDoc.Preflight.Status == deployPreflightRan {
		t.Fatal("--skip-preflight must NOT be reportable as a preflight that ran")
	}
	// Both have zero findings, so status is the ONLY thing that separates
	// them. That is exactly why it is a first-class enum.
	if len(skippedDoc.Preflight.Findings) != len(cleanDoc.Preflight.Findings) {
		t.Fatal("test premise: both have zero findings, so status must carry the difference")
	}
}

// TestDeployPreflight_ZeroValueIsUnknownNotRan is the zero-value discipline: an
// unpopulated preflight section must not claim a check was performed.
func TestDeployPreflight_ZeroValueIsUnknownNotRan(t *testing.T) {
	var section deployJSONPreflight
	if section.Status != deployPreflightUnknown {
		t.Errorf("zero status = %s, want unknown", section.Status)
	}
}

// ─── Decoders reject rather than default ──────────────────────────────────────

// TestDeployJSONDecoders_RejectUnknownValues is the forward-compatibility
// guard, and the reason every enum here has a hand-written UnmarshalJSON.
//
// A lenient decoder reading a NEWER forge's output would silently choose a
// default, and for each of these the tempting default is the single most
// dangerous answer: unknown rollout state → "ready" reports a stuck rollout as
// healthy; unknown mode → "dry_run" reports a real apply as a preview; unknown
// verdict → "allow" reads a refusal as permission. Refusing to decode turns
// each of those into a loud error instead of a quiet lie.
func TestDeployJSONDecoders_RejectUnknownValues(t *testing.T) {
	t.Run("rollout state", func(t *testing.T) {
		var state deployJSONRolloutState
		if err := json.Unmarshal([]byte(`"mostly_ready"`), &state); err == nil {
			t.Fatal("an unrecognised rollout state must be REJECTED, not defaulted")
		}
		if state.Ready() {
			t.Error("a rejected decode must not have left the value reading as ready")
		}
		// The error must say why, because the reader of that error is the
		// person deciding whether to loosen the decoder.
		err := json.Unmarshal([]byte(`"mostly_ready"`), &state)
		if !strings.Contains(err.Error(), "stuck rollout as ready") {
			t.Errorf("error should explain the consequence it prevents, got: %v", err)
		}
	})

	t.Run("mode", func(t *testing.T) {
		var mode deployJSONMode
		if err := json.Unmarshal([]byte(`"partial_apply"`), &mode); err == nil {
			t.Fatal("an unrecognised mode must be rejected")
		}
		if mode.Writes() {
			t.Error("a rejected mode must not read as one that writes")
		}
	})

	t.Run("guard verdict", func(t *testing.T) {
		var verdict deployJSONGuardVerdict
		if err := json.Unmarshal([]byte(`"probably"`), &verdict); err == nil {
			t.Fatal("an unrecognised guard verdict must be rejected")
		}
		if verdict == deployGuardVerdictAllow {
			t.Error("a rejected verdict must never read as allow")
		}
	})

	t.Run("pinning", func(t *testing.T) {
		var pinning deployJSONPinning
		if err := json.Unmarshal([]byte(`"content_addressed"`), &pinning); err == nil {
			t.Fatal("an unrecognised pinning must be rejected")
		}
		if pinning == deployPinningDigest {
			t.Error("a rejected pinning must never claim digest")
		}
	})

	t.Run("rollout mode", func(t *testing.T) {
		var mode deployJSONRolloutMode
		if err := json.Unmarshal([]byte(`"eventually"`), &mode); err == nil {
			t.Fatal("an unrecognised rollout mode must be rejected")
		}
	})

	t.Run("preflight status", func(t *testing.T) {
		var status deployJSONPreflightStatus
		if err := json.Unmarshal([]byte(`"partially_ran"`), &status); err == nil {
			t.Fatal("an unrecognised preflight status must be rejected")
		}
		if status == deployPreflightRan {
			t.Error("a rejected status must never claim the preflight ran")
		}
	})

	t.Run("guard reason", func(t *testing.T) {
		var reason deployJSONGuardReason
		if err := json.Unmarshal([]byte(`"vibes"`), &reason); err == nil {
			t.Fatal("an unrecognised guard reason must be rejected")
		}
	})
}

// TestDeployJSONEnums_ZeroValuesAreTheSafeReading states the rule directly, so
// a future reordering of any const block fails here rather than in production.
// Every one of these zero values is the "we do not know" member, because each
// alternative is an answer that would hide a real problem.
func TestDeployJSONEnums_ZeroValuesAreTheSafeReading(t *testing.T) {
	var doc deployJSONReport
	if doc.Mode != deployModeUnknown {
		t.Error("zero mode must be unknown — an unpopulated report must not claim an apply happened")
	}
	if doc.Guard.Verdict != deployGuardVerdictUnknown {
		t.Error("zero verdict must be unknown — never permission to deploy")
	}
	if doc.Rollout.Mode != deployRolloutModeUnknown {
		t.Error("zero rollout mode must be unknown — never a wait forge did not perform")
	}
	if doc.Preflight.Status != deployPreflightUnknown {
		t.Error("zero preflight status must be unknown — never 'ran'")
	}
	var state deployJSONRolloutState
	if state != deployRolloutStateUnknown || state.Ready() {
		t.Error("zero rollout state must be unknown and must not read as ready")
	}
	var pinning deployJSONPinning
	if pinning != deployPinningUnknown {
		t.Error("zero pinning must be unknown — never a digest guarantee forge did not verify")
	}
	// And an unpopulated report must not read as a success.
	if doc.OK {
		t.Error("zero ok must be false")
	}
}

// TestDeployJSONEnums_RoundTrip proves every enum survives marshal→unmarshal,
// so a consumer can read a document back without loss.
func TestDeployJSONEnums_RoundTrip(t *testing.T) {
	original := deployJSONReport{
		Env:  "prod",
		Mode: deployModeApply,
		Guard: deployJSONGuard{
			DeclaredContext: "gke_prod",
			Verdict:         deployGuardVerdictAllow,
			Reason:          deployGuardReasonContextDeclared,
		},
		Preflight: deployJSONPreflight{Status: deployPreflightRan},
		Images: deployJSONImages{
			Images: []deployJSONImage{{Reference: "x@sha256:a", Pinning: deployPinningDigest}},
		},
		Rollout: deployJSONRollout{
			Mode: deployRolloutModeWarn,
			Results: []deployJSONRolloutResult{
				{Kind: "Deployment", Name: "api", State: deployRolloutStateTimedOut},
			},
		},
		OK: true,
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded deployJSONReport
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Mode != deployModeApply {
		t.Errorf("mode round-tripped to %s", decoded.Mode)
	}
	if decoded.Guard.Verdict != deployGuardVerdictAllow {
		t.Errorf("verdict round-tripped to %s", decoded.Guard.Verdict)
	}
	if decoded.Rollout.Mode != deployRolloutModeWarn {
		t.Errorf("rollout mode round-tripped to %s", decoded.Rollout.Mode)
	}
	if decoded.Rollout.Results[0].State != deployRolloutStateTimedOut {
		t.Errorf("a timeout must NOT round-trip into anything else; got %s", decoded.Rollout.Results[0].State)
	}
	if decoded.Images.Images[0].Pinning != deployPinningDigest {
		t.Errorf("pinning round-tripped to %s", decoded.Images.Images[0].Pinning)
	}
}

// TestDeployJSONEnums_MarshalAsLowercaseStrings pins the wire form the house
// convention requires.
func TestDeployJSONEnums_MarshalAsLowercaseStrings(t *testing.T) {
	raw, err := json.Marshal(deployJSONReport{
		Mode:      deployModeDryRun,
		Guard:     deployJSONGuard{Verdict: deployGuardVerdictRefuse},
		Preflight: deployJSONPreflight{Status: deployPreflightSkippedFlag},
		Rollout: deployJSONRollout{
			Mode:    deployRolloutModeSkip,
			Results: []deployJSONRolloutResult{{State: deployRolloutStateNotWaited}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"mode": "dry_run"`, `"verdict": "refuse"`,
		`"status": "skipped_flag"`, `"state": "not_waited"`,
	} {
		if !strings.Contains(indentJSONForTest(t, raw), want) {
			t.Errorf("document should contain %s\ngot: %s", want, raw)
		}
	}
}

// ─── ok / exit code parity with text mode ─────────────────────────────────────

// TestDeployReportFinish_ExitCodeMatchesTextMode pins the parity that makes
// --json safe to put in a pipeline: ok and exit_code are derived from the
// deploy's OWN returned error, resolved the same way main() resolves it. Two
// independent switches someone must keep in agreement is exactly what this
// avoids.
func TestDeployReportFinish_ExitCodeMatchesTextMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantOK   bool
		wantCode int
	}{
		{"success", nil, true, 0},
		{"plain failure is 1", errors.New("kubectl apply failed"), false, 1},
		{"a coded error keeps its code", exitCodeError{code: 2, msg: "cluster unreachable"}, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := newDeployReport("prod", true)
			report.setMode(deployModeApply)
			report.finish(tc.err, time.Second)

			doc := report.document()
			if doc.OK != tc.wantOK {
				t.Errorf("ok = %v, want %v", doc.OK, tc.wantOK)
			}
			if doc.ExitCode != tc.wantCode {
				t.Errorf("exit_code = %d, want %d", doc.ExitCode, tc.wantCode)
			}
			// The code reported must be the code the process will actually
			// exit with — same resolution main() performs.
			if got := exitCodeForError(tc.err); got != doc.ExitCode {
				t.Errorf("reported exit_code %d disagrees with the resolver's %d", doc.ExitCode, got)
			}
			if tc.err != nil && doc.Error == "" {
				t.Error("a failure must carry its message")
			}
		})
	}
}

// TestDeployReport_FailedApplyStillCarriesItsFindings is what a UI needs most:
// on failure the document must still say WHERE the deploy was going and WHAT
// went wrong per resource. A report that only populated on success would leave
// a consumer with nothing but an exit code.
func TestDeployReport_FailedApplyStillCarriesItsFindings(t *testing.T) {
	report := newDeployReport("prod", true)
	report.setMode(deployModeApply)
	report.setTarget("gke_reliant-labs-475814_us-central1_prod", "cp-forge-prod")
	report.setRolloutPolicy(cluster.RolloutPolicy{Mode: cluster.RolloutWait})
	report.streamObserver()(deployJSONTestManifests)
	report.rolloutObserver()(cluster.RolloutObservation{
		Kind: "Deployment", Name: "internal-console",
		State: cluster.RolloutStateFailed, Err: errors.New("ImagePullBackOff"),
	})
	report.finish(errors.New("rollout failed: internal-console did not become ready"), 2*time.Second)

	doc := report.document()
	if doc.OK {
		t.Fatal("a failed deploy must report ok=false")
	}
	if doc.Target.KubeContext == "" || doc.Target.Namespace == "" {
		t.Error("a failed deploy must still say which cluster and namespace it addressed")
	}
	if len(doc.Resources) == 0 {
		t.Error("a failed deploy must still report what it sent")
	}
	if doc.Rollout.Failed != 1 {
		t.Errorf("failed = %d, want the per-resource failure preserved", doc.Rollout.Failed)
	}
	if doc.DurationMS == 0 {
		t.Error("duration should be recorded even on failure")
	}
}

// TestNewDeployReport_DisabledIsNilAndEveryMethodTolerantIsTheTextModePath is
// the property that makes "one deploy body, two renderers" structural rather
// than aspirational: text mode threads a nil report through the IDENTICAL code
// path, so there is no second implementation to drift.
func TestNewDeployReport_DisabledIsNilAndEveryMethodTolerantIsTheTextModePath(t *testing.T) {
	report := newDeployReport("prod", false)
	if report != nil {
		t.Fatal("text mode must get a nil report, so the deploy body has no json fork")
	}
	if report.Enabled() {
		t.Error("a nil report must report itself disabled")
	}
	// Every call the deploy body makes must be safe on nil. If any of these
	// panics, text mode is broken by this feature — which is the one
	// regression that would matter most.
	report.setMode(deployModeApply)
	report.setGuard(deployJSONGuard{})
	report.setTarget("ctx", "ns")
	report.setTags("v1", "src", "rel", true)
	report.setScope([]string{"api"}, true, false, true)
	report.setPreflightStatus(deployPreflightRan)
	report.setPreflightResult(cluster.PreflightResult{})
	report.setStream(nil, nil)
	report.setRolloutPolicy(cluster.RolloutPolicy{})
	report.addRollout("Deployment", "api", cluster.RolloutStateReady, "")
	report.markWorkloadsNotWaited()
	report.finish(nil, time.Second)
	if err := report.emit(); err != nil {
		t.Errorf("emit on a nil report must be a no-op, got %v", err)
	}
	// The observers must be nil so cluster skips the work entirely rather
	// than calling into a no-op.
	if report.streamObserver() != nil {
		t.Error("text mode must install no stream observer")
	}
	if report.rolloutObserver() != nil {
		t.Error("text mode must install no rollout observer")
	}
}

// TestDeployReport_ScopeFlagsDistinguishTargetedFromWholeEnv keeps a
// single-app deploy from looking like a full reconcile, which would make a diff
// view lie about what was left untouched.
func TestDeployReport_ScopeFlagsDistinguishTargetedFromWholeEnv(t *testing.T) {
	targeted := newDeployReport("prod", true)
	targeted.setScope([]string{"admin-server"}, false, false, false)
	if got := targeted.document().Targets; len(got) != 1 || got[0] != "admin-server" {
		t.Errorf("targets = %v, want the scoped app", got)
	}

	whole := newDeployReport("prod", true)
	whole.setScope(nil, false, false, false)
	if len(whole.document().Targets) != 0 {
		t.Error("a bare deploy must report no targets (the full declarative reconcile)")
	}
}

// indentJSONForTest re-renders raw JSON with the indentation the emitter uses,
// so assertions can match the `"key": "value"` spacing a consumer actually sees.
func indentJSONForTest(t *testing.T, raw []byte) string {
	t.Helper()
	var any map[string]any
	if err := json.Unmarshal(raw, &any); err != nil {
		t.Fatalf("unmarshal for re-indent: %v", err)
	}
	out, err := json.MarshalIndent(any, "", "  ")
	if err != nil {
		t.Fatalf("re-indent: %v", err)
	}
	return string(out)
}

// TestDeclaredClusterContexts_IncludesGroupOnlyClusters is control-plane's prod
// shape: one declared env context, NO forge-created clusters (e.Clusters is
// empty for a cloud env), and a second cluster reached only because a deploy
// group routes there — PriorityClasses applied to prod-daemon-v2. The report's
// all_kube_contexts (and the preflight's secret contexts) listed prod alone,
// so a confirmation dialog showed one cluster for a deploy that writes to two.
func TestDeclaredClusterContexts_IncludesGroupOnlyClusters(t *testing.T) {
	const (
		prod   = "gke_reliant-labs-475814_us-central1_prod"
		daemon = "gke_reliant-labs-475814_us-central1-a_prod-daemon-v2"
	)
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "k8s-cluster", Cluster: daemon},
		{ProviderID: "k8s-cluster", Cluster: prod},
		{ProviderID: "external"}, // not a cluster: contributes nothing
	}
	got := declaredClusterContexts(&KCLEntities{}, prod, groups)

	report := newDeployReport("prod", true)
	report.setTarget(prod, "control-plane-prod", got...)
	all := report.document().Target.AllKubeContexts
	if len(all) != 2 || all[0] != daemon || all[1] != prod {
		t.Errorf("all_kube_contexts = %v, want both clusters the dispatch applies to: [%s %s]", all, daemon, prod)
	}
}
