package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

var snapshotClusterApply = cluster.ApplySnapshot
var snapshotResolveGateBackend = resolveGateBackend
var snapshotBundleRepository = func(ctx context.Context, root, env, reference string) (bundle.Resolver, error) {
	return openBundleRepository(ctx, root, env, reference)
}

func emptyUnbound(id string) string {
	if id == expectUnboundLiteral {
		return ""
	}
	return id
}

func dispatchSnapshotPromotion(ctx context.Context, env, version string, ledger envLedger, opts promoteOptions) (bool, error) {
	candidate, err := ledger.Releases.Get(ctx, version)
	if err != nil {
		return false, err
	}
	if candidate == nil || len(candidate.SnapshotIntents) == 0 {
		return false, nil
	}
	return true, runSnapshotPromote(ctx, env, *candidate, ledger, opts)
}

// runSnapshotPromote never calls legacy release digest flattening or rerenders
// source. The sealed bundle and its exact base are the only desired decision.
func runSnapshotPromote(ctx context.Context, env string, r release.Release, ledger envLedger, opts promoteOptions) error {
	store, ok := ledger.Releases.(*hostedStore)
	if !ok {
		return fmt.Errorf("snapshot promotion requires immutable seal-capable ledger")
	}
	if err := requireSnapshotCapabilities(ctx, store.client); err != nil {
		return err
	}
	resolver, ok := ledger.Bindings.(*hostedStore)
	if !ok {
		return fmt.Errorf("snapshot binding store missing")
	}
	envID, err := resolver.envID(ctx, env)
	if err != nil {
		return err
	}
	// Resolve by sealed snapshot intent, not newest arbitrary record.
	var response struct {
		Bundles []wireBundle `json:"bundles"`
	}
	if err := store.client.Call(ctx, "controlplane.v1.DeployService/ListBundles", map[string]any{"environmentId": envID, "limit": 500}, &response); err != nil {
		return err
	}
	var record *release.BundleRecord
	for _, w := range response.Bundles {
		if w.ReleaseVersion != r.Version || w.SnapshotDigest != r.SnapshotIntents[env] {
			continue
		}
		b, err := bundleFromWire(env, w)
		if err != nil {
			return err
		}
		if record != nil && record.Digest != b.Digest {
			return fmt.Errorf("ambiguous snapshot seal")
		}
		record = &b
	}
	if record == nil || record.Snapshot == nil {
		return fmt.Errorf("release %s has no sealed snapshot for %s", r.Version, env)
	}
	s := record.Snapshot
	if s.Project != r.Project || s.Environment != env {
		return fmt.Errorf("sealed snapshot project/environment mismatch")
	}
	current, bound, err := ledger.Bindings.Current(ctx, env)
	if err != nil {
		return err
	}
	expected := expectUnboundLiteral
	if bound {
		expected = current.ID
	}
	alreadyDesired := bound && current.Release == r.Version && current.SnapshotDigest == record.SnapshotDigest && current.BundleDigest == record.Digest
	if s.Base != nil {
		expected = s.Base.PromotionID
		if !alreadyDesired && (!bound || current.ID != expected) {
			return &promoteRefusedError{Reason: reasonPlanStale, Detail: "captured base promotion moved; refusing automatic rebase"}
		}
	}
	if s.BootstrapFromPromotionID != "" {
		expected = s.BootstrapFromPromotionID
		if !alreadyDesired && (!bound || current.ID != expected) {
			return &promoteRefusedError{Reason: reasonPlanStale, Detail: "legacy bootstrap basis moved"}
		}
	}
	if opts.ExpectCurrent != "" && opts.ExpectCurrent != expected {
		return fmt.Errorf("expected-current does not match snapshot base")
	}
	if s.Base == nil && opts.ExpectCurrent == "" {
		return fmt.Errorf("full snapshot bootstrap requires explicit --expect-current or --expect-unbound")
	}
	repository, err := snapshotBundleRepository(ctx, projectDirForKCL(), env, record.Reference)
	if err != nil {
		return err
	}
	frozen := release.Promotion{BundleDigest: record.Digest, SnapshotDigest: record.SnapshotDigest}
	fetched, err := verifyCompositionBase(ctx, repository, frozen)
	if err != nil {
		return err
	}
	if fetched.Doc.Release != r.Version {
		return fmt.Errorf("sealed bundle release mismatch")
	}
	plan, err := (hostedPlanClient{client: store.client}).PlanDeploy(ctx, envID, record.ID, r.Version)
	if err != nil {
		return err
	}
	if plan.BundleID != record.ID || plan.ReleaseVersion != r.Version || plan.LiveBasis.CurrentPromotionID != func() string {
		if alreadyDesired {
			return current.ID
		}
		return emptyUnbound(expected)
	}() {
		return fmt.Errorf("server plan does not name exact sealed snapshot and captured base")
	}
	if err := gateDeployOnPlan(env, &plan, opts.Approval, opts.Confirm, opts.JSON); err != nil {
		return err
	}
	// Full binding/spec information is shown separately from legacy repo shape.
	for _, d := range s.Decisions {
		fmt.Fprintf(os.Stderr, "[snapshot] %s %s: %s\n", d.Consumer.Workload, d.Action, d.Reason)
	}
	for _, b := range s.Bindings {
		a := s.Artifacts[b.Artifact]
		fmt.Fprintf(os.Stderr, "[snapshot] %s/%s = %s@%s\n", b.Consumer.Workload, b.Slot, a.Location, a.Content)
	}
	if opts.DryRun {
		return nil
	}
	return executeSnapshotPromotion(ctx, r, ledger, opts, plan, expected, record, s)
}

