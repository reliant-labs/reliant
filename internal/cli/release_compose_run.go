package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/releasecompose"
	"github.com/reliant-labs/forge/pkg/release"
)

type compositionRun struct {
	Base         *release.Promotion
	BaseSnapshot *release.Snapshot
	Declaration  release.Snapshot
	Ledger       envLedger
	Selected     []release.ConsumerKey
	Entities     *KCLEntities
	PlanDocument []byte
}

type compositionSnapshotKey struct{}

func compositionSnapshotFrom(ctx context.Context) *release.Snapshot {
	s, _ := ctx.Value(compositionSnapshotKey{}).(*release.Snapshot)
	return s
}
func withCompositionSnapshot(ctx context.Context, s release.Snapshot) (context.Context, error) {
	ctx, err := kclrender.WithSnapshot(ctx, s)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, compositionSnapshotKey{}, &s), nil
}

func runCompositionBuildPlan(ctx context.Context, cfg *config.ProjectConfig, entities *KCLEntities, opts buildOptions, tag, projectTag string) error {
	run := opts.composition
	var ref *release.SnapshotBase
	if run.BaseSnapshot != nil {
		ref = &release.SnapshotBase{PromotionID: run.Base.ID, SnapshotDigest: run.Base.SnapshotDigest, BundleDigest: run.Base.BundleDigest}
	}
	names := []string{}
	for _, k := range run.Selected {
		names = append(names, k.Workload)
	}
	p, err := planCompositionBuild(ctx, cfg, entities, run.Declaration.Consumers, names, ref, opts, planInputs{projectDir: projectDirForKCL(), resolvedTag: tag, projectTag: projectTag, goPackageName: goListPackageName})
	if err != nil {
		return err
	}
	if bad := p.Build.problems(); len(bad) > 0 {
		return fmt.Errorf("composition preflight has %d failed steps", len(bad))
	}
	if opts.planJSON {
		decisions := []release.CompositionDecision{}
		set := map[release.ConsumerKey]bool{}
		for _, k := range run.Selected {
			set[k] = true
		}
		for _, c := range run.Declaration.Consumers {
			action, reason := "carry", "retain captured base bytes pending final compatibility validation"
			if set[c.Key] {
				action, reason = "build", "explicit selection; output digest pending"
			}
			decisions = append(decisions, release.CompositionDecision{Consumer: c.Key, Action: action, Reason: reason})
		}
		out := struct {
			Schema             string                        `json:"schema"`
			CurrentPromotionID string                        `json:"current_promotion_id"`
			Base               *release.SnapshotBase         `json:"base"`
			Selected           []release.ConsumerKey         `json:"selected"`
			Decisions          []release.CompositionDecision `json:"decisions"`
			Pending            bool                          `json:"pending"`
			Approvable         bool                          `json:"approvable"`
		}{"forge.dev/composition-preflight/v1", basePromotionID(run.Base), ref, run.Selected, decisions, true, false}
		data, err := json.Marshal(out)
		run.PlanDocument = data
		return err
	}
	printBuildPlan(p.Build, opts)
	if bad := p.Build.problems(); len(bad) > 0 {
		return fmt.Errorf("composition preflight has %d failed steps", len(bad))
	}
	fmt.Printf("[composition] base: %s; %d consumers selected; output digests pending. NOT an approvable promotion plan. Nothing built or recorded.\n", basePromotionID(run.Base), len(run.Selected))
	return nil
}
func basePromotionID(p *release.Promotion) string {
	if p == nil {
		return "unbound"
	}
	return p.ID
}

// finishComposition uses only this invocation's successful outputs plus the
// captured verified base. It never lists or globs build state.
func finishComposition(ctx context.Context, cfg *config.ProjectConfig, entities *KCLEntities, opts buildOptions, results []buildResult) error {
	run := opts.composition
	candidate := run.Declaration
	if run.BaseSnapshot != nil {
		candidate.Base = &release.SnapshotBase{PromotionID: run.Base.ID, SnapshotDigest: run.Base.SnapshotDigest, BundleDigest: run.Base.BundleDigest}
	}
	candidate.Selected = run.Selected
	candidate.Closure = run.Selected
	if err := populateCompositionBindings(ctx, &candidate, run, entities, opts, results); err != nil {
		return err
	}
	return sealComposition(ctx, cfg, entities, opts, candidate)
}

