// Copyright (c) 2025 Reliant Labs
package prrecords

import (
	"slices"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// LabelPolicy is what a pipeline's labels mean, for comparing them with the
// records (the shadow report). Labels are a projection of the head's verdict:
// the passed label is on exactly when the head passed, the changes label
// exactly when it got changes.
type LabelPolicy struct {
	Passed  string
	Changes string
	// Held labels take a pull request out of the pipeline: not compared.
	Held []string
	// Check is the name of the check run that reports the head's verdict.
	Check string
	// Settle is how long after a record last changed a disagreement is
	// still expected (the pipeline writing labels after its verdict).
	Settle time.Duration
}

// DefaultLabelPolicy is Reliant's built-in review pipeline's vocabulary.
func DefaultLabelPolicy() LabelPolicy {
	return LabelPolicy{
		Passed: "review:passed", Changes: "review:changes", Held: []string{"hold"},
		Check: "Reliant review", Settle: 10 * time.Minute,
	}
}

// Diff classes, one per open pull request.
const (
	DiffAgree          = "agree"
	DiffLabelMissing   = "label_missing"   // the head has a verdict its label does not show
	DiffLabelStale     = "label_stale"     // a verdict label the head's verdict does not support
	DiffCheckDisagrees = "check_disagrees" // the verdict check concluded against the verdict
	DiffSettling       = "settling"        // a disagreement within the settle window
	DiffHeld           = "held"
	DiffDraft          = "draft"
)

// DiffRow is one open pull request compared.
type DiffRow struct {
	Number  int    `json:"number"`
	Head    string `json:"head"`
	Verdict string `json:"verdict"`
	// Check is the conclusion of the policy's check on the head ("" when
	// there is none, or it has not completed).
	Check  string   `json:"check"`
	Labels []string `json:"labels"`
	Class  string   `json:"class"`
}

// Classify compares one open record's labels and verdict check with its head's
// verdict. checks are the checks on the record's current head.
func Classify(rec *core.PRRecord, v Verdict, checks []core.PRRecordCheck, lp LabelPolicy, now time.Time) DiffRow {
	row := DiffRow{Number: rec.Number, Head: rec.HeadSHA, Verdict: v.Kind, Labels: rec.Labels}
	var newest int64
	for _, c := range checks {
		if c.HeadSHA == rec.HeadSHA && c.Name == lp.Check && c.CheckRunID > newest && c.CompletedAt != nil {
			newest, row.Check = c.CheckRunID, c.Conclusion
		}
	}
	switch {
	case rec.Draft:
		row.Class = DiffDraft
		return row
	case slices.ContainsFunc(lp.Held, func(l string) bool { return slices.Contains(rec.Labels, l) }):
		row.Class = DiffHeld
		return row
	}
	hasPassed, hasChanges := slices.Contains(rec.Labels, lp.Passed), slices.Contains(rec.Labels, lp.Changes)
	switch {
	case (v.Kind == core.PRReviewPassed && !hasPassed) || (v.Kind == core.PRReviewChanges && !hasChanges):
		row.Class = DiffLabelMissing
	case (hasPassed && v.Kind != core.PRReviewPassed) || (hasChanges && v.Kind != core.PRReviewChanges):
		row.Class = DiffLabelStale
	case row.Check != "" && (row.Check == "success") != (v.Kind == core.PRReviewPassed):
		row.Class = DiffCheckDisagrees
	default:
		row.Class = DiffAgree
		return row
	}
	if lp.Settle > 0 && now.Sub(rec.ObservedAt) < lp.Settle {
		row.Class = DiffSettling
	}
	return row
}
