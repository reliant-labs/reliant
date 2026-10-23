package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// captureCompositionBase is called exactly once, before build dispatch. No
// automatic rebase/retry is permitted after this immutable basis is captured.
func captureCompositionBase(ctx context.Context, ledger envLedger, env, requested string, bootstrap bool) (*release.Promotion, error) {
	current, bound, err := ledger.Bindings.Current(ctx, env)
	if err != nil {
		return nil, err
	}
	if bootstrap {
		// Full bootstrap may replace legacy desired state, but promotion must still
		// compare its captured ID; genuinely unbound bootstrap uses expect-unbound.
		if !bound {
			return nil, nil
		}
		return &current, nil
	}
	if !bound {
		return nil, fmt.Errorf("partial composition has no base: environment is unbound; use a complete snapshot bootstrap")
	}
	if requested != "current" && requested != current.ID {
		return nil, &promoteRefusedError{Reason: reasonPlanStale, Detail: fmt.Sprintf("base promotion moved from %s to %s", requested, current.ID)}
	}
	if current.Snapshot == nil || current.SnapshotDigest == "" || current.BundleID == "" || !release.ValidDigest(current.BundleDigest) {
		return nil, fmt.Errorf("promotion %s has no sealed complete snapshot; create a full bootstrap snapshot", current.ID)
	}
	if current.Snapshot.Environment != env {
		return nil, fmt.Errorf("base snapshot environment mismatch")
	}
	d, err := current.Snapshot.Digest()
	if err != nil || d != current.SnapshotDigest {
		return nil, fmt.Errorf("invalid captured base snapshot")
	}
	return &current, nil
}

// verifyCompositionBase fetches the exact recorded archive, never today's render
// or a mutable version tag. The shared bundle reader validates descriptors and
// the snapshot-layer match.
func verifyCompositionBase(ctx context.Context, resolver bundle.Resolver, p release.Promotion) (bundle.Fetched, error) {
	fetched, err := bundle.Fetch(ctx, resolver, p.BundleDigest)
	if err != nil {
		return bundle.Fetched{}, err
	}
	if fetched.Digest != p.BundleDigest || fetched.Doc.Snapshot == nil || fetched.Doc.SnapshotDigest != p.SnapshotDigest {
		return bundle.Fetched{}, fmt.Errorf("captured promotion bundle does not match snapshot seal")
	}
	if err := release.ValidateSnapshotLayer(*fetched.Doc.Snapshot, fetched.Manifests); err != nil {
		return bundle.Fetched{}, err
	}
	return fetched, nil
}

// boundCompositionLookup keeps read-only remote lookups bounded even when a
// caller did not provide a deadline. It is not a retry or a rebase policy.
func boundCompositionLookup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 30*time.Second)
}