func populateCompositionBindings(ctx context.Context, candidate *release.Snapshot, run *compositionRun, entities *KCLEntities, opts buildOptions, results []buildResult) error {
	selected := map[release.ConsumerKey]bool{}
	for _, k := range run.Selected {
		selected[k] = true
	}
	for _, c := range candidate.Consumers {
		action := "carry"
		if selected[c.Key] {
			action = "build"
		}
		candidate.Decisions = append(candidate.Decisions, release.CompositionDecision{Consumer: c.Key, Action: action, Reason: "explicit native selection"})
		for _, slot := range c.Slots {
			if !selected[c.Key] {
				if err := carryCompositionBinding(candidate, run.BaseSnapshot, c.Key, slot); err != nil {
					return err
				}
				continue
			}
			if err := addSelectedCompositionBinding(ctx, candidate, run, entities, opts, results, c.Key, slot); err != nil {
				return err
			}
		}
	}
	return nil
}

func addSelectedCompositionBinding(ctx context.Context, candidate *release.Snapshot, run *compositionRun, entities *KCLEntities, opts buildOptions, results []buildResult, key release.ConsumerKey, slot string) error {
	c := release.Consumer{Key: key}
	var ref string
	var receipt *release.BuildReceipt
	var source *release.Source
	var requiredPlatform string
	for _, w := range runWorkloads(run, entities) {
		if w.Name != c.Key.Workload {
			continue
		}
		if w.Runtime.Type == RuntimeHosted {
			requiredPlatform = "linux/" + w.Runtime.HostedPlatform()
		} else if w.Runtime.Cluster != nil && w.Runtime.Cluster.Platform != "" {
			requiredPlatform = "linux/" + w.Runtime.Cluster.Platform
		}
		if slot == "build-only" || slot == "container:"+w.Name {
			var err error
			ref, receipt, source, err = compositionPrimaryOutput(ctx, w, slot, opts, results, candidate.Source)
			if err != nil {
				return err
			}
		}
		for _, sidecar := range w.Spec.Sidecars {
			if slot == "container:"+sidecar.Name {
				ref = sidecar.Image
			}
		}
		if strings.HasPrefix(slot, "input:") {
			ref = w.ReleaseInputs[strings.TrimPrefix(slot, "input:")]
		}
	}
	if slot == "frontend" {
		var artifact release.SnapshotArtifact
		found := false
		for _, fe := range entities.Frontends {
			if fe.Name != c.Key.Workload {
				continue
			}
			s := candidate.Source
			if fe.Source != nil {
				if fe.SourceOverridden || !release.ValidObjectID(fe.SourceCommit) {
					return fmt.Errorf("frontend %s source is unresolved or overridden", fe.Name)
				}
				s = release.Source{Repo: fe.Source.Repo, Ref: fe.Source.Ref, Subdir: fe.Source.Subdir, Commit: fe.SourceCommit}
			}
			artifact = release.SnapshotArtifact{Kind: release.KindGit, Location: s.Repo, Content: s.Commit, Origin: release.ArtifactOrigin{Release: opts.release, Source: &s}}
			found = true
		}
		if !found {
			return fmt.Errorf("selected frontend %s has no resolved source output", c.Key.Workload)
		}
		id, err := release.ArtifactIdentity(artifact)
		if err != nil {
			return err
		}
		candidate.Artifacts[id] = artifact
		candidate.Bindings = append(candidate.Bindings, release.ConsumerBinding{Consumer: c.Key, Slot: slot, Artifact: id})
		return nil
	}
	for _, m := range run.Entities.ReleaseManifestConsumers {
		if m.Name == c.Key.Workload && m.Cluster == c.Key.Cluster {
			ref = m.ReleaseInputs[slot]
		}
	}
	if ref == "" {
		return fmt.Errorf("selected slot %v/%s has no build output", c.Key, slot)
	}
	artifact, err := resolveCompositionArtifact(ctx, ref, opts.release, receipt, source)
	if err != nil {
		return err
	}
	if requiredPlatform != "" {
		found := false
		for _, platform := range artifact.Platforms {
			found = found || platform == requiredPlatform
		}
		if !found {
			return fmt.Errorf("artifact for %s/%s does not provide required platform %s", c.Key.Workload, slot, requiredPlatform)
		}
	}
	id, err := release.ArtifactIdentity(artifact)
	if err != nil {
		return err
	}
	recordCompositionBuildEvent(candidate, c.Key, id, artifact.Origin)
	candidate.Artifacts[id] = artifact
	candidate.Bindings = append(candidate.Bindings, release.ConsumerBinding{Consumer: c.Key, Slot: slot, Artifact: id})
	return nil
}

