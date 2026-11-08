package obsbackend

// The request and result vocabulary of release health (`forge obs check`) and
// of the change feed (`forge obs changes`). The interfaces are in contract.go;
// the local implementation is in local_health.go, the cloud one in
// cloud_health.go.

import (
	"errors"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// HealthRequest asks for a release's health.
type HealthRequest struct {
	// Cloud: the promotion to judge, by id or by release version. Both empty
	// means the env's current promotion.
	PromotionID string
	Release     string
	// Local: the instant to judge from. There are no promotions locally, so
	// the caller names one (`--since`, or the start of the last `forge env up`).
	PromotedAt time.Time
	// Now is the clock.
	Now time.Time
	// Options pick the thresholds. Local applies them all; cloud sends the
	// window and the named overrides and leaves fail_on to the caller
	// (releasehealth.Rejudge).
	Options releasehealth.Options
}

// HealthResult is the judged release.
type HealthResult struct {
	Report releasehealth.Report
	// Statements are what a local judgement ran.
	Statements []Statement
}

// ChangesRequest asks what changed in an env.
type ChangesRequest struct {
	Env string
	// Range bounds the rows, newest first.
	Range Range
	Limit int
	// Project is the forge project whose promotions are listed (cloud).
	Project string
}

// Change kinds.
const (
	ChangePromotion    = "promotion"
	ChangeFailedDeploy = "failed_deploy"
	ChangeActivity     = "activity"
	ChangeSession      = "session"
)

// Change is one row of the feed.
type Change struct {
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`
	Summary     string    `json:"summary"`
	Version     string    `json:"version,omitempty"`
	PromotionID string    `json:"promotion_id,omitempty"`
	Direction   string    `json:"direction,omitempty"`
	Verb        string    `json:"verb,omitempty"`
	Target      string    `json:"target,omitempty"`
	Actor       string    `json:"actor,omitempty"`
	Via         string    `json:"via,omitempty"`
}

// ChangesResult is the feed, newest first.
type ChangesResult struct {
	Changes []Change
	// Omitted counts rows the Limit cut.
	Omitted int
	// Current is the promotion in force at the end of the range, whether or
	// not it falls inside it; nil when the env has none on record.
	Current *Change
	// CurrentUnsettled says Current's rollout had not been reported finished:
	// a promotion is intent, and the env may still be serving the one before.
	CurrentUnsettled bool
	Warnings         []string
}

// current fills res.Current from tl: the promotion live at end (now when end
// is zero).
func (res *ChangesResult) current(tl releasetimeline.Timeline, end time.Time) {
	if end.IsZero() {
		end = time.Now()
	}
	at := tl.At(end)
	if at.Live == nil {
		return
	}
	ch := changeOfEntry(*at.Live)
	res.Current, res.CurrentUnsettled = &ch, at.Unsettled
}

// unimplemented reports whether err says the control plane does not serve the
// procedure yet.
func unimplemented(err error) bool {
	var ce *cloud.Error
	return errors.As(err, &ce) && ce.Code == cloud.CodeUnimplemented
}
