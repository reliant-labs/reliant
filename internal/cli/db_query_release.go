package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/reliant-labs/forge/internal/releasetimeline"
)

// liveRelease is the release that was live in an env at a moment (a query, a
// trace): what lets an agent line rows and telemetry up with the change that
// shipped (`forge env releases at`).
type liveRelease struct {
	Version     string    `json:"version"`
	PromotionID string    `json:"promotion_id,omitempty"`
	PromotedAt  time.Time `json:"promoted_at"`
	// Unsettled: the promotion's rollout had not finished, so the env may
	// still have been serving the previous release.
	Unsettled bool `json:"unsettled,omitempty"`
}

// liveReleaseTimeout bounds the release lookup. It is context for the
// rows, never a reason to withhold them.
const liveReleaseTimeout = 10 * time.Second

// liveReleaseAt reads env's release ledger (the machine ledger or the
// control plane, as `forge env releases` does) and returns the release live at
// at. A nil release with a nil error means nothing was promoted yet (a dev
// env that only ever ran `env up`). A variable so a test can substitute it.
var liveReleaseAt = func(ctx context.Context, env string, at time.Time) (*liveRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, liveReleaseTimeout)
	defer cancel()
	b, err := openReleasesBackend(ctx, env)
	if err != nil {
		return nil, err
	}
	d, err := b.Source.Load(ctx, env, time.Time{})
	if err != nil {
		return nil, err
	}
	return releaseFromAt(releasetimeline.Assemble(d, time.Time{}, 0).At(at)), nil
}

// releaseFromAt projects the live promotion of an At answer.
func releaseFromAt(r releasetimeline.AtResult) *liveRelease {
	if r.Live == nil {
		return nil
	}
	return &liveRelease{Version: r.Live.Version, PromotionID: r.Live.PromotionID, PromotedAt: r.Live.At.UTC(), Unsettled: r.Unsettled}
}

// stampRelease looks the live release up with lookup, warning on errOut
// (never failing the verb) when the ledger cannot be read.
func stampRelease(ctx context.Context, lookup func(context.Context, string, time.Time) (*liveRelease, error), errOut io.Writer, env string, at time.Time) *liveRelease {
	rel, err := lookup(ctx, env, at)
	if err != nil {
		fmt.Fprintf(errOut, "warning: the release live in %s is unknown: %v\n", env, err)
		return nil
	}
	return rel
}
