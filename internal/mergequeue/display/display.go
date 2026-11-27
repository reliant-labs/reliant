// Copyright (c) 2025 Reliant Labs

// Package display renders the merge queue's state onto GitHub, one way: a
// display-only check run named "Reliant" on each queued PR head, and one PR
// comment per ejection or needs-a-person (pipeline redesign §2 "State in the
// DB, display on GitHub", §5; MVP task M11, Q7-lite).
//
// Everything shown is computed from DB state ([FromState]) and nothing shown
// is ever read back: the check is not an authority, admission is (§3.1). The
// queue's other writers branch on nothing they find here either.
//
// Shadow mode makes no GitHub write. [Projector.Publish] is the one place that
// rule is kept for display: in shadow it records what it would have shown
// into the batch's verdict_json.display, in on it writes it to GitHub, and in
// off it does nothing.
//
//forge:exclude-contract: called directly by the merge queue's lander and scheduler; its seams are the consumer-side Store interface and ghport.Client, and it holds no state of its own
package display

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// CheckName is the one check run the queue posts on a PR head.
const CheckName = "Reliant"

// State is what the Reliant check says about one PR head.
type State string

const (
	// StateQueued: approved and waiting for a batch.
	StateQueued State = "queued"
	// StateGating: in a batch being built, gated or landed.
	StateGating State = "gating"
	// StateLanded: squash-merged onto the base branch by the queue.
	StateLanded State = "landed"
	// StateEjected: taken out of the queue, with a reason.
	StateEjected State = "ejected"
	// StateNeedsPerson: the pipeline asks a person to act.
	StateNeedsPerson State = "needs_person"
)

// View is one PR head's display, computed from DB state.
type View struct {
	Number int    `json:"number"`
	Head   string `json:"head"`
	State  State  `json:"state"`
	// Reason is an ejection's reason (queue_entries.eject_reason) or a
	// needs-person reason.
	Reason string `json:"reason,omitempty"`
	// Detail is free text for a person: the evidence.
	Detail    string `json:"detail,omitempty"`
	BatchID   string `json:"batch_id,omitempty"`
	LandedSHA string `json:"landed_sha,omitempty"`
	// EntryID keys an ejection's comment, so a retry posts it once.
	EntryID string `json:"entry_id,omitempty"`
}

// FromState computes a PR head's view from its queue entry, the batch it is
// (or was last) in (nil when none) and its pr_pipeline row (nil when none).
// A needs-person row wins: it is what a person must see.
func FromState(number int, e *core.QueueEntry, batch *core.QueueBatch, pp *core.PRPipeline) View {
	v := View{Number: number}
	if e != nil {
		v.Head, v.EntryID, v.BatchID = e.HeadSHA, e.ID, e.BatchID
		switch e.State {
		case core.QueueEntryWaiting:
			v.State = StateQueued
		case core.QueueEntryBatched:
			v.State = StateGating
		case core.QueueEntryLanded:
			v.State, v.LandedSHA = StateLanded, e.LandedSHA
		case core.QueueEntryEjected, core.QueueEntryRemoved:
			v.State, v.Reason = StateEjected, string(e.EjectReason)
			if e.State == core.QueueEntryRemoved {
				v.Reason = "removed"
			}
		}
		if batch != nil && batch.ID == e.BatchID && e.State == core.QueueEntryBatched && batch.State == core.QueueBatchPassed {
			v.Detail = "the gate passed; landing"
		}
	}
	if pp != nil && pp.State == core.PRPipelineNeedsPerson {
		v.State, v.Reason = StateNeedsPerson, pp.NeedsPersonReason
		if v.Head == "" {
			v.Head = pp.StateHeadSHA
		}
	}
	return v
}

