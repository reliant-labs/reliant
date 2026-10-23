package cli

// Request goldens: what forge SENDS, asserted against the proto's own field
// names (protojson lowerCamel).
//
// Each golden below was checked against a document protojson emitted from
// control-plane's generated request type at commit 7a5253d8 — the same source
// as the response fixtures in hosted_dsot_roundtrip_test.go. A request forge
// spells wrongly is not an error anywhere: connect-go's JSON codec DISCARDS
// unknown fields, so a misspelled `planDigest` means the server receives no
// plan digest at all and silently admits a promote that should have been
// judged. That is the failure these tests exist to make loud.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// fakeDSOTCaller records requests and answers with canned replies, in the
// fakeCPCaller shape (cloud_envs_test.go) rather than a second pattern.
type fakeDSOTCaller struct {
	mu      sync.Mutex
	calls   []fakeCPCall
	replies map[string]any
	// errs is what a procedure returns instead of a reply.
	errs map[string]error
	// reply, when set and when it claims a procedure, answers from the
	// REQUEST — for a test whose server has to be self-consistent with the
	// bytes it was sent rather than canned. Checked before replies.
	reply func(proc string, body map[string]any) (any, bool)
}

func (f *fakeDSOTCaller) Call(_ context.Context, proc string, req, out any) error {
	raw, _ := json.Marshal(req)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCPCall{Proc: proc, Body: body})
	err := f.errs[proc]
	reply, ok := f.replies[proc]
	if f.reply != nil {
		if dynamic, claimed := f.reply(proc, body); claimed {
			reply, ok = dynamic, true
		}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("unexpected procedure " + proc)
	}
	if out == nil {
		return nil
	}
	encoded, merr := json.Marshal(reply)
	if merr != nil {
		return merr
	}
	return json.Unmarshal(encoded, out)
}

func (f *fakeDSOTCaller) body(t *testing.T, proc string) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Proc == proc {
			return c.Body
		}
	}
	t.Fatalf("no call to %s; calls were %+v", proc, f.calls)
	return nil
}

