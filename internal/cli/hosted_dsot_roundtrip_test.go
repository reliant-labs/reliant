package cli

// THE ONE TEST THAT CANNOT BE FOOLED BY FORGE AGREEING WITH ITSELF.
//
// forge declares every deploy wire shape by hand (it does not import
// control-plane), so a misspelled json tag compiles, passes any test that
// marshals forge's own struct and reads it back, and fails only in production
// as a field that silently reads as its zero value. Every fixture below was
// produced by control-plane's OWN generated Go types, marshalled with
// protojson and pasted verbatim — so these documents are what the real server
// emits, not what forge believes it emits.
//
// Regenerate with the program in the PR body if the proto changes.
//
//	control-plane commit: 7a5253d8 ("feat(deploy): proto contract for the
//	deploy source of truth (P0)", branch dsot/p0-proto, PR #526)
//	emitted by: protojson.MarshalOptions{Multiline: true, Indent: "  "}
//	over gen/controlplane/v1 and gen/services/deploy/v1
//
// Note the SHAPE fields inside these documents: `api_version`, `config_hash`,
// `declared_by`. A google.protobuf.Struct has no proto field names of its own
// — it carries whatever object it was given — and what the control plane
// stores there is release.Shape's canonical JSON, which is snake_case. So the
// surrounding message is camelCase and the Struct's contents are not, in the
// same document. That asymmetry is exactly the kind of thing a hand-declared
// struct gets wrong, and it is pinned here.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// cpBundleFixture is controlplane.v1.DeployBundle, from cp 7a5253d8.
const cpBundleFixture = `{
  "id":  "bnd_1",
  "environmentId":  "env_prod",
  "releaseVersion":  "v1.4.0",
  "digest":  "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "reference":  "ghcr.io/acme/app/bundle.v1/prod@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "configDigest":  "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
  "shape":  {
    "clusters":  ["prod-ctx"],
    "domains":  ["app.example.com"],
    "kind":  "self_managed",
    "objects":  [
      {
        "api_version":  "apps/v1",
        "cluster":  "prod-ctx",
        "config_hash":  "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "hash":  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "identity":  {"load_balancer_ip":  "203.0.113.7", "type":  "LoadBalancer"},
        "images":  {"api":  "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
        "kind":  "Deployment",
        "name":  "api",
        "namespace":  "app",
        "workload":  "api"
      }
    ],
    "secrets":  [{"declared_by":  ["api"], "name":  "DB_URL", "provider":  "hosted"}],
    "workloads":  [{"artifact":  "api", "cluster":  "prod-ctx", "name":  "api", "runtime":  "cluster"}]
  },
  "provenance":  {
    "repo":  "github.com/reliant-labs/hounders",
    "commit":  "1111111111111111111111111111111111111111",
    "branch":  "main",
    "tag":  "v1.4.0",
    "dirty":  true,
    "tree":  "2222222222222222222222222222222222222222",
    "worktree":  {"key":  "feat-x", "label":  "feat-x", "hostId":  "host-abc"},
    "forgeVersion":  "v0.1.44",
    "attestation":  {"provider":  "github", "subject":  "repo:acme/app:ref:refs/heads/main", "runId":  "12345"}
  },
  "run":  {"id":  "github:acme/app/999/1", "url":  "https://gh/run/999", "provider":  "github"},
  "createdBy":  "token:tok_1",
  "createdAt":  "2026-10-02T10:00:00Z"
}`

