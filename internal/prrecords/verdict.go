// Copyright (c) 2025 Reliant Labs
package prrecords

import (
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Policy is whose review markers count: the reader's, never the record's.
// An entry matches a review author's login (case-insensitive) or numeric id.
type Policy struct {
	// TrustedAuthors may post a head's verdict (pr-review's marker_authors).
	TrustedAuthors []string
	// RereviewAuthors may re-decide a head by an evidence re-review.
	RereviewAuthors []string
}

// Verdict is a head's one authoritative review verdict.
type Verdict struct {
	// Kind is core.PRReviewPassed, core.PRReviewChanges, or "" for none.
	Kind        string
	ReviewID    string
	AuthorLogin string
	SubmittedAt time.Time
	// Rereview is true when an evidence re-review decided it.
	Rereview bool
}

// HeadVerdict applies the one-verdict-per-head rule (control-plane
// docs/pr-pipeline.md, "One verdict per head") to a record's review markers:
//
//   - only a review submitted on the head, whose marker names the head, by a
//     trusted author, counts;
//   - the newest evidence re-review of the head by a re-review author, when
//     there is one, decides it;
//   - otherwise the FIRST passed-or-changes review decides it, and every later
//     marker on the head is history.
//
// The order is submission time, then review id, so it does not depend on the
// order the deliveries arrived in.
func HeadVerdict(reviews []core.PRRecordReview, head string, p Policy) Verdict {
	head = strings.ToLower(head)
	type agg struct {
		first core.PRRecordReview
		kinds map[string]bool
	}
	byReview := map[string]*agg{}
	var order []string
	for _, r := range reviews {
		if r.CommitSHA != head || r.MarkerSHA != head {
			continue
		}
		a := byReview[r.ReviewID]
		if a == nil {
			a = &agg{first: r, kinds: map[string]bool{}}
			byReview[r.ReviewID] = a
			order = append(order, r.ReviewID)
		}
		a.kinds[r.Kind] = true
	}
	verdictOf := func(a *agg) string {
		switch {
		case a.kinds[core.PRReviewPassed]:
			return core.PRReviewPassed
		case a.kinds[core.PRReviewChanges]:
			return core.PRReviewChanges
		}
		return ""
	}
	var first, rereview *agg
	for _, id := range order {
		a := byReview[id]
		kind := verdictOf(a)
		if kind == "" {
			continue
		}
		if a.kinds[core.PRReviewRereview] && matches(p.RereviewAuthors, a.first) {
			if rereview == nil || before(rereview.first, a.first) {
				rereview = a
			}
			continue
		}
		if matches(p.TrustedAuthors, a.first) && (first == nil || before(a.first, first.first)) {
			first = a
		}
	}
	pick, isRereview := first, false
	if rereview != nil {
		pick, isRereview = rereview, true
	}
	if pick == nil {
		return Verdict{}
	}
	return Verdict{
		Kind: verdictOf(pick), ReviewID: pick.first.ReviewID, AuthorLogin: pick.first.AuthorLogin,
		SubmittedAt: pick.first.SubmittedAt, Rereview: isRereview,
	}
}

// before orders reviews by submission, then by numeric review id.
func before(a, b core.PRRecordReview) bool {
	if !a.SubmittedAt.Equal(b.SubmittedAt) {
		return a.SubmittedAt.Before(b.SubmittedAt)
	}
	if len(a.ReviewID) != len(b.ReviewID) {
		return len(a.ReviewID) < len(b.ReviewID)
	}
	return a.ReviewID < b.ReviewID
}

func matches(authors []string, r core.PRRecordReview) bool {
	for _, a := range authors {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if (r.AuthorID != "" && a == r.AuthorID) || strings.EqualFold(a, r.AuthorLogin) {
			return true
		}
	}
	return false
}