func executeSnapshotPromotion(ctx context.Context, r release.Release, ledger envLedger, opts promoteOptions, plan release.Plan, expected string, record *release.BundleRecord, s *release.Snapshot) (retErr error) {
	env := s.Environment
	if opts.Follow != nil && ledger.Hosted && len(ledger.HostedTiers) > 0 && !ledger.hubUnits() {
		return fmt.Errorf("snapshot hosted client publication adapter not available; no promotion written")
	}
	if opts.Follow != nil && opts.Follow.NoWait && !ledger.hubUnits() {
		return fmt.Errorf("snapshot client apply requires rollout verification; no promotion written")
	}
	if opts.Follow != nil && !ledger.hubUnits() {
		if opts.Follow.clientDeploy.rollout.Normalize().Mode != cluster.RolloutWait {
			return fmt.Errorf("snapshot apply requires strict rollout verification; skip/warn cannot establish an applied baseline")
		}
		_, trees, err := release.SnapshotDelivery(*s)
		if err != nil {
			return err
		}
		byPath := make(map[string]release.BundleTree, len(trees))
		for _, tree := range trees {
			byPath[tree.Path] = tree
		}
		for _, tree := range trees {
			for _, dependency := range tree.DependsOn {
				parent, ok := byPath[dependency]
				if !ok || parent.Cluster != tree.Cluster || parent.Unit != tree.Unit {
					return fmt.Errorf("snapshot client apply does not yet execute cross-unit delivery graphs; use hub convergence")
				}
			}
		}
	}
	if err := snapshotPublishFrontends(ctx, *s, true); err != nil {
		return err
	}
	if opts.Confirm != nil {
		confirmation := promotePlan{Target: promotePlanTarget{Release: r.Version}, DeployPlan: &plan}
		outcome := confirmDeployPlan(env, confirmation, *opts.Confirm, opts.JSON)
		if outcome.Err != nil {
			return outcome.Err
		}
		if !outcome.Confirmed {
			return nil
		}
	}
	guard := appendGuard{ExpectedCurrentID: expected, SupersedeInFlight: opts.Supersede}
	if expected == expectUnboundLiteral {
		guard.ExpectedCurrentID = ""
		guard.ExpectUnbound = true
	}
	promotion := release.Promotion{Env: env, Release: r.Version, Kind: release.KindPromote, PlanDigest: plan.Digest, Note: opts.Note, AcknowledgedFindings: opts.Approval.AcknowledgedFindings}
	var err error
	promotion.Gates, err = resolvePromoteGates(opts.Gates)
	if err != nil {
		return err
	}
	promotion.Run, err = opts.Run.resolveRun()
	if err != nil {
		return err
	}
	written, err := ledger.Bindings.Append(ctx, promotion, guard)
	if err != nil {
		return err
	}
	if written.SnapshotDigest != record.SnapshotDigest || written.BundleDigest != record.Digest {
		return fmt.Errorf("server promotion did not freeze the approved seal")
	}
	if opts.Follow == nil {
		return nil
	}
	started := time.Now()
	if !ledger.hubUnits() {
		defer func() {
			gate := applyOutcomeGate(started, time.Now(), retErr, record.Digest, plan.Digest)
			backend, err := snapshotResolveGateBackend(ctx, env)
			if err == nil && backend.store == nil {
				err = fmt.Errorf("no gate store for %s", env)
			}
			if err == nil && backend.store != nil {
				_, _, err = backend.store.recordGate(ctx, written.ID, gate)
			}
			if err != nil && retErr == nil {
				retErr = fmt.Errorf("snapshot applied but applied-baseline gate could not be recorded: %w", err)
			}
		}()
	}
	if ledger.hubUnits() {
		return runPromoteWait(ctx, env, envWaitOptions{PromotionID: written.ID, Timeout: opts.Follow.Timeout, FailFast: opts.Follow.FailFast})
	}
	if ledger.Hosted && len(ledger.HostedTiers) > 0 {
		return fmt.Errorf("snapshot hosted client publication adapter not available; desired promotion recorded but not reported applied")
	}
	clusters := map[string]string{}
	for _, resource := range s.Resources {
		if old := clusters[resource.Key.Cluster]; old == "" && resource.Key.Namespace != "" {
			clusters[resource.Key.Cluster] = resource.Key.Namespace
		} else if _, ok := clusters[resource.Key.Cluster]; !ok {
			clusters[resource.Key.Cluster] = ""
		}
	}
	names := make([]string, 0, len(clusters))
	for name := range clusters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasPrefix(name, "_") {
			return fmt.Errorf("unsupported client snapshot destination %s", name)
		}
		policy := opts.Follow.clientDeploy.rollout
		if opts.Follow.NoWait {
			return fmt.Errorf("snapshot client apply requires rollout verification; desired state remains recorded")
		}
		if err := snapshotClusterApply(ctx, *s, name, cluster.ApplyOpts{Context: name, Namespace: clusters[name], Env: env, Rollout: policy}); err != nil {
			return err
		}
	}
	return snapshotPublishFrontends(ctx, *s, false)
}
