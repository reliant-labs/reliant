package releasetimeline

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// maxPromotions is how many promotions a remote source reads before it stops
// paging and says the history is truncated. A variable so a test can shrink it
// instead of serving hundreds of rows.
var maxPromotions = 500

// LedgerReader is the part of the machine ledger the timeline reads. It is
// declared here, at its consumer; *ledgerfile.Store satisfies it.
type LedgerReader interface {
	// Promotions are oldest first.
	Promotions(env string) ([]release.Promotion, error)
	Releases() ([]release.Release, error)
	Applies(env string) ([]ledgerfile.ApplyRecord, error)
	// Gates are the post-promote evidence recorded against one promotion.
	Gates(env, promotionID string) ([]release.Gate, error)
}

// Local reads the machine ledger (the readers `forge ledger show` uses).
type Local struct {
	Ledger LedgerReader
}

// Load reads env's whole local history. A file ledger writes post-promote
// gates to a side file, so they are joined to their promotion here.
func (l Local) Load(_ context.Context, env string, _ time.Time) (Data, error) {
	promos, err := l.Ledger.Promotions(env)
	if err != nil {
		return Data{}, fmt.Errorf("read %s promotions: %w", env, err)
	}
	d := Data{Env: env, Promotions: promos}
	for i := range d.Promotions {
		p := &d.Promotions[i]
		if p.ID == "" {
			continue
		}
		gates, err := l.Ledger.Gates(env, p.ID)
		if err != nil {
			return Data{}, fmt.Errorf("read %s gates: %w", env, err)
		}
		p.RecordedGates = append(append([]release.Gate(nil), p.RecordedGates...), gates...)
	}
	// A release ledger that cannot be read costs commit ranges and
	// directions, not the timeline.
	if d.Releases, err = l.Ledger.Releases(); err != nil {
		d.Warnings = append(d.Warnings, fmt.Sprintf("releases unreadable, directions fall back to unknown: %v", err))
	}
	applies, err := l.Ledger.Applies(env)
	if err != nil {
		d.Warnings = append(d.Warnings, fmt.Sprintf("applies unreadable, failed deploys are not shown: %v", err))
	}
	for _, a := range applies {
		// An apply with no outcome is running or abandoned; neither is a
		// result, so neither is a row.
		if a.Outcome == nil {
			continue
		}
		status := StatusSucceeded
		switch a.Outcome.Status {
		case release.ApplyFailed:
			status = StatusFailed
		case release.ApplyTimedOut:
			status = StatusTimedOut
		}
		d.Attempts = append(d.Attempts, Attempt{
			ID: a.Apply.ID, PromotionID: a.Apply.PromotionID, Status: status,
			StartedAt: a.Apply.CreatedAt, FinishedAt: a.Outcome.FinishedAt,
			Reason: a.Outcome.Summary,
		})
	}
	return d, nil
}
