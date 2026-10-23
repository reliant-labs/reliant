package cli

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/release"
)

func prepareComposition(ctx context.Context, opts *buildOptions, base string, bootstrap bool, selectors []string) error {
	if opts.release == "" {
		return fmt.Errorf("snapshot composition requires --release")
	}
	if bootstrap && base != "" {
		return fmt.Errorf("--snapshot-bootstrap and --base-promotion are mutually exclusive")
	}
	if !bootstrap && base == "" {
		return fmt.Errorf("snapshot composition requires --base-promotion or --snapshot-bootstrap")
	}
	lookup, cancel := boundCompositionLookup(ctx)
	defer cancel()
	e, err := RenderKCL(lookup, projectDirForKCL(), opts.env)
	if err != nil {
		return err
	}
	ledger, err := ledgerFor(lookup, projectDirForKCL(), opts.env)
	if err != nil {
		return err
	}
	// The file ledger lacks transactional immutable seals and frozen snapshot
	// promotion today. Refuse rather than silently falling back to legacy rows.
	store, ok := ledger.Releases.(*hostedStore)
	if !ok {
		return fmt.Errorf("snapshot composition requires a ledger with immutable seals and frozen promotion support; file-ledger adapter is not yet available")
	}
	if err := requireSnapshotCapabilities(lookup, store.client); err != nil {
		return err
	}
	current, err := captureCompositionBase(lookup, ledger, opts.env, base, bootstrap)
	if err != nil {
		return err
	}
	consumers, err := compositionConsumers(e)
	if err != nil {
		return err
	}
	selected, err := compositionSelection(consumers, selectors)
	if err != nil {
		return err
	}
	if bootstrap && len(selected) != len(consumers) {
		return fmt.Errorf("snapshot bootstrap must select every consumer")
	}
	run := &compositionRun{Base: current, Ledger: ledger, Selected: selected, Entities: e}
	if !bootstrap {
		if current.Snapshot.Project != store.project {
			return fmt.Errorf("captured base belongs to another project")
		}
		recorder, err := bundleRecorderFor(lookup, projectDirForKCL(), opts.env, ledger)
		if err != nil {
			return err
		}
		record, err := recorder.Bundle(lookup, current.BundleID)
		if err != nil {
			return err
		}
		if record == nil || record.Digest != current.BundleDigest || record.Reference == "" {
			return fmt.Errorf("captured base bundle is missing")
		}
		repository, err := snapshotBundleRepository(lookup, projectDirForKCL(), opts.env, record.Reference)
		if err != nil {
			return err
		}
		fetched, err := verifyCompositionBase(lookup, repository, *current)
		if err != nil {
			return err
		}
		run.BaseSnapshot = fetched.Doc.Snapshot
	}
	provenance, err := release.CaptureProvenance(lookup, projectDirForKCL(), release.CaptureOptions{})
	if err != nil {
		return err
	}
	if provenance.Dirty || provenance.Repo == "" || !release.ValidObjectID(provenance.Commit) {
		return fmt.Errorf("snapshot composition requires a clean resolved source checkout")
	}
	// Conservative policy identity is a versioned fixed policy, not a claimed
	// compatibility proof. All partial dependency validation remains hard-fail.
	run.Declaration = release.Snapshot{Schema: release.SnapshotSchema, Project: store.project, Environment: opts.env, Source: release.Source{Repo: provenance.Repo, Commit: provenance.Commit}, PolicyDigest: release.SpecDigest([]byte("forge.conservative-composition-policy/v1")), Consumers: consumers, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	if bootstrap && current != nil {
		run.Declaration.BootstrapFromPromotionID = current.ID
	}
	if run.BaseSnapshot != nil {
		run.Declaration.Base = &release.SnapshotBase{PromotionID: current.ID, SnapshotDigest: current.SnapshotDigest, BundleDigest: current.BundleDigest}
	}
	if len(selected) != len(consumers) {
		set := map[release.ConsumerKey]bool{}
		for _, k := range selected {
			set[k] = true
		}
		for _, c := range consumers {
			for _, d := range c.DependsOn {
				if set[c.Key] != set[d.Consumer] {
					return fmt.Errorf("compatibility cohort requires explicitly selecting both %s and %s (%s); no trusted pair evidence supplied", c.Key.Workload, d.Consumer.Workload, d.Contract)
				}
			}
		}
		for _, c := range consumers {
			if !c.DependenciesDeclared {
				return fmt.Errorf("consumer %q has unknown release dependencies; declare its complete cohort or select all", c.Key.Workload)
			}
		}
	}
	if _, err := compositionBuildUnits(e); err != nil {
		return err
	}
	if len(selectors) > 0 && len(selected) == 0 {
		return fmt.Errorf("empty composition selection")
	}
	opts.composition = run
	opts.targets = nil
	for _, k := range selected {
		opts.targets = append(opts.targets, k.Workload)
	}
	opts.buildTarget = "all"
	return nil
}
