// Copyright (c) 2025 Reliant Labs
package prrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Deps is what a projector needs.
type Deps struct {
	// Store keeps the records.
	Store Store
	// Scope is which repositories records are kept for.
	Scope Scope
	// Pipeline, when set, also keeps records for every repository enrolled
	// in the PR pipeline (mode shadow or on), and projects their PR events
	// into pr_pipeline and their base-branch pushes into a pending read of
	// the repository's config, which the worker's ConfigRefresher does
	// (pipeline.go). nil (pipeline.enabled off): neither, and the
	// projector is exactly the records projector.
	// forge:optional-dep
	Pipeline PipelineStore
}

// projector applies deliveries to records. It is safe for concurrent use.
//
//forge:contract
type projector struct {
	store    Store
	scope    Scope
	pipeline PipelineStore
	newID    func() string
	now      func() time.Time
}

// New builds a projector that keeps records for deps.Scope.
func New(deps Deps) Service {
	return &projector{store: deps.Store, scope: deps.Scope, pipeline: deps.Pipeline,
		newID: uuid.NewString, now: time.Now}
}

// Apply applies every event of a delivery that concerns a recorded
// repository's pull requests, each at most once. An error leaves the events
// not yet applied unclaimed, so retrying the delivery finishes the job.
//
// An enrolled repository's events are then projected into the pipeline
// (projectPipeline). That projection is level-triggered (it reads the
// record as applied, not the event), so a retried delivery repeats it
// harmlessly.
func (p *projector) Apply(ctx context.Context, d Delivery) error {
	if (p.scope.Empty() && p.pipeline == nil) || d.Key == "" {
		return nil
	}
	now := p.now().UTC()
	enrolments := map[string]*core.PipelineRepo{}
	for i, ev := range d.Events {
		repo, err := p.enrolment(ctx, d.Provider, ev, enrolments)
		if err != nil {
			return fmt.Errorf("prrecords: %s event %d: %w", ev.Type, i, err)
		}
		if c, ok := p.decode(d.Provider, ev, now, repo != nil); ok {
			c.Delivery = core.PRRecordDelivery{
				Provider: d.Provider, DeliveryKey: d.Key, EventIndex: i,
				EventType: ev.Type, AppliedAt: now,
			}
			if _, err := p.store.ApplyPRRecordChange(ctx, c); err != nil {
				return fmt.Errorf("prrecords: apply %s event %d: %w", ev.Type, i, err)
			}
		}
		if repo != nil {
			if err := p.projectPipeline(ctx, d.Provider, ev, repo, now); err != nil {
				return fmt.Errorf("prrecords: project %s event %d into the pipeline: %w", ev.Type, i, err)
			}
		}
	}
	return nil
}

// decode turns one event into the change it makes, or false when it makes
// none: another provider, a repository out of scope (neither in Scope nor
// enrolled), an event type records do not follow, or an event missing what
// its type must carry.
func (p *projector) decode(provider string, ev Event, now time.Time, enrolled bool) (core.PRRecordChange, bool) {
	repository := ev.Attributes["repository"]
	if ev.ResourceKey == "" || (!enrolled && !p.scope.Includes(provider, repository)) {
		return core.PRRecordChange{}, false
	}
	family, action, _ := strings.Cut(ev.Type, ".")
	switch family {
	case "pull_request":
		snap, ok := p.snapshot(provider, ev, repository, mapOf(ev.Data["pull_request"]), now)
		if !ok {
			return core.PRRecordChange{}, false
		}
		c := core.PRRecordChange{Snapshot: snap, HeadID: p.newID()}
		c.PreviousHeadSHA = delta(action, ev, snap)
		return c, true
	case "pull_request_review":
		if action != "submitted" {
			return core.PRRecordChange{}, false
		}
		snap, ok := p.snapshot(provider, ev, repository, mapOf(ev.Data["pull_request"]), now)
		if !ok {
			return core.PRRecordChange{}, false
		}
		return core.PRRecordChange{Snapshot: snap, HeadID: p.newID(), Reviews: p.reviews(mapOf(ev.Data["review"]), now)}, true
	case "check_run":
		check, ok := p.check(provider, ev, mapOf(ev.Data["check_run"]), now)
		if !ok {
			return core.PRRecordChange{}, false
		}
		return core.PRRecordChange{Check: check}, true
	}
	return core.PRRecordChange{}, false
}

// delta records on the snapshot what this event's own action changed, which
// is all that applies when the record already holds a snapshot of the same
// second (core.PRRecordDelta). It returns the head a push replaced.
func delta(action string, ev Event, snap *core.PRRecordSnapshot) (previousHead string) {
	d := &snap.Delta
	switch action {
	case "labeled":
		d.LabelAdded = strOf(ev.Data["label"])
	case "unlabeled":
		d.LabelRemoved = strOf(ev.Data["label"])
	case "closed", "reopened":
		d.State = snap.State
		d.MergedAt, d.MergeCommit = snap.MergedAt, snap.MergeCommit
	case "ready_for_review", "converted_to_draft":
		draft := snap.Draft
		d.Draft = &draft
	case "edited":
		d.Title, d.BaseRef = snap.Title, snap.BaseRef
	case "synchronize":
		before := lowerSHA(strOf(ev.Data["before"]))
		if !isSHA(before) {
			return ""
		}
		after := lowerSHA(strOf(ev.Data["after"]))
		if !isSHA(after) {
			after = snap.HeadSHA
		}
		d.FromHead, d.HeadSHA, d.HeadRef = before, after, snap.HeadRef
		return before
	}
	return ""
}