// TestRoundTrip_DeployBundle_FromRealProto decodes control-plane's own
// protojson for DeployBundle and asserts every field arrived — including the
// snake_case ones inside the shape Struct.
func TestRoundTrip_DeployBundle_FromRealProto(t *testing.T) {
	t.Parallel()
	var w wireBundle
	if err := json.Unmarshal([]byte(cpBundleFixture), &w); err != nil {
		t.Fatal(err)
	}
	b, err := bundleFromWire("prod", w)
	if err != nil {
		t.Fatalf("bundleFromWire: %v", err)
	}

	if b.ID != "bnd_1" || b.Release != "v1.4.0" {
		t.Errorf("id/release = %q/%q", b.ID, b.Release)
	}
	if b.Digest != "sha256:"+rep64('d') {
		t.Errorf("digest = %q (the `digest` field never arrived)", b.Digest)
	}
	if b.ConfigDigest != "sha256:"+rep64('e') {
		t.Errorf("configDigest = %q (forge's tag disagrees with the proto's config_digest)", b.ConfigDigest)
	}
	if b.Reference == "" {
		t.Error("reference never arrived")
	}
	if b.CreatedBy != "token:tok_1" {
		t.Errorf("createdBy = %q", b.CreatedBy)
	}
	if !b.CreatedAt.Equal(time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("createdAt = %s", b.CreatedAt)
	}
	if b.Run.ID != "github:acme/app/999/1" || b.Run.Provider != "github" {
		t.Errorf("run = %+v", b.Run)
	}

	// The Struct's snake_case contents.
	if b.Shape.Kind != release.EnvSelfManaged {
		t.Errorf("shape kind = %q", b.Shape.Kind)
	}
	if len(b.Shape.Objects) != 1 {
		t.Fatalf("shape objects = %d, want 1", len(b.Shape.Objects))
	}
	obj := b.Shape.Objects[0]
	if obj.APIVersion != "apps/v1" {
		t.Errorf("object api_version = %q — the Struct holds snake_case, not camelCase", obj.APIVersion)
	}
	if obj.ConfigHash != "sha256:"+rep64('b') {
		t.Errorf("object config_hash = %q", obj.ConfigHash)
	}
	if obj.Images["api"] != "sha256:"+rep64('c') {
		t.Errorf("object images = %v", obj.Images)
	}
	if obj.Identity[release.IdentityLoadBalancerIP] != "203.0.113.7" {
		t.Errorf("object identity = %v", obj.Identity)
	}
	if len(b.Shape.Secrets) != 1 || len(b.Shape.Secrets[0].DeclaredBy) != 1 {
		t.Errorf("shape secrets = %+v — declared_by is snake_case in the Struct", b.Shape.Secrets)
	}

	// The provenance, where `hostId` and `forgeVersion` are the two names
	// most likely to be mirrored wrong.
	p := b.Provenance
	if p.Repo != "github.com/reliant-labs/hounders" || p.Commit != rep40('1') {
		t.Errorf("provenance repo/commit = %q/%q", p.Repo, p.Commit)
	}
	if p.Branch != "main" || p.Tag != "v1.4.0" || !p.Dirty || p.Tree != rep40('2') {
		t.Errorf("provenance = %+v", p)
	}
	if p.ForgeVersion != "v0.1.44" {
		t.Errorf("forgeVersion = %q", p.ForgeVersion)
	}
	if p.Worktree.Host != "host-abc" {
		t.Errorf("worktree host = %q — the proto field is host_id, i.e. hostId on the wire", p.Worktree.Host)
	}
	if p.Worktree.Key != "feat-x" || p.Worktree.Label != "feat-x" {
		t.Errorf("worktree = %+v", p.Worktree)
	}
	if p.Attestation == nil || p.Attestation.RunID != "12345" || p.Attestation.Provider != "github" {
		t.Errorf("attestation = %+v", p.Attestation)
	}
}

// cpApplyFixture is controlplane.v1.DeployApply, from cp 7a5253d8.
const cpApplyFixture = `{
  "id":  "apl_1",
  "environmentId":  "env_prod",
  "bundleId":  "bnd_1",
  "promotionId":  "pr_1",
  "appliedBy":  "usr_1",
  "run":  {"id":  "github:acme/app/999/1"},
  "supersededInFlight":  true,
  "createdAt":  "2026-10-02T10:01:00Z",
  "deadlineAt":  "2026-10-02T10:31:00Z",
  "outcome":  {
    "status":  "succeeded",
    "summary":  "3 of 3 workloads ready",
    "workloads":  {"workloads":  [{"cluster":  "prod-ctx", "name":  "api", "state":  "ready"}]},
    "finishedAt":  "2026-10-02T10:05:00Z",
    "reportedBy":  "token:tok_1"
  },
  "state":  "succeeded",
  "planDigest":  "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
  "approvedBy":  "usr_1"
}`