func sealComposition(ctx context.Context, cfg *config.ProjectConfig, entities *KCLEntities, opts buildOptions, candidate release.Snapshot) error {
	run := opts.composition
	pinned, err := withCompositionSnapshot(ctx, candidate)
	if err != nil {
		return err
	}
	doc, err := renderEnvShape(pinned, os.Stderr, projectDirForKCL(), opts.env)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if prior, err := run.Ledger.Releases.Get(ctx, opts.release); err != nil {
		return err
	} else if prior != nil {
		now = prior.CreatedAt
	}
	assets, err := bundleObservabilityFiles(projectDirForKCL(), bundleBuildInputs{Release: opts.release, OutputDir: opts.outputDir, StrictObservability: true})
	if err != nil {
		return err
	}
	if doc.Provenance.Commit != candidate.Source.Commit || doc.Provenance.Dirty {
		return fmt.Errorf("composition source changed during build")
	}
	input := bundle.BuildInput{Project: cfg.Name, Env: opts.env, Release: opts.release, Provenance: doc.Provenance, Shape: bundleShapeInputOf(doc), CreatedAt: now, Units: doc.units, Reconciled: doc.reconciled(), ReconciledUnits: doc.reconciledUnits(), Observability: assets}
	candidate, err = bundle.SnapshotInventory(pinned, input, candidate)
	if err != nil {
		return err
	}
	if err := captureSnapshotFrontends(ctx, &candidate, run.BaseSnapshot, compositionFrontendEntities(run, entities), projectDirForKCL(), opts.env); err != nil {
		return err
	}
	var ref *release.SnapshotBase
	if run.BaseSnapshot != nil {
		ref = &release.SnapshotBase{PromotionID: run.Base.ID, SnapshotDigest: run.Base.SnapshotDigest, BundleDigest: run.Base.BundleDigest}
	}
	composed, err := releasecompose.Compose(releasecompose.Input{Base: run.BaseSnapshot, Candidate: candidate, BaseRef: ref, Selected: run.Selected})
	if err != nil {
		return err
	}
	input.Snapshot = &composed
	built, err := bundle.Build(ctx, input)
	if err != nil {
		return err
	}
	digest, err := composed.Digest()
	if err != nil {
		return err
	}
	rel := release.Release{Version: opts.release, Project: cfg.Name, CreatedAt: now, Catalog: composed.Artifacts, SnapshotIntents: map[string]string{opts.env: digest}}
	rel.SetProvenance(doc.Provenance)
	if _, err := run.Ledger.Releases.Cut(ctx, rel); err != nil {
		return err
	}
	placed, err := placeBundleBytes(ctx, projectDirForKCL(), opts.env, built, run.Ledger, bundleBuildInputs{Release: opts.release, Pushed: true, Now: now})
	if err != nil {
		return err
	}
	if run.Ledger.Hosted && placed.repository == "" {
		return fmt.Errorf("snapshot bundle was not pushed; no seal recorded")
	}
	recorder, err := bundleRecorderFor(ctx, projectDirForKCL(), opts.env, run.Ledger)
	if err != nil {
		return err
	}
	layer, _ := built.Layer(release.BundleManifestsLayer)
	record := release.BundleRecord{Env: opts.env, Release: opts.release, Digest: built.Digest, Reference: placed.Reference, ConfigDigest: built.Doc.ConfigDigest, Shape: built.Doc.Shape, Provenance: doc.Provenance, CreatedAt: now, Snapshot: &composed, SnapshotDigest: digest}
	if _, _, err := recorder.RecordBundle(ctx, record, bundleBlobs{Repository: placed.repository, Manifest: built.Manifest, Config: built.ConfigBlob(), ManifestsLayer: layer}); err != nil {
		return err
	}
	fmt.Printf("[composition] sealed %s snapshot %s; captured base %s. Promotion requires that exact expected-current basis.\n", opts.release, digest, basePromotionID(run.Base))
	return nil
}
func recordCompositionBuildEvent(s *release.Snapshot, c release.ConsumerKey, id release.ArtifactID, origin release.ArtifactOrigin) {
	s.BuildEvents = append(s.BuildEvents, release.ArtifactBuildEvent{Consumer: c, Artifact: id, Origin: origin})
}

func compositionFrontendEntities(run *compositionRun, built *KCLEntities) *KCLEntities {
	out := *run.Entities
	out.Frontends = append([]FrontendEntity(nil), out.Frontends...)
	for i, f := range out.Frontends {
		for _, resolved := range built.Frontends {
			if f.Name == resolved.Name {
				out.Frontends[i] = resolved
			}
		}
	}
	return &out
}
func runWorkloads(run *compositionRun, e *KCLEntities) []WorkloadEntity {
	return run.Entities.Workloads
}