// Check renders a view's check run.
func Check(v View) ghport.CreateCheckRun {
	in := ghport.CreateCheckRun{Name: CheckName, HeadSHA: v.Head, ExternalID: v.BatchID}
	switch v.State {
	case StateQueued:
		in.Status = ghport.CheckQueued
		in.Output = ghport.CheckOutput{Title: "Queued", Summary: "Approved, and waiting for the next batch of the Reliant merge queue."}
	case StateGating:
		in.Status = ghport.CheckInProgress
		in.Output = ghport.CheckOutput{Title: "Gating", Summary: "In a batch the Reliant merge queue is gating" + batchSuffix(v) + "."}
	case StateLanded:
		in.Status, in.Conclusion = ghport.CheckCompleted, ghport.ConclusionSuccess
		in.Output = ghport.CheckOutput{Title: "Landed", Summary: "Landed by the Reliant merge queue as " + short(v.LandedSHA) + batchSuffix(v) + "."}
	case StateEjected:
		in.Status, in.Conclusion = ghport.CheckCompleted, ghport.ConclusionFailure
		in.Output = ghport.CheckOutput{Title: "Ejected: " + reasonText(v.Reason), Summary: "The Reliant merge queue took this head out of the queue: " + reasonText(v.Reason) + "."}
	case StateNeedsPerson:
		in.Status, in.Conclusion = ghport.CheckCompleted, ghport.ConclusionActionRequired
		in.Output = ghport.CheckOutput{Title: "Needs a person", Summary: "The Reliant pipeline needs a person to act: " + reasonText(v.Reason) + "."}
	default:
		in.Status = ghport.CheckQueued
		in.Output = ghport.CheckOutput{Title: string(v.State)}
	}
	if v.Detail != "" {
		in.Output.Text = v.Detail
	}
	return in
}

// Comment renders a view's PR comment: one per ejection and per
// needs-person. ok is false for every other state.
func Comment(v View) (body string, ok bool) {
	var b strings.Builder
	switch v.State {
	case StateEjected:
		fmt.Fprintf(&b, "The Reliant merge queue took `%s` out of the queue: **%s**.", short(v.Head), reasonText(v.Reason))
	case StateNeedsPerson:
		fmt.Fprintf(&b, "The Reliant pipeline needs a person to act on this pull request: **%s**.", reasonText(v.Reason))
	default:
		return "", false
	}
	if v.Detail != "" {
		b.WriteString("\n\n" + v.Detail)
	}
	b.WriteString("\n\n" + marker(v))
	return b.String(), true
}

// marker keys a comment so a retry, or a second Publish of the same state,
// never posts it twice.
func marker(v View) string {
	key := v.EntryID
	if key == "" {
		key = v.Head
	}
	return fmt.Sprintf("<!-- reliant-queue:%s:%s:%s -->", v.State, key, v.Reason)
}