// wantFields asserts each key's value, naming the proto field in the failure
// so a mismatch reads as "forge spells this differently from the proto".
func wantFields(t *testing.T, body map[string]any, want map[string]any) {
	t.Helper()
	for key, expected := range want {
		got, present := body[key]
		if !present {
			t.Errorf("request has no %q; the server discards unknown fields, so this reads server-side as absent. body = %v", key, body)
			continue
		}
		if !jsonEqual(got, expected) {
			t.Errorf("request %q = %#v, want %#v", key, got, expected)
		}
	}
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func testProvenance() release.Provenance {
	return release.Provenance{
		Repo: "github.com/reliant-labs/hounders", Commit: rep40('1'),
		Branch: "main", Dirty: true, Tree: rep40('2'),
		Worktree:     release.Worktree{Key: "feat-x", Label: "feat-x", Host: "host-abc", Path: "/Users/someone/src/app"},
		ForgeVersion: "v0.1.44",
	}
}

// dsotTestShape is a shape with rendered OBJECTS, which the plan tests need:
// ledger_records_test.go's testShape declares workloads only, and BuildPlan
// has nothing to diff without objects.
func dsotTestShape() release.Shape {
	return release.Shape{
		Kind:      release.EnvSelfManaged,
		Workloads: []release.ShapeWorkload{{Name: "api", Runtime: "cluster", Cluster: "prod-ctx"}},
		Secrets:   []release.ShapeSecret{{Name: "DB_URL", Provider: "hosted"}},
		Clusters:  []string{"prod-ctx"},
		Objects: []release.ShapeObject{{
			Cluster: "prod-ctx", APIVersion: "apps/v1", Kind: "Deployment",
			Namespace: "app", Name: "api", Hash: "sha256:" + rep64('a'),
		}},
	}
}

// ─── RecordBundle ────────────────────────────────────────────────────────────

// TestRecordBundle_SendsTheBytesNeverADescription pins RecordDeployBundleRequest:
// environmentId, repository, manifest, config, run — and NOTHING that describes
// the bundle, because the server derives the shape, provenance, config digest
// and release from the verified config blob.
func TestRecordBundle_SendsTheBytesNeverADescription(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		procRecordBundle: map[string]any{
			"bundle":  json.RawMessage(cpBundleFixture),
			"created": true,
		},
	}}
	c := hostedBundleClient{client: f}
	manifest := []byte(`{"schemaVersion":2}`)
	config := []byte(`{"schema":"forge.dev/bundle/v1"}`)
	rec, created, err := c.RecordBundle(context.Background(), "prod", "env_prod",
		"ghcr.io/acme/app/bundle.v1/prod", manifest, config,
		release.Run{ID: "github:acme/app/999/1"})
	if err != nil {
		t.Fatal(err)
	}
	if !created || rec.ID != "bnd_1" {
		t.Errorf("created=%v id=%q", created, rec.ID)
	}

	body := f.body(t, procRecordBundle)
	wantFields(t, body, map[string]any{
		"environmentId": "env_prod",
		"repository":    "ghcr.io/acme/app/bundle.v1/prod",
		// proto3 JSON encodes `bytes` as base64, which is what the
		// control-plane fixture shows and what encoding/json produces for
		// a []byte. Pinned literally, because a []byte accidentally typed
		// as a string would send the raw JSON text and the server's
		// sha256 would not match.
		"manifest": "eyJzY2hlbWFWZXJzaW9uIjoyfQ==",
		"config":   "eyJzY2hlbWEiOiJmb3JnZS5kZXYvYnVuZGxlL3YxIn0=",
		"run":      map[string]any{"id": "github:acme/app/999/1"},
	})
	// THE ABSENCE IS THE CONTRACT. A client that could state these could
	// record a shape that disagrees with the bytes it pushed.
	for _, forbidden := range []string{"shape", "provenance", "configDigest", "digest", "releaseVersion"} {
		if _, present := body[forbidden]; present {
			t.Errorf("RecordBundle must not DESCRIBE the bundle; it sent %q", forbidden)
		}
	}
}

// TestRecordBundle_RefusesWithoutBytes: a record whose bytes are absent could
// only be a description, so it is refused before a round trip rather than
// sent and rejected.
func TestRecordBundle_RefusesWithoutBytes(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{}
	c := hostedBundleClient{client: f}
	if _, _, err := c.RecordBundle(context.Background(), "prod", "env_prod",
		"r", nil, []byte("{}"), release.Run{}); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("want release.ErrInvalid, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("nothing should have been sent; calls = %+v", f.calls)
	}
}

// TestGetBundle_NotFoundIsAnAnswer: a bundle nobody recorded is (nil, nil),
// the same rule hostedStore.Get follows for a version nobody cut. An error
// would make "never recorded" indistinguishable from "could not read".
func TestGetBundle_NotFoundIsAnAnswer(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{errs: map[string]error{
		procGetBundle: &cloud.Error{Code: cloud.CodeNotFound, Message: "no such bundle"},
	}}
	c := hostedBundleClient{client: f}
	got, err := c.GetBundleByDigest(context.Background(), "prod", "env_prod", "sha256:"+rep64('d'))
	if err != nil {
		t.Fatalf("not_found must be an answer, got %v", err)
	}
	if got != nil {
		t.Errorf("want nil bundle, got %+v", got)
	}
	wantFields(t, f.body(t, procGetBundle), map[string]any{
		"environmentId": "env_prod",
		"digest":        "sha256:" + rep64('d'),
	})
}

// ─── Reading a convergence record ────────────────────────────────────────────

