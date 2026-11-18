// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// The syncer's pace.
const (
	// syncInterval is how often Run looks for due checks. A run's start and
	// end make their check due at once, so this is the latency of a
	// transition, not of polling a live run.
	syncInterval = 3 * time.Second
	// pollLive is when a check that is not final is next looked at, for the
	// transitions nothing reports (a firing that launched or was skipped, a
	// run that paused or died): GitHub sees at most one update per check in
	// this long, unless the run itself reported.
	pollLive = 15 * time.Second
	// lease is how long a claimed check is held before another syncer may
	// take it.
	lease = 2 * time.Minute
	// batch is how many checks one pass claims.
	batch = 25
	// maxBackoff caps the wait after a failed GitHub write.
	maxBackoff = 5 * time.Minute
	// giveUpAfter is how long past a check's timeout failed writes keep
	// being retried before the syncer gives up on it.
	giveUpAfter = time.Hour
)

type service struct {
	store  Store
	github GitHub
	runURL func(string) string
	now    func() time.Time
}

func newService(deps Deps) *service {
	return &service{store: deps.Store, github: deps.GitHub, runURL: deps.RunURL, now: time.Now}
}

// Open implements Service.
func (s *service) Open(ctx context.Context, f Firing) error {
	spec, err := SpecOf(f.Workflow)
	if err != nil {
		logging.WarnContext(ctx, "[workflowcheck] the workflow's check is invalid; this firing posts none", "error", err)
		return nil
	}
	if spec == nil || f.Event == nil || f.Trigger == nil {
		return nil
	}
	target, err := ResolveTarget(spec, f.Event.Payload, f.Scope)
	if err != nil {
		logging.WarnContext(ctx, "[workflowcheck] this firing posts no check", "trigger_id", f.Trigger.ID, "event_id", f.Event.ID, "check", spec.Name, "reason", err)
		return nil
	}
	now := s.now().UTC()
	row := &core.WorkflowCheckRun{
		ID:             uuid.NewString(),
		TriggerEventID: f.Event.ID,
		Name:           spec.Name,
		UserID:         f.Trigger.UserID,
		OrgID:          f.Trigger.OrgID,
		ActingIdentity: string(f.Trigger.ActingIdentity),
		Automation:     f.Trigger.Name,
		Owner:          target.Owner,
		Repo:           target.Repo,
		HeadSHA:        target.HeadSHA,
		Timeout:        spec.Timeout,
		NextSyncAt:     now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := s.store.CreateWorkflowCheckRun(ctx, row); err != nil {
		return fmt.Errorf("record check %q for event %s: %w", spec.Name, f.Event.ID, err)
	}
	return nil
}

// Started implements Service.
func (s *service) Started(ctx context.Context, chatID string) error {
	if chatID == "" {
		return nil
	}
	return s.store.MarkWorkflowCheckRunsStarted(ctx, chatID, s.now().UTC())
}

// Ended implements Service.
func (s *service) Ended(ctx context.Context, chatID string, ending core.WorkflowCheckEnding) error {
	if chatID == "" {
		return nil
	}
	switch ending.Status {
	case core.TriggerRunCompleted, core.TriggerRunFailed, core.TriggerRunCancelled:
	default:
		return fmt.Errorf("check ending status %q", ending.Status)
	}
	return s.store.RecordWorkflowCheckRunsEnded(ctx, chatID, ending, s.now().UTC())
}

// Syncer is the worker's background loop: it runs a Service's SyncOnce every
// few seconds until its context ends. Every worker runs one; checks are
// leased with SKIP LOCKED, so the workers share them.
type Syncer struct {
	svc      Service
	interval time.Duration
	scope    func(context.Context) context.Context
}

// NewSyncer builds the loop over svc. scope, when set, derives the context
// every pass runs in (the org policy resolver, as an activity's context
// carries it).
func NewSyncer(svc Service, scope func(context.Context) context.Context) *Syncer {
	return &Syncer{svc: svc, interval: syncInterval, scope: scope}
}

// Run syncs until ctx is done (a lifecyclekit runner).
func (s *Syncer) Run(ctx context.Context) error {
	if s.scope != nil {
		ctx = s.scope(ctx)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		if _, err := s.svc.SyncOnce(ctx); err != nil && ctx.Err() == nil {
			logging.WarnContext(ctx, "[workflowcheck] sync pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// SyncOnce implements Service.
func (s *service) SyncOnce(ctx context.Context) (int, error) {
	if s.github == nil {
		return 0, errors.New("workflowcheck: no GitHub writer in this process")
	}
	now := s.now().UTC()
	rows, err := s.store.ClaimWorkflowCheckRuns(ctx, now, now.Add(lease), batch)
	if err != nil {
		return 0, err
	}
	synced := 0
	var errs []error
	for _, row := range rows {
		if err := s.sync(ctx, row); err != nil {
			errs = append(errs, fmt.Errorf("check %s (%s on %s/%s@%s): %w", row.ID, row.Name, row.Owner, row.Repo, row.HeadSHA, err))
			continue
		}
		synced++
	}
	return synced, errors.Join(errs...)
}

// sync brings one check up to date and saves the result, whatever happened:
// a failed write is recorded and retried later, never lost.
func (s *service) sync(ctx context.Context, row *core.WorkflowCheckRun) error {
	now := s.now().UTC()
	obs, err := s.store.ObserveWorkflowCheckRun(ctx, row.TriggerEventID)
	switch {
	case errors.Is(err, core.ErrWorkflowCheckRunNotFound):
		obs = nil
	case err != nil:
		return s.failed(ctx, row, now, fmt.Errorf("observe: %w", err))
	}
	st, paused := derive(row, obs, now)
	row.PausedSince = paused
	chatID := ""
	if obs != nil {
		chatID = obs.ChatID
	}
	out := outputFor(st, row.Output, row.OutputError, s.link(chatID))
	if err := s.push(ctx, row, st, out, s.link(chatID), now); err != nil {
		return s.failed(ctx, row, now, err)
	}
	row.Attempts, row.LastError = 0, ""
	if st.final() && row.AnnotationsSent >= len(out.Annotations) {
		row.FinishedAt = ptr.Of(now)
	} else {
		row.NextSyncAt = now.Add(pollLive)
	}
	row.ClaimedUntil = nil
	row.UpdatedAt = now
	return s.store.SaveWorkflowCheckRunSync(ctx, row)
}

// failed records a failed sync: backoff, and past the check's timeout plus
// giveUpAfter, giving up.
//
// A refusal (ErrNotAuthorized: the owner left the org, lost the repository,
// or the activation cannot write as the App) abandons the check at once: the
// row is finished with the reason in last_error, and nothing more is written
// to GitHub, because every write would be made with the authority that was
// refused. A check already open there stays as it is, which a required check
// never counts as passing.
func (s *service) failed(ctx context.Context, row *core.WorkflowCheckRun, now time.Time, cause error) error {
	row.Attempts++
	row.LastError = truncate(cause.Error(), 2048)
	wait := time.Duration(1<<min(row.Attempts, 10)) * 5 * time.Second
	if wait > maxBackoff {
		wait = maxBackoff
	}
	row.NextSyncAt = now.Add(wait)
	refused := errors.Is(cause, ErrNotAuthorized)
	switch {
	case refused:
		row.LastError = truncate("abandoned, not authorized: "+cause.Error(), 2048)
		row.FinishedAt = ptr.Of(now)
		logging.WarnContext(ctx, "[workflowcheck] abandoned a check its activation may no longer write", "check_id", row.ID, "name", row.Name, "reason", cause)
	case now.After(row.CreatedAt.Add(row.Timeout + giveUpAfter)):
		row.LastError = truncate("abandoned after retries: "+cause.Error(), 2048)
		row.FinishedAt = ptr.Of(now)
		logging.WarnContext(ctx, "[workflowcheck] gave up on a check that could not be written", "check_id", row.ID, "name", row.Name, "error", cause)
	}
	row.ClaimedUntil = nil
	row.UpdatedAt = now
	if err := s.store.SaveWorkflowCheckRunSync(ctx, row); err != nil {
		return errors.Join(cause, fmt.Errorf("save: %w", err))
	}
	if refused {
		return nil
	}
	return cause
}

// push writes st and out to GitHub when they differ from what it holds, then
// the output's annotations not yet sent. It updates row's GitHub fields as
// each write lands, so a failure part-way keeps what did.
func (s *service) push(ctx context.Context, row *core.WorkflowCheckRun, st state, out Output, detailsURL string, now time.Time) error {
	status := st.Status
	// A check moves forward only: once in_progress, a run that reads as
	// starting again (a resumed run's row, for a moment) changes nothing.
	if status == core.CheckStatusQueued && row.GitHubStatus == core.CheckStatusInProgress {
		return nil
	}
	conclusion := ""
	if status == core.CheckStatusCompleted {
		conclusion = st.Conclusion
	}
	want := digest(status, conclusion, out)
	pendingAnnotations := status == core.CheckStatusCompleted && row.AnnotationsSent < len(out.Annotations)
	if row.GitHubCheckRunID != nil && row.GitHubDigest == want && !pendingAnnotations {
		return nil
	}
	runs, err := s.github.CheckRuns(ctx, Identity{
		UserID: row.UserID, OrgID: ptr.From(row.OrgID), ActingIdentity: row.ActingIdentity, Automation: row.Automation,
	}, row.Owner, row.Repo)
	if err != nil {
		return err
	}
	if row.GitHubCheckRunID == nil || row.GitHubDigest != want {
		w := Write{Status: status, Conclusion: conclusion, ExternalID: row.ID, DetailsURL: detailsURL, Output: &out}
		if status != core.CheckStatusQueued {
			w.StartedAt = row.StartedAt
		}
		if status == core.CheckStatusCompleted {
			w.CompletedAt = ptr.Of(now)
		}
		// Annotations go in their own writes, below.
		w.Output = &Output{Title: out.Title, Summary: out.Summary, Text: out.Text}
		if row.GitHubCheckRunID == nil {
			id, err := runs.Find(ctx, row.HeadSHA, row.Name, row.ID)
			if err != nil {
				return fmt.Errorf("find: %w", err)
			}
			if id == 0 {
				w.Name, w.HeadSHA = row.Name, row.HeadSHA
				if id, err = runs.Create(ctx, w); err != nil {
					return fmt.Errorf("create: %w", err)
				}
				row.GitHubCheckRunID = &id
				row.GitHubStatus, row.GitHubConclusion, row.GitHubDigest = status, conclusion, want
				w = Write{}
			} else {
				row.GitHubCheckRunID = &id
			}
		}
		if w.Status != "" {
			if err := runs.Update(ctx, *row.GitHubCheckRunID, w); err != nil {
				return fmt.Errorf("update: %w", err)
			}
			row.GitHubStatus, row.GitHubConclusion, row.GitHubDigest = status, conclusion, want
		}
	}
	for status == core.CheckStatusCompleted && row.AnnotationsSent < len(out.Annotations) {
		end := min(row.AnnotationsSent+AnnotationBatch, len(out.Annotations))
		page := Output{Title: out.Title, Summary: out.Summary, Text: out.Text, Annotations: out.Annotations[row.AnnotationsSent:end]}
		if err := runs.Update(ctx, *row.GitHubCheckRunID, Write{Output: &page}); err != nil {
			return fmt.Errorf("annotations %d-%d: %w", row.AnnotationsSent, end, err)
		}
		row.AnnotationsSent = end
	}
	return nil
}

func (s *service) link(chatID string) string {
	if chatID == "" || s.runURL == nil {
		return ""
	}
	return s.runURL(chatID)
}