func batchSuffix(v View) string {
	if v.BatchID == "" {
		return ""
	}
	return " (batch " + v.BatchID + ")"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func reasonText(reason string) string {
	switch core.QueueEjectReason(reason) {
	case core.QueueEjectConflict:
		return "it conflicts with the base branch"
	case core.QueueEjectGateRed:
		return "the gate failed"
	case core.QueueEjectHeadMoved:
		return "the head moved after it was approved"
	case core.QueueEjectHold:
		return "it is on hold"
	case core.QueueEjectClosed:
		return "the pull request was closed"
	}
	if reason == "" {
		return "no reason recorded"
	}
	return reason
}

// Store is what the projector records shadow display into.
type Store interface {
	GetQueueBatch(ctx context.Context, id string) (*core.QueueBatch, error)
	PatchQueueBatchVerdict(ctx context.Context, id string, patch json.RawMessage) (*core.QueueBatch, error)
}

// Projector publishes views. It is safe for concurrent use.
type Projector struct {
	Store  Store
	Logger *slog.Logger
}

// Rendered is one view as shadow mode records it in
// queue_batches.verdict_json.display, keyed by PR number.
type Rendered struct {
	View    View                  `json:"view"`
	Check   ghport.CreateCheckRun `json:"check"`
	Comment string                `json:"comment,omitempty"`
}

// Publish shows views on GitHub (mode on), records them into batchID's
// verdict_json.display (mode shadow; batchID is required then), or does
// nothing (mode off). In on, each view's Reliant check on its head is updated
// in place (created when there is none), and its comment is posted unless a
// comment with its marker is already there. It returns every failure joined;
// display never decides anything, so a caller logs and carries on.
func (p *Projector) Publish(ctx context.Context, repo *core.PipelineRepo, gh ghport.Client, batchID string, views []View) error {
	if repo == nil || len(views) == 0 {
		return nil
	}
	switch repo.Mode {
	case core.PipelineRepoModeShadow:
		return p.record(ctx, batchID, views)
	case core.PipelineRepoModeOn:
	default:
		return nil
	}
	ghRepo, err := ghport.ParseRepo(repo.Repository)
	if err != nil {
		return err
	}
	var errs []error
	for _, v := range views {
		if v.Head == "" || v.Number == 0 {
			continue
		}
		if err := p.check(ctx, gh, ghRepo, v); err != nil {
			errs = append(errs, fmt.Errorf("check on #%d: %w", v.Number, err))
		}
		if body, ok := Comment(v); ok {
			if err := p.comment(ctx, gh, ghRepo, v.Number, body, marker(v)); err != nil {
				errs = append(errs, fmt.Errorf("comment on #%d: %w", v.Number, err))
			}
		}
	}
	err = errors.Join(errs...)
	if err != nil && p.Logger != nil {
		p.Logger.WarnContext(ctx, "merge queue display failed", "repository", repo.Repository, "err", err)
	}
	return err
}

func (p *Projector) check(ctx context.Context, gh ghport.Client, repo ghport.Repo, v View) error {
	in := Check(v)
	runs, err := gh.ListCheckRuns(ctx, repo, v.Head, ghport.ListCheckRuns{Name: CheckName})
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		_, err = gh.CreateCheckRun(ctx, repo, in)
		return err
	}
	out := in.Output
	upd := ghport.UpdateCheckRun{Status: in.Status, Conclusion: in.Conclusion, Output: &out}
	if in.Conclusion != "" {
		upd.Status = ghport.CheckCompleted
	}
	if runs[0].Status == ghport.CheckCompleted && in.Conclusion == "" {
		// GitHub cannot reopen a completed run: a head shown completed and
		// queued again (a requeue after an integrity pause) gets a new one.
		_, err = gh.CreateCheckRun(ctx, repo, in)
		return err
	}
	_, err = gh.UpdateCheckRun(ctx, repo, runs[0].ID, upd)
	return err
}

func (p *Projector) comment(ctx context.Context, gh ghport.Client, repo ghport.Repo, number int, body, mark string) error {
	existing, err := gh.ListIssueComments(ctx, repo, number)
	if err != nil {
		return err
	}
	for _, c := range existing {
		if strings.Contains(c.Body, mark) {
			return nil
		}
	}
	_, err = gh.CreateIssueComment(ctx, repo, number, body)
	return err
}

// record merges views into the batch's verdict_json.display.
func (p *Projector) record(ctx context.Context, batchID string, views []View) error {
	if batchID == "" || p.Store == nil {
		return nil
	}
	b, err := p.Store.GetQueueBatch(ctx, batchID)
	if err != nil || b == nil {
		return err
	}
	shown := map[string]Rendered{}
	var doc struct {
		Display map[string]Rendered `json:"display"`
	}
	if len(b.VerdictJSON) > 0 && json.Unmarshal(b.VerdictJSON, &doc) == nil && doc.Display != nil {
		shown = doc.Display
	}
	for _, v := range views {
		r := Rendered{View: v, Check: Check(v)}
		r.Comment, _ = Comment(v)
		shown[strconv.Itoa(v.Number)] = r
	}
	patch, err := json.Marshal(map[string]any{"display": shown})
	if err != nil {
		return err
	}
	_, err = p.Store.PatchQueueBatchVerdict(ctx, batchID, patch)
	return err
}

// Recorded reads the shadow display out of a batch's verdict_json, in PR
// number order.
func Recorded(verdict json.RawMessage) []Rendered {
	var doc struct {
		Display map[string]Rendered `json:"display"`
	}
	if len(verdict) == 0 || json.Unmarshal(verdict, &doc) != nil {
		return nil
	}
	out := make([]Rendered, 0, len(doc.Display))
	for _, r := range doc.Display {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].View.Number < out[j].View.Number })
	return out
}