// The BeginApply / FinishApply / ListApplies request tests are gone with the
// requests, which were deleted as dead code — nothing in forge ever issued
// one. What survives is the half with a real consumer: DECODING a record the
// control plane's observer wrote, which arrives on GetLiveView.
//
// TestApplyFromWire_DerivesAbandonedRatherThanSuccess keeps the one property
// those tests were really protecting. An apply with no outcome means two
// different things, and only the deadline separates them: before it the work
// is running, after it nobody is ever going to report. Reading the absence as
// success would turn "we could not look" into "it worked", which is failure
// mode F-2 and the reason the derivation is in one place.
func TestApplyFromWire_DerivesAbandonedRatherThanSuccess(t *testing.T) {
	t.Parallel()
	const noOutcome = `{"id":"apl_2","environmentId":"env_prod","bundleId":"bnd_2",
	  "createdAt":"2026-10-02T11:00:00Z","deadlineAt":"2026-10-02T11:30:00Z"}`

	var w wireApply
	if err := json.Unmarshal([]byte(noOutcome), &w); err != nil {
		t.Fatal(err)
	}
	a, outcome, err := applyFromWire("prod", w)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != nil {
		t.Error("a record with no outcome decodes to no outcome; the absence IS the fact")
	}
	past := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) // past the deadline
	if got := release.DeriveApplyState(a, outcome, past); got != release.ApplyAbandoned {
		t.Errorf("past its deadline with no outcome = %q, want abandoned (never success)", got)
	}
	within := time.Date(2026, 10, 2, 11, 15, 0, 0, time.UTC)
	if got := release.DeriveApplyState(a, outcome, within); got != release.ApplyRunning {
		t.Errorf("before its deadline with no outcome = %q, want running", got)
	}

	// And a terminal outcome decodes as itself.
	var done wireApply
	if err := json.Unmarshal([]byte(cpApplyFixture), &done); err != nil {
		t.Fatal(err)
	}
	da, doutcome, err := applyFromWire("prod", done)
	if err != nil {
		t.Fatal(err)
	}
	if doutcome == nil {
		t.Fatal("the fixture carries an outcome")
	}
	if got := release.DeriveApplyState(da, doutcome, past); got != release.ApplyStateOK {
		t.Errorf("a succeeded record = %q, want %q", got, release.ApplyStateOK)
	}
}

// ─── PlanDeploy ──────────────────────────────────────────────────────────────

// TestPlanDeploy_SendsTheThreeFieldsAndNothingElse: PlanDeployRequest is
// environment, bundle, optional release version. A pure read.
func TestPlanDeploy_SendsTheThreeFields(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)
	f := &fakeDSOTCaller{replies: map[string]any{
		procPlanDeploy: map[string]any{"plan": planToWireFixture(plan)},
	}}
	c := hostedPlanClient{client: f}
	got, err := c.PlanDeploy(context.Background(), "env_prod", "bnd_1", "v1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != plan.Digest {
		t.Errorf("digest = %q, want %q", got.Digest, plan.Digest)
	}
	wantFields(t, f.body(t, procPlanDeploy), map[string]any{
		"environmentId":  "env_prod",
		"bundleId":       "bnd_1",
		"releaseVersion": "v1.4.0",
	})
}

// ─── GetLiveView ─────────────────────────────────────────────────────────────

// TestGetLiveView_SendsProjectAndIncludeDeleted, and an empty env list is an
// EMPTY LIST, never an error: a project with no hosted envs has nothing Live
// to show, which is a fact rather than a failure.
func TestGetLiveView_EmptyIsAListNotAnError(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		procGetLiveView: map[string]any{"environments": []any{}},
	}}
	c := hostedLiveClient{client: f}
	rows, err := c.GetLiveView(context.Background(), "hounders", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %d", len(rows))
	}
	wantFields(t, f.body(t, procGetLiveView), map[string]any{
		"project":        "hounders",
		"includeDeleted": true,
	})
}

// ─── ReportLocalSession ──────────────────────────────────────────────────────