func TestRoundTrip_DeployApply_FromRealProto(t *testing.T) {
	t.Parallel()
	var w wireApply
	if err := json.Unmarshal([]byte(cpApplyFixture), &w); err != nil {
		t.Fatal(err)
	}
	a, outcome, err := applyFromWire("prod", w)
	if err != nil {
		t.Fatalf("applyFromWire: %v", err)
	}
	if a.ID != "apl_1" || a.BundleID != "bnd_1" || a.PromotionID != "pr_1" {
		t.Errorf("apply ids = %+v", a)
	}
	if a.AppliedBy != "usr_1" {
		t.Errorf("appliedBy = %q", a.AppliedBy)
	}
	if !a.SupersededInFlight {
		t.Error("supersededInFlight never arrived — an override that leaves no trace")
	}
	if a.PlanDigest != "sha256:"+rep64('f') {
		t.Errorf("planDigest = %q", a.PlanDigest)
	}
	if a.DeadlineAt.IsZero() {
		t.Error("deadlineAt never arrived; without it every apply reads as abandoned")
	}
	if a.Run.ID != "github:acme/app/999/1" {
		t.Errorf("run = %+v", a.Run)
	}
	if outcome == nil {
		t.Fatal("outcome never arrived")
	}
	if outcome.Status != release.ApplySucceeded {
		t.Errorf("outcome status = %q", outcome.Status)
	}
	if outcome.ReportedBy != "token:tok_1" {
		t.Errorf("reportedBy = %q — the label that keeps the report honest", outcome.ReportedBy)
	}
	if len(outcome.Workloads) != 1 || outcome.Workloads[0].State != "ready" || outcome.Workloads[0].Cluster != "prod-ctx" {
		t.Errorf("outcome workloads = %+v", outcome.Workloads)
	}

	// An apply with a terminal outcome is never "running", whatever the
	// clock says — that is the whole point of deriving the state.
	if got := release.DeriveApplyState(a, outcome, a.DeadlineAt.Add(time.Hour)); got != release.ApplyStateOK {
		t.Errorf("derived state past the deadline WITH an outcome = %q, want succeeded", got)
	}
	// With no outcome, the deadline is what separates running from
	// abandoned. Both answers are pinned, because reading abandoned as
	// success is the dangerous one.
	bare := a
	if got := release.DeriveApplyState(bare, nil, bare.CreatedAt.Add(time.Minute)); got != release.ApplyRunning {
		t.Errorf("no outcome, before the deadline = %q, want running", got)
	}
	if got := release.DeriveApplyState(bare, nil, bare.DeadlineAt.Add(time.Minute)); got != release.ApplyAbandoned {
		t.Errorf("no outcome, past the deadline = %q, want abandoned", got)
	}
}

// cpLiveEnvFixture is controlplane.v1.DeployLiveEnvironment, from cp 7a5253d8.
const cpLiveEnvFixture = `{
  "environment":  {
    "id":  "env_prod",
    "name":  "prod",
    "kind":  "DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED",
    "namespace":  "acme-prod",
    "project":  "hounders",
    "declaredShape":  {
      "clusters":  ["prod-ctx"],
      "domains":  ["app.example.com"],
      "kind":  "self_managed",
      "objects":  [
        {
          "api_version":  "apps/v1",
          "cluster":  "prod-ctx",
          "hash":  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          "kind":  "Deployment",
          "name":  "api",
          "namespace":  "app"
        }
      ],
      "secrets":  [{"name":  "DB_URL", "provider":  "hosted"}],
      "workloads":  [{"name":  "api", "runtime":  "cluster"}]
    },
    "declaredBy":  {
      "repo":  "github.com/reliant-labs/hounders",
      "commit":  "1111111111111111111111111111111111111111",
      "branch":  "main",
      "dirty":  true,
      "tree":  "2222222222222222222222222222222222222222",
      "worktree":  {"key":  "feat-x", "label":  "feat-x", "hostId":  "host-abc"},
      "forgeVersion":  "v0.1.44"
    },
    "declaredAt":  "2026-10-02T09:59:00Z"
  },
  "currentPromotion":  {
    "id":  "pr_1",
    "environmentId":  "env_prod",
    "releaseId":  "rel_1",
    "kind":  "DEPLOY_PROMOTION_KIND_PROMOTE",
    "createdAt":  "2026-10-02T10:00:10Z",
    "releaseVersion":  "v1.4.0",
    "importedFrom":  ".forge/promotions/prod.jsonl#12",
    "planDigest":  "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
    "approvedBy":  "usr_1",
    "acknowledgedFindings":  ["stateful_deletion"]
  },
  "phase":  "DEPLOY_ROLLOUT_PHASE_SUCCEEDED"
}`

