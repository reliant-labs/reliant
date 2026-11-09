package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// checkReleaseVersionFree refuses a release-bound build whose version is
// already CUT in the env's ledger from another source, before anything is
// built or pushed.
//
// Releases are immutable, and a version's identity is its artifact set. A
// second cut of the same version from a different commit builds and pushes
// images under that version's tag for twenty minutes and is then refused at
// the record (release.ErrReleaseConflict). A version is "taken" the moment it
// is cut, not when it is first promoted: control-plane cut v1.7.57 twice on
// 2026-10-25, from d4f71ded and b7059e22, because the only check before the
// second cut asked the promotion timeline, where an unpromoted release does
// not appear.
//
// The same source is NOT refused. A retry of a cut that failed after its
// record, or a re-run of the pipeline at the same clean commit, records an
// identical release or fails with the conflict as it always did: Cut owns
// that decision, and a retry must stay possible.
func checkReleaseVersionFree(ctx context.Context, ledger envLedger, env, version string, prov release.Provenance) error {
	if version == "" || ledger.Releases == nil {
		return nil
	}
	cut, err := ledger.Releases.Get(ctx, version)
	if err != nil || cut == nil {
		// The cut reads the ledger again and owns an unreadable one.
		return nil //nolint:nilerr // see above
	}
	cutCommit := cut.Git.Commit
	if cut.Provenance != nil && cut.Provenance.Commit != "" {
		cutCommit = cut.Provenance.Commit
	}
	if cutCommit != "" && cutCommit == prov.Commit && !prov.Dirty {
		return nil
	}
	from := "an unrecorded source"
	if cutCommit != "" {
		from = "commit " + shortCommit(cutCommit)
	}
	when := ""
	if !cut.CreatedAt.IsZero() {
		when = " at " + cut.CreatedAt.UTC().Format(time.RFC3339)
	}
	this := "this tree"
	if prov.Commit != "" {
		this = "commit " + shortCommit(prov.Commit)
		if prov.Dirty {
			this += " (dirty)"
		}
	}
	return fmt.Errorf("--release %s: %w: %s is already cut in %s, from %s%s, and this build is from %s.\n"+
		"  Releases are immutable, so this cut would build and push under %s and then be refused at the record.\n"+
		"  Pick the next version, or ship the existing one: forge env deploy %s %s",
		version, release.ErrReleaseConflict, version, ledger.Releases.Location(), from, when, this, version, env, version)
}

// preflightReleaseVersion is checkReleaseVersionFree for a release-bound
// build, over the env's ledger. A ledger that cannot be resolved here is left
// to the cut, which needs it anyway and fails loudly.
func preflightReleaseVersion(ctx context.Context, projectDir string, entities *KCLEntities, opts buildOptions) error {
	if opts.release == "" || opts.env == "" {
		return nil
	}
	ledger, err := ledgerForRendered(ctx, projectDir, opts.env, entities)
	if err != nil {
		return nil //nolint:nilerr // the cut resolves the ledger again and owns that failure
	}
	return checkReleaseVersionFree(ctx, ledger, opts.env, opts.release, captureReleaseProvenance(ctx, projectDir))
}