// TestReportLocalSession_SendsPresenceAndStripsThePath pins
// ReportLocalSessionRequest, and that the worktree PATH — which names a
// user's home directory — never leaves the machine.
func TestReportLocalSession_SendsPresenceAndStripsThePath(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		procReportLocalSession: map[string]any{"session": json.RawMessage(cpSessionFixture)},
	}}
	c := hostedSessionClient{client: f}
	started := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	if _, err := c.ReportSession(context.Background(), "hounders", release.LocalSession{
		ID: "ses_1", Env: "dev",
		Worktree:     release.Worktree{Key: "feat-x", Label: "feat-x", Host: "host-abc", Path: "/Users/someone/src/app"},
		Provenance:   testProvenance(),
		BundleDigest: "sha256:" + rep64('7'),
		StartedAt:    started, LastSeenAt: started.Add(5 * time.Minute),
	}, false); err != nil {
		t.Fatal(err)
	}
	body := f.body(t, procReportLocalSession)
	wantFields(t, body, map[string]any{
		"sessionId":   "ses_1",
		"project":     "hounders",
		"environment": "dev",
		// `host_id`, not `host`: the proto name and release.Worktree's
		// field name differ, which is exactly the kind of mismatch a
		// hand-declared struct loses silently.
		"worktree":     map[string]any{"key": "feat-x", "label": "feat-x", "hostId": "host-abc"},
		"bundleDigest": "sha256:" + rep64('7'),
		"state":        sessionRunning,
	})
	worktree, _ := body["worktree"].(map[string]any)
	if _, present := worktree["path"]; present {
		t.Error("the worktree PATH names a user's home directory and must never be sent")
	}
	prov, _ := body["provenance"].(map[string]any)
	if pwt, _ := prov["worktree"].(map[string]any); pwt != nil {
		if _, present := pwt["path"]; present {
			t.Error("the provenance's worktree path must be stripped too (Provenance.ForHosted)")
		}
	}

	// Teardown is the same request with state = stopped, which is what
	// sets stopped_at — a clean exit, as distinct from going quiet.
	f.calls = nil
	if _, err := c.ReportSession(context.Background(), "hounders", release.LocalSession{
		ID: "ses_1", Env: "dev",
		Worktree:  release.Worktree{Host: "host-abc"},
		StartedAt: started, LastSeenAt: started,
	}, true); err != nil {
		t.Fatal(err)
	}
	wantFields(t, f.body(t, procReportLocalSession), map[string]any{"state": sessionStopped})
}

// TestReportSessionBestEffort_NeverBlocksTheStack: presence is a
// nice-to-have, and `forge env up` must start whether or not the control
// plane is reachable (§7.4). The error is returned for the caller to LOG, and
// the name is what stops the reflex `if err != nil { return err }`.
func TestReportSessionBestEffort_ReturnsAnErrorThatNamesItselfHarmless(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{errs: map[string]error{
		procReportLocalSession: &cloud.Error{Code: cloud.CodeUnavailable, Message: "down"},
	}}
	c := hostedSessionClient{client: f}
	started := time.Now().UTC()
	err := c.ReportSessionBestEffort(context.Background(), "hounders", release.LocalSession{
		ID: "ses_1", Env: "dev", Worktree: release.Worktree{Host: "h"},
		StartedAt: started, LastSeenAt: started,
	}, false)
	if err == nil {
		t.Fatal("want the failure reported to the caller")
	}
	if !errContains(err, "the stack is unaffected") {
		t.Errorf("the message must tell the caller this is not fatal; got %q", err)
	}
}

// ─── ImportLedger ────────────────────────────────────────────────────────────