func TestRoundTrip_DeployLiveEnvironment_FromRealProto(t *testing.T) {
	t.Parallel()
	var row wireLiveEnvRow
	if err := json.Unmarshal([]byte(cpLiveEnvFixture), &row); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	live, err := liveEnvFromWire(row, now)
	if err != nil {
		t.Fatalf("liveEnvFromWire: %v", err)
	}
	if live.EnvironmentID != "env_prod" || live.Env.Name != "prod" {
		t.Errorf("env = %+v", live.Env)
	}
	// SELF_MANAGED is the kind P0 ADDS. If forge did not mirror it, the
	// read refuses rather than guessing, which is why this assertion is
	// about the kind having been recognised at all.
	if live.Env.Kind != release.EnvSelfManaged {
		t.Errorf("kind = %q, want self_managed", live.Env.Kind)
	}
	if live.Namespace != "acme-prod" {
		t.Errorf("namespace = %q", live.Namespace)
	}
	if live.Env.DeclaredShape == nil {
		t.Fatal("declaredShape never arrived (F-DECL's whole point)")
	}
	if len(live.Env.DeclaredShape.Objects) != 1 || live.Env.DeclaredShape.Objects[0].APIVersion != "apps/v1" {
		t.Errorf("declared shape objects = %+v", live.Env.DeclaredShape.Objects)
	}
	if live.Env.DeclaredAt == nil {
		t.Error("declaredAt never arrived; without it a declaration cannot be judged stale")
	}
	if live.Env.DeclaredBy == nil || live.Env.DeclaredBy.ForgeVersion != "v0.1.44" {
		t.Errorf("declaredBy = %+v", live.Env.DeclaredBy)
	}
	if live.CurrentPromotion == nil {
		t.Fatal("currentPromotion never arrived")
	}
	if live.CurrentPromotion.Release != "v1.4.0" {
		t.Errorf("promotion release = %q", live.CurrentPromotion.Release)
	}
	if live.CurrentPromotion.PlanDigest != "sha256:"+rep64('f') {
		t.Errorf("promotion planDigest = %q — O-13's record of which review this promotion was written under", live.CurrentPromotion.PlanDigest)
	}
	if live.CurrentPromotion.ApprovedBy != "usr_1" {
		t.Errorf("promotion approvedBy = %q", live.CurrentPromotion.ApprovedBy)
	}
	if len(live.CurrentPromotion.AcknowledgedFindings) != 1 {
		t.Errorf("acknowledgedFindings = %v", live.CurrentPromotion.AcknowledgedFindings)
	}
	if rolloutPhaseName(live.Phase) != "succeeded" {
		t.Errorf("phase = %q", live.Phase)
	}
	// Phase B fills these; Phase A leaves them empty. nil is MEANINGFUL
	// here — no bundle means no config has been applied — so the test pins
	// that the absent fields decode as absent rather than as zero values
	// pretending to be records.
	if live.CurrentBundle != nil || live.LatestApply != nil {
		t.Error("a row with no bundle or apply must leave both nil, not zero-valued")
	}
	if live.Drift != nil {
		t.Error("a row with no drift must leave it nil: unobservable is not in_sync")
	}
}

// cpSessionFixture is controlplane.v1.DeployLocalSession, from cp 7a5253d8.
const cpSessionFixture = `{
  "id":  "ses_1",
  "environmentId":  "env_dev",
  "worktree":  {"key":  "feat-x", "label":  "feat-x", "hostId":  "host-abc"},
  "provenance":  {
    "repo":  "github.com/reliant-labs/hounders",
    "commit":  "1111111111111111111111111111111111111111",
    "branch":  "feat-x",
    "dirty":  true,
    "tree":  "2222222222222222222222222222222222222222",
    "worktree":  {"key":  "feat-x", "label":  "feat-x", "hostId":  "host-abc"},
    "forgeVersion":  "v0.1.44"
  },
  "bundleDigest":  "sha256:7777777777777777777777777777777777777777777777777777777777777777",
  "startedAt":  "2026-10-02T09:00:00Z",
  "lastSeenAt":  "2026-10-02T09:05:00Z",
  "stoppedAt":  "2026-10-02T09:30:00Z"
}`

