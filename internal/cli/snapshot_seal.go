package cli

import (
	"context"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/releasecompose"
	"github.com/reliant-labs/forge/pkg/release"
)

// compositionPolicyDigest is the versioned conservative compatibility policy
// every forge-built snapshot names. It is a fixed policy identity, not a
// claimed compatibility proof. A composition and an env deploy's evidence
// snapshot name the same one, so an evidence promotion is a base a later
// partial composition can build on ("policy change requires full selection").
func compositionPolicyDigest() string {
	return release.SpecDigest([]byte("forge.conservative-composition-policy/v1"))
}

// fullSnapshotInput is what buildFullSnapshot seals.
type fullSnapshotInput struct {
	// Candidate is the declaration with a binding for every selected
	// consumer's slots, their artifacts and one decision per consumer. Its
	// Selected names the consumers whose frontends are frozen from this
	// render (captureSnapshotFrontends).
	Candidate release.Snapshot
	// Bundle is the env's bundle input, from the same render pass the
	// candidate's bindings describe. Its Snapshot is ignored.
	Bundle bundle.BuildInput
	// Base and BaseRef are the captured base of a partial composition; both
	// nil for a snapshot that describes the whole environment on its own.
	Base    *release.Snapshot
	BaseRef *release.SnapshotBase
	// Selected is the consumers this snapshot takes from the candidate; with
	// no base it is every consumer.
	Selected []release.ConsumerKey
	// Frontends is the render whose frontend declarations are frozen.
	Frontends  *KCLEntities
	ProjectDir string
	Env        string
}

// buildFullSnapshot is THE ONE full-snapshot builder: the rendered resource
// inventory (bundle.SnapshotInventory), the frozen frontend declarations
// (captureSnapshotFrontends) and the composition over the base
// (releasecompose.Compose), then the v2 bundle that seals the result.
//
// `forge release compose` (sealComposition) and an env deploy's evidence
// snapshot (sealEvidenceSnapshot) both build through it, so the snapshot a
// promotion freezes has one shape whichever command recorded it. It records
// nothing: cutting, placing and recording stay with each caller, whose rules
// differ (an intent seal versus an evidence seal).
func buildFullSnapshot(ctx context.Context, in fullSnapshotInput) (release.Snapshot, bundle.Bundle, error) {
	candidate, err := bundle.SnapshotInventory(ctx, in.Bundle, in.Candidate)
	if err != nil {
		return release.Snapshot{}, bundle.Bundle{}, err
	}
	if err := captureSnapshotFrontends(ctx, &candidate, in.Base, in.Frontends, in.ProjectDir, in.Env); err != nil {
		return release.Snapshot{}, bundle.Bundle{}, err
	}
	composed, err := releasecompose.Compose(releasecompose.Input{Base: in.Base, Candidate: candidate, BaseRef: in.BaseRef, Selected: in.Selected})
	if err != nil {
		return release.Snapshot{}, bundle.Bundle{}, err
	}
	input := in.Bundle
	input.Snapshot = &composed
	built, err := bundle.Build(ctx, input)
	if err != nil {
		return release.Snapshot{}, bundle.Bundle{}, err
	}
	return composed, built, nil
}