// TestImportLedger_SendsTypedItemListsPerRecord pins every list in
// ImportLedgerRequest, and that `importedFrom` rides on each ITEM rather than
// once on the request: the import is idempotent per RECORD, so two runs over
// overlapping ranges must dedupe individually.
func TestImportLedger_SendsTypedItemListsPerRecord(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		procImportLedger: map[string]any{
			"counts":    map[string]any{"releases": 1, "promotions": 1, "bundles": 1, "applies": 1},
			"conflicts": []any{"env prod already holds a non-imported promotion"},
		},
	}}
	c := hostedImportClient{client: f}

	cut := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	promoted := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	deleted := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	prov := testProvenance()

	rel := release.Release{
		Version: "v1.0.0", CreatedAt: cut,
		Artifacts: map[string]release.Artifact{"api": {
			Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: "sha256:" + rep64('c')},
		}},
	}
	rel.SetProvenance(prov)

	li := newLedgerImport("control-plane", "git:origin/main", true)
	if !li.Empty() {
		t.Error("a fresh import writes nothing until something is added")
	}
	li.AddRelease(rel, ".forge/releases/v1.0.0.json")
	li.AddEnvironment("prod", release.EnvSelfManaged, &deleted, ".forge/envs/prod")
	li.AddPromotion(release.Promotion{
		Env: "prod", Release: "v1.0.0", Kind: release.KindPromote,
		Resolved: map[string]string{"api": "sha256:" + rep64('c')},
		FromEnv:  "staging", Note: "first",
		PromotedBy: release.Actor{Actor: "ci"},
	}, promoted, ".forge/promotions/prod.jsonl#1")
	li.AddBundle(release.BundleRecord{
		Env: "prod", Release: "v1.0.0", Digest: "sha256:" + rep64('d'),
		Reference: "oci:local", ConfigDigest: "sha256:" + rep64('e'),
		Shape: dsotTestShape(), Provenance: prov, CreatedAt: promoted,
	}, ".forge/bundles/1")
	li.AddApply("prod", "sha256:"+rep64('d'), &release.ApplyOutcome{
		ApplyID: "x", Status: release.ApplySucceeded,
		FinishedAt: promoted.Add(5 * time.Minute),
	}, promoted, ".forge/applies/1")
	if li.Empty() {
		t.Error("Empty must report what was added")
	}

	res, err := c.ImportLedger(context.Background(), li)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["releases"] != 1 || res.Counts["promotions"] != 1 {
		t.Errorf("counts = %v", res.Counts)
	}
	// A non-empty conflict list with NO error is the normal outcome of a
	// partial import: the transaction committed what it could name
	// unambiguously and said what it declined.
	if len(res.Conflicts) != 1 {
		t.Errorf("conflicts = %v", res.Conflicts)
	}

	body := f.body(t, procImportLedger)
	wantFields(t, body, map[string]any{
		"project": "control-plane",
		"source":  "git:origin/main",
		"dryRun":  true,
	})

	rels, _ := body["releases"].([]any)
	if len(rels) != 1 {
		t.Fatalf("releases = %v", body["releases"])
	}
	r0, _ := rels[0].(map[string]any)
	wantFields(t, r0, map[string]any{
		"version":      "v1.0.0",
		"createdAt":    "2025-01-01T00:00:00Z",
		"importedFrom": ".forge/releases/v1.0.0.json",
	})
	if _, present := r0["provenance"]; !present {
		t.Error("an imported release carries its provenance")
	}

	envs, _ := body["environments"].([]any)
	e0, _ := envs[0].(map[string]any)
	wantFields(t, e0, map[string]any{
		"name": "prod",
		// The enum's VALUE NAME, as protojson writes it, not release's
		// lowercase "self_managed".
		"kind":         "DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED",
		"deletedAt":    "2025-06-01T00:00:00Z",
		"importedFrom": ".forge/envs/prod",
	})

	proms, _ := body["promotions"].([]any)
	p0, _ := proms[0].(map[string]any)
	wantFields(t, p0, map[string]any{
		"environment":       "prod",
		"releaseVersion":    "v1.0.0",
		"resolvedArtifacts": map[string]any{"api": "sha256:" + rep64('c')},
		"fromEnvironment":   "staging",
		"note":              "first",
		// The HISTORICAL name, provenance only. Never promotedByUserId:
		// that column references a real account, and an imported name is
		// not one.
		"promotedByActor": "ci",
		// EXPLICIT, never "now": the whole point of an import is that
		// these events already happened.
		"promotedAt":   "2025-01-02T00:00:00Z",
		"importedFrom": ".forge/promotions/prod.jsonl#1",
	})
	if _, present := p0["promotedByUserId"]; present {
		t.Error("an imported promotion must not claim a real user account")
	}

	bundles, _ := body["bundles"].([]any)
	b0, _ := bundles[0].(map[string]any)
	wantFields(t, b0, map[string]any{
		"environment":    "prod",
		"releaseVersion": "v1.0.0",
		"digest":         "sha256:" + rep64('d'),
		"reference":      "oci:local",
		"configDigest":   "sha256:" + rep64('e'),
		"createdAt":      "2025-01-02T00:00:00Z",
		"importedFrom":   ".forge/bundles/1",
	})
	shape, _ := b0["shape"].(map[string]any)
	if shape == nil {
		t.Fatal("an imported bundle carries its shape")
	}
	// The Struct holds release.Shape's canonical JSON: snake_case keys,
	// inside a camelCase message.
	objs, _ := shape["objects"].([]any)
	if len(objs) != 1 {
		t.Fatalf("shape objects = %v", shape["objects"])
	}
	if o0, _ := objs[0].(map[string]any); o0["api_version"] != "apps/v1" {
		t.Errorf("the shape Struct must hold snake_case keys; got %v", o0)
	}

	applies, _ := body["applies"].([]any)
	a0, _ := applies[0].(map[string]any)
	wantFields(t, a0, map[string]any{
		"environment": "prod",
		// By DIGEST, not id: the import is assigning the ids.
		"bundleDigest": "sha256:" + rep64('d'),
		"createdAt":    "2025-01-02T00:00:00Z",
		"importedFrom": ".forge/applies/1",
	})
	outcome, _ := a0["outcome"].(map[string]any)
	if outcome["status"] != "succeeded" {
		t.Errorf("imported apply outcome = %v", outcome)
	}
	if _, present := outcome["reportedBy"]; present {
		t.Error("nobody can attest to who observed a 2025 apply; reportedBy is the server's to set")
	}
}