// snapshot reads the pull request object of a GitHub event's data
// (projection/objects/pr.yaml). A review event carries the slimmer object
// without `merged`: it is not Full, so it only creates a missing record.
func (p *projector) snapshot(provider string, ev Event, repository string, pr map[string]any, now time.Time) (*core.PRRecordSnapshot, bool) {
	number, ok := intOf(pr["number"])
	if !ok || number <= 0 {
		return nil, false
	}
	updated, err := time.Parse(time.RFC3339, strOf(pr["updated_at"]))
	if err != nil {
		return nil, false
	}
	merged, full := pr["merged"].(bool)
	state := core.PRRecordOpen
	if strOf(pr["state"]) == "closed" {
		state = core.PRRecordClosed
		if merged {
			state = core.PRRecordMerged
		}
	}
	head, base, user := mapOf(pr["head"]), mapOf(pr["base"]), mapOf(pr["user"])
	s := &core.PRRecordSnapshot{
		ID:           p.newID(),
		Provider:     provider,
		AccountKey:   ev.AccountKey,
		RepositoryID: ev.ResourceKey,
		Repository:   repository,
		Number:       number,
		State:        state,
		Draft:        pr["draft"] == true,
		HeadSHA:      lowerSHA(strOf(head["sha"])),
		HeadRef:      strOf(head["ref"]),
		BaseRef:      strOf(base["ref"]),
		BaseSHA:      lowerSHA(strOf(base["sha"])),
		AuthorLogin:  strOf(user["login"]),
		Title:        strOf(pr["title"]),
		Labels:       stringsOf(pr["labels"]),
		MergeCommit:  lowerSHA(strOf(pr["merge_commit_sha"])),
		SnapshotAt:   updated.UTC(),
		ObservedAt:   now,
		Full:         full,
		// head.repo is absent when the head repository was deleted.
		HeadRepository: strOf(mapOf(head["repo"])["full_name"]),
	}
	if id, ok := intOf(user["id"]); ok {
		s.AuthorID = strconv.FormatInt(int64(id), 10)
	}
	if t, err := time.Parse(time.RFC3339, strOf(pr["merged_at"])); err == nil {
		t = t.UTC()
		s.MergedAt = &t
	}
	return s, true
}

// Pipeline marker names a review body may carry (control-plane
// docs/pr-pipeline.md, "One verdict per head"), and the kind each records.
var markerKinds = map[string]string{
	"review-passed":   core.PRReviewPassed,
	"review-changes":  core.PRReviewChanges,
	"review-close":    core.PRReviewClose,
	"review-rereview": core.PRReviewRereview,
}

// reviews reads the pipeline markers of a submitted review: the receiver's
// `markers` (extracted from the whole body before it is capped), each a
// {name, attrs} object. A marker without a full sha names no head.
func (p *projector) reviews(review map[string]any, now time.Time) []core.PRRecordReview {
	id, ok := intOf(review["id"])
	commit := lowerSHA(strOf(review["commit_id"]))
	if !ok || !isSHA(commit) {
		return nil
	}
	submitted, err := time.Parse(time.RFC3339, strOf(review["submitted_at"]))
	if err != nil {
		submitted = now
	}
	user := mapOf(review["user"])
	authorID := ""
	if uid, ok := intOf(user["id"]); ok {
		authorID = strconv.FormatInt(int64(uid), 10)
	}
	var out []core.PRRecordReview
	seen := map[string]bool{}
	for _, m := range listOf(review["markers"]) {
		marker := mapOf(m)
		kind, ok := markerKinds[strOf(marker["name"])]
		attrs := mapOf(marker["attrs"])
		sha := lowerSHA(strOf(attrs["sha"]))
		if !ok || !isSHA(sha) || seen[kind] {
			continue
		}
		seen[kind] = true
		r := core.PRRecordReview{
			ID: p.newID(), ReviewID: strconv.FormatInt(int64(id), 10), CommitSHA: commit,
			MarkerSHA: sha, Kind: kind, AuthorID: authorID, AuthorLogin: strOf(user["login"]),
			SubmittedAt: submitted.UTC(), ObservedAt: now,
		}
		if kind == core.PRReviewRereview {
			if of, ok := intOf(attrs["of"]); ok {
				r.OfReviewID = strconv.FormatInt(int64(of), 10)
			}
		}
		out = append(out, r)
	}
	return out
}

// check reads a check_run event's run.
func (p *projector) check(provider string, ev Event, run map[string]any, now time.Time) (*core.PRRecordCheck, bool) {
	id, ok := intOf(run["id"])
	sha := lowerSHA(strOf(run["head_sha"]))
	name := strOf(run["name"])
	if !ok || !isSHA(sha) || name == "" {
		return nil, false
	}
	c := &core.PRRecordCheck{
		ID: p.newID(), Provider: provider, RepositoryID: ev.ResourceKey, HeadSHA: sha,
		Name: name, AppSlug: strOf(mapOf(run["app"])["slug"]), CheckRunID: int64(id),
		Status: strOf(run["status"]), Conclusion: strOf(run["conclusion"]), ObservedAt: now,
	}
	if t, err := time.Parse(time.RFC3339, strOf(run["completed_at"])); err == nil {
		t = t.UTC()
		c.CompletedAt = &t
	}
	return c, true
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func isSHA(s string) bool { return shaRE.MatchString(s) }

func lowerSHA(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// The data is the receiver's normalized payload: in memory its numbers are
// int64, and read back from storage they are float64 or json.Number.

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func stringsOf(v any) []string {
	var out []string
	switch l := v.(type) {
	case []string:
		out = append(out, l...)
	case []any:
		for _, it := range l {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func intOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int64(n)) {
			return int(n), true
		}
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return int(i), true
		}
	}
	return 0, false
}