func TestRoundTrip_DeployLocalSession_FromRealProto(t *testing.T) {
	t.Parallel()
	var w wireLocalSession
	if err := json.Unmarshal([]byte(cpSessionFixture), &w); err != nil {
		t.Fatal(err)
	}
	s, err := localSessionFromWire("dev", w)
	if err != nil {
		t.Fatalf("localSessionFromWire: %v", err)
	}
	if s.ID != "ses_1" || s.Env != "dev" {
		t.Errorf("session = %+v", s)
	}
	if s.Worktree.Host != "host-abc" || s.Worktree.Key != "feat-x" {
		t.Errorf("worktree = %+v — identity is (env, host, worktree key)", s.Worktree)
	}
	if s.BundleDigest != "sha256:"+rep64('7') {
		t.Errorf("bundleDigest = %q", s.BundleDigest)
	}
	if s.StoppedAt == nil {
		t.Fatal("stoppedAt never arrived")
	}
	// A stopped session is not live, and a stopped session is never stale:
	// "went quiet" and "exited cleanly" are different facts, and staleness
	// only means anything about the first.
	if s.Live() {
		t.Error("a session with stoppedAt is not live")
	}
	if s.Stale(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)) {
		t.Error("a STOPPED session must never read as stale — it exited cleanly")
	}
}

// cpDriftFixture is controlplane.v1.DeployDrift, from cp 7a5253d8.
const cpDriftFixture = `{
  "state":  "drifted",
  "bundleId":  "bnd_1",
  "applyId":  "apl_1",
  "observedAt":  "2026-10-02T11:00:00Z",
  "objects":  [
    {
      "cluster":  "prod-ctx",
      "kind":  "Deployment",
      "namespace":  "app",
      "name":  "api",
      "expectedHash":  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "observedHash":  "sha256:6666666666666666666666666666666666666666666666666666666666666666",
      "state":  "drifted"
    }
  ],
  "detail":  "1 object changed outside forge"
}`

func TestRoundTrip_DeployDrift_FromRealProto(t *testing.T) {
	t.Parallel()
	var w wireDrift
	if err := json.Unmarshal([]byte(cpDriftFixture), &w); err != nil {
		t.Fatal(err)
	}
	d := driftFromWire(w)
	if d.State != driftDrifted || !d.Drifted() {
		t.Errorf("state = %q", d.State)
	}
	if d.BundleID != "bnd_1" || d.ApplyID != "apl_1" {
		t.Errorf("drift ids = %+v", d)
	}
	if d.ObservedAt == nil {
		t.Error("observedAt never arrived")
	}
	if len(d.Objects) != 1 {
		t.Fatalf("objects = %d", len(d.Objects))
	}
	o := d.Objects[0]
	if o.Key.String() != "prod-ctx/Deployment/app/api" {
		t.Errorf("object key = %q", o.Key)
	}
	if o.ExpectedHash == "" || o.ObservedHash == "" || o.State != driftObjectDrifted {
		t.Errorf("object = %+v", o)
	}
}

// TestDriftFromWire_AbsentStateIsUnknownNeverInSync: the dangerous default
// here is agreement. A server that sends no state has told us nothing, and
// "we cannot see it" must never be reported as "it matches" — a self-managed
// cluster has no server-side observer, so that is the NORMAL answer there.
func TestDriftFromWire_AbsentStateIsUnknownNeverInSync(t *testing.T) {
	t.Parallel()
	d := driftFromWire(wireDrift{BundleID: "bnd_1"})
	if d.State != driftUnknown {
		t.Errorf("state = %q, want %q", d.State, driftUnknown)
	}
	if d.Drifted() {
		t.Error("unknown is not drifted either")
	}
	// Per-object, the same rule: an object nobody could look at is an
	// absence of information, not a verdict.
	d = driftFromWire(wireDrift{State: driftUnknown, Objects: []wireDriftObject{{Kind: "Deployment", Name: "api"}}})
	if d.Objects[0].State != driftObjectUnobservable {
		t.Errorf("object state = %q, want %q", d.Objects[0].State, driftObjectUnobservable)
	}
}

func rep64(c byte) string { return repByte(c, 64) }
func rep40(c byte) string { return repByte(c, 40) }

func repByte(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
