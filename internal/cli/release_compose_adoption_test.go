package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	digestpkg "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// Extends the real KCL/memory-OCI outer fixture with first-adoption recovery.
// Approval is fresh after desired append: an old approval is refused unchanged.
func testCompositionAdoptionRecovery(ctx context.Context, t *testing.T, registry *memory.Store, s release.Snapshot, ledger envLedger, fake *fakeDSOTCaller, statuses *[]string) {
	t.Helper()
	s.Base = nil
	s.BootstrapFromPromotionID = "legacy-promotion"
	s.Selected = nil
	s.Closure = nil
	for _, c := range s.Consumers {
		s.Selected = append(s.Selected, c.Key)
		s.Closure = append(s.Closure, c.Key)
	}
	built, err := bundle.Build(ctx, bundle.BuildInput{Project: s.Project, Env: s.Environment, Release: "adoption", Snapshot: &s, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.Push(ctx, registry, built); err != nil {
		t.Fatal(err)
	}
	if err := registry.Tag(ctx, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digestpkg.Digest(built.Digest), Size: int64(len(built.Manifest))}, built.Digest); err != nil {
		t.Fatal(err)
	}
	snapshotDigest, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r := release.Release{Version: "adoption", Project: s.Project, Catalog: s.Artifacts, SnapshotIntents: map[string]string{s.Environment: snapshotDigest}}
	current := wirePromotion{ID: s.BootstrapFromPromotionID, ReleaseVersion: "legacy", Kind: wireKindPromote}
	written := wirePromotion{ID: "adopted-promotion", ReleaseVersion: r.Version, Kind: wireKindPromote, Snapshot: &s, SnapshotDigest: snapshotDigest, BundleID: "adoption-bundle", BundleDigest: built.Digest}
	fake.replies["controlplane.v1.DeployService/ListBundles"] = map[string]any{"bundles": []wireBundle{{ID: written.BundleID, ReleaseVersion: r.Version, Digest: built.Digest, Reference: "registry.test/acme@" + built.Digest, Snapshot: &s, SnapshotDigest: snapshotDigest}}}
	var plan release.Plan
	updatePlan := func() {
		plan, err = release.BuildPlan(release.PlanInput{EnvironmentID: "env-prod", BundleID: written.BundleID, ReleaseVersion: r.Version, Candidate: built.Doc.Shape, CandidateConfigDigest: built.Doc.ConfigDigest, Basis: release.PlanBasis{CurrentPromotionID: current.ID}})
		if err != nil {
			t.Fatal(err)
		}
		w := wirePlan{Digest: plan.Digest, EnvironmentID: plan.EnvironmentID, BundleID: plan.BundleID, ReleaseVersion: plan.ReleaseVersion, LiveBasis: &wirePlanBasis{CurrentPromotionID: current.ID}, ConfigIdentical: plan.ConfigIdentical}
		for _, finding := range plan.Findings {
			w.Findings = append(w.Findings, wirePlanFinding{Code: finding.Code, Class: string(finding.Class), Section: finding.Section, Subject: finding.Subject, Detail: finding.Detail})
		}
		fake.replies[procPlanDeploy] = map[string]any{"plan": w}
	}
	updatePlan()
	originalReply := fake.reply
	defer func() { fake.reply = originalReply }()
	promotions := 0
	fake.reply = func(proc string, body map[string]any) (any, bool) {
		switch proc {
		case procListPromotions:
			return map[string]any{"promotions": []wirePromotion{current}}, true
		case procPromote:
			if body["expectedCurrentPromotionId"] != s.BootstrapFromPromotionID {
				t.Errorf("adoption retry lost original captured expectation: %v", body)
			}
			if current.ID == s.BootstrapFromPromotionID {
				promotions++
				current = written
			} else if current.ID != written.ID {
				t.Errorf("unexpected current %s", current.ID)
			}
			return map[string]any{"promotion": written}, true
		}
		if originalReply != nil {
			return originalReply(proc, body)
		}
		return nil, false
	}
	oldApply := snapshotClusterApply
	defer func() { snapshotClusterApply = oldApply }()
	interrupted := true
	applies := 0
	snapshotClusterApply = func(_ context.Context, candidate release.Snapshot, _ string, _ cluster.ApplyOpts) error {
		applies++
		d, e := candidate.Digest()
		if e != nil || d != snapshotDigest {
			t.Fatal("recovery applied different snapshot")
		}
		if interrupted {
			return errors.New("interrupted adoption apply")
		}
		return nil
	}
	before := len(*statuses)
	options := promoteOptions{ExpectCurrent: s.BootstrapFromPromotionID, Approval: deployApproval{Digest: plan.Digest}, Follow: &promoteFollowOptions{}}
	if err := runSnapshotPromote(ctx, s.Environment, r, ledger, options); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("expected interrupted adoption: %v", err)
	}
	if promotions != 1 || len(*statuses) != before+1 || (*statuses)[before] != "failed" {
		t.Fatal("first adoption did not retain desired/failed baseline")
	}
	oldDigest := options.Approval.Digest
	updatePlan()
	if plan.Digest == oldDigest {
		t.Fatal("fresh current basis did not invalidate old approval")
	}
	beforeCalls := compositionCallCount(fake, procPromote)
	if err := runSnapshotPromote(ctx, s.Environment, r, ledger, options); err == nil {
		t.Fatal("stale pre-append approval silently accepted on recovery")
	}
	if compositionCallCount(fake, procPromote) != beforeCalls {
		t.Fatal("stale approval wrote a promotion")
	}
	interrupted = false
	options.Approval.Digest = plan.Digest
	if err := runSnapshotPromote(ctx, s.Environment, r, ledger, options); err != nil {
		t.Fatalf("original expected-current failed recovery: %v", err)
	}
	if promotions != 1 || applies != 2 || len(*statuses) != before+2 || (*statuses)[before+1] != "passed" {
		t.Fatalf("recovery did not reuse one promotion and record new successful gate: promotions=%d applies=%d gates=%v", promotions, applies, *statuses)
	}
	// A subsequent successful retry is still idempotent and uses the same snapshot.
	if err := runSnapshotPromote(ctx, s.Environment, r, ledger, options); err != nil {
		t.Fatal(err)
	}
	if promotions != 1 || applies != 3 {
		t.Fatal("successful retry created another promotion")
	}
}