// ─── The hosted record seam (F3's sessionReporter) ───────────────────────────

// TestHostedRecordStore_SatisfiesSessionReporter: the hosted twin of
// machineRecordStore, for the one seam whose signature can carry the hosted
// contract losslessly.
//
// The seam has no `stopped` parameter and does not need one: a stopped session
// is one whose StoppedAt is set, so BOTH backends derive the same fact from
// the same field rather than from a flag one of them is told. This test pins
// that derivation, because getting it wrong means a torn-down stack shows as
// running in Live until the 24 h GC reaps it.
func TestHostedRecordStore_SatisfiesSessionReporter(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		procReportLocalSession: map[string]any{"session": json.RawMessage(cpSessionFixture)},
	}}
	var reporter sessionReporter = hostedRecordStoreFor(f, "hounders")

	started := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	live := release.LocalSession{
		ID: "ses_1", Env: "dev",
		Worktree:  release.Worktree{Key: "feat-x", Host: "host-abc"},
		StartedAt: started, LastSeenAt: started.Add(time.Minute),
	}
	if err := reporter.ReportSession(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	wantFields(t, f.body(t, procReportLocalSession), map[string]any{
		"project": "hounders", "environment": "dev", "state": sessionRunning,
	})

	f.calls = nil
	stopped := live
	at := started.Add(30 * time.Minute)
	stopped.StoppedAt = &at
	if err := reporter.ReportSession(context.Background(), stopped); err != nil {
		t.Fatal(err)
	}
	wantFields(t, f.body(t, procReportLocalSession), map[string]any{"state": sessionStopped})
}

// errContains reads an error's rendered message. Used only where the MESSAGE
// is the contract — the operator-facing sentence F-15 names, and the flag a
// stop-class refusal must point at. Never for control flow: that is what the
// reason and the typed errors above are for.
func errContains(err error, sub string) bool {
	return err != nil && contains(err.Error(), sub)
}
