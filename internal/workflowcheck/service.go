// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
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
	// take it, unless the pass holding it renews it first.
	lease = 2 * time.Minute
	// renewEvery is the heartbeat: how often a pass extends the leases it
	// still holds, its rows waiting their turn included. A third of the
	// lease, so one late or failed renewal does not lose it.
	renewEvery = lease / 3
	// writeMargin is how close to its lease's expiry a sync stops starting
	// GitHub calls. A call in flight at the expiry is cancelled then, but a
	// write GitHub already accepted cannot be taken back, so none starts
	// this close to the moment another syncer may take the row.
	writeMargin = 15 * time.Second
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
	// holder names this service in the leases it takes: one per process,
	// so two workers' syncers never mistake each other's lease for theirs.
	holder string
	// ticker starts a pass's heartbeat (time.NewTicker; a test drives it).
	ticker func(time.Duration) (<-chan time.Time, func())
	// afterFunc runs f once d has passed, unless stopped first
	// (time.AfterFunc; a test fires it from its clock).
	afterFunc func(d time.Duration, f func()) (stop func() bool)
	// renewed, when set, is told the outcome of every renewal (tests).
	renewed func(ids []string, err error)
}

func newService(deps Deps) *service {
	return &service{
		store: deps.Store, github: deps.GitHub, runURL: deps.RunURL, now: time.Now,
		holder: newHolder(), ticker: newTicker, afterFunc: afterFunc,
	}
}

func afterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// newHolder is a lease holder name unique to this process: the host, for
// whoever reads the row, and a random suffix, for uniqueness.
func newHolder() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return host + "/" + uuid.NewString()
}

func newTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
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

// SyncOnce implements Service. It claims a batch under a lease, renews the
// leases it still holds on a heartbeat while it works through the batch, and
// syncs each row it still holds. Writing fails closed on the lease: a row's
// GitHub calls stop when a renewal finds its lease taken, and also when the
// lease reaches its expiry with no renewal since (the renewals failing, the
// database unreachable), because another syncer may then take it. Its save
// is fenced, so what this sync did lands only if nobody did. A row stopped
// for its lease is not a failed sync.
func (s *service) SyncOnce(ctx context.Context) (int, error) {
	if s.github == nil {
		return 0, errors.New("workflowcheck: no GitHub writer in this process")
	}
	now := s.now().UTC()
	rows, err := s.store.ClaimWorkflowCheckRuns(ctx, s.holder, now, now.Add(lease), batch)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	hb := s.startHeartbeat(ctx, rows)
	defer hb.stop()
	synced := 0
	var errs []error
	for _, row := range rows {
		l := hb.begin(ctx, row.ID)
		if l == nil {
			logging.InfoContext(ctx, "[workflowcheck] no longer sure of the lease on a check (lost, or expiring with no renewal); left it to the next claim", "check_id", row.ID)
			continue
		}
		err := s.sync(ctx, l, row)
		l.done()
		switch {
		case errors.Is(err, core.ErrWorkflowCheckLeaseLost):
			logging.InfoContext(ctx, "[workflowcheck] stopped syncing a check for its lease; left it to the next claim", "check_id", row.ID, "detail", err)
		case err != nil:
			errs = append(errs, fmt.Errorf("check %s (%s on %s/%s@%s): %w", row.ID, row.Name, row.Owner, row.Repo, row.HeadSHA, err))
		default:
			synced++
		}
	}
	return synced, errors.Join(errs...)
}

// sync brings one check up to date and saves the result, whatever happened:
// a failed write is recorded and retried later, never lost. l carries the
// reads and GitHub calls and stops them when the lease is lost or expiring;
// the save runs on ctx, and its fence decides whether it lands.
func (s *service) sync(ctx context.Context, l *rowLease, row *core.WorkflowCheckRun) error {
	now := s.now().UTC()
	obs, err := s.store.ObserveWorkflowCheckRun(l.ctx, row.TriggerEventID)
	switch {
	case errors.Is(err, core.ErrWorkflowCheckRunNotFound):
		obs = nil
	case err != nil && l.stopped():
		return s.leaseEnded(ctx, l, row, now, err)
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
	if err := s.push(l, row, st, out, s.link(chatID), now); err != nil {
		if l.stopped() {
			return s.leaseEnded(ctx, l, row, now, err)
		}
		return s.failed(ctx, row, now, err)
	}
	row.Attempts, row.LastError = 0, ""
	if st.final() && row.AnnotationsSent >= len(out.Annotations) {
		row.FinishedAt = ptr.Of(now)
	} else {
		row.NextSyncAt = now.Add(pollLive)
	}
	row.UpdatedAt = now
	return s.store.SaveWorkflowCheckRunSync(ctx, row)
}

// leaseEnded ends a sync stopped for its lease. What landed on GitHub before
// it stopped (push records each write as it lands) is saved, fenced: it
// lands only if no other syncer took the row meanwhile, and then no other
// syncer wrote either. The row is left due at once, with no attempt counted:
// the stop was this syncer's, not GitHub's.
func (s *service) leaseEnded(ctx context.Context, l *rowLease, row *core.WorkflowCheckRun, now time.Time, cause error) error {
	stopped := cause
	if !errors.Is(cause, core.ErrWorkflowCheckLeaseLost) {
		stopped = fmt.Errorf("%w (stopped: %v)", context.Cause(l.ctx), cause)
	}
	row.NextSyncAt, row.UpdatedAt = now, now
	if err := s.store.SaveWorkflowCheckRunSync(ctx, row); err != nil && !errors.Is(err, core.ErrWorkflowCheckLeaseLost) {
		return errors.Join(stopped, fmt.Errorf("save: %w", err))
	}
	return stopped
}

// failed records a failed sync: backoff, and past the check's timeout plus
// giveUpAfter, giving up.
//
// A refusal (ErrNotAuthorized: the owner left the org, lost the repository,
// or the activation cannot write as the App) abandons the check at once: the
// row is finished with the reason in last_error, and nothing more is written
// to GitHub, because every write would be made with the authority that was
// refused. A check already open there stays as it is, which a required check
// never counts as passing. A write GitHub rejected (ErrRejected: the
// repository, commit or check run is gone, or a 422 even without the
// details) is abandoned the same way: sending it again gets the same answer.
func (s *service) failed(ctx context.Context, row *core.WorkflowCheckRun, now time.Time, cause error) error {
	row.Attempts++
	row.LastError = truncate(cause.Error(), 2048)
	wait := time.Duration(1<<min(row.Attempts, 10)) * 5 * time.Second
	if wait > maxBackoff {
		wait = maxBackoff
	}
	row.NextSyncAt = now.Add(wait)
	refused := errors.Is(cause, ErrNotAuthorized) || errors.Is(cause, ErrRejected)
	switch {
	case errors.Is(cause, ErrNotAuthorized):
		row.LastError = truncate("abandoned, not authorized: "+cause.Error(), 2048)
		row.FinishedAt = ptr.Of(now)
		logging.WarnContext(ctx, "[workflowcheck] abandoned a check its activation may no longer write", "check_id", row.ID, "name", row.Name, "reason", cause)
	case errors.Is(cause, ErrRejected):
		row.LastError = truncate("abandoned, GitHub rejected it: "+cause.Error(), 2048)
		row.FinishedAt = ptr.Of(now)
		logging.WarnContext(ctx, "[workflowcheck] abandoned a check GitHub rejected", "check_id", row.ID, "name", row.Name, "reason", cause)
	case now.After(row.CreatedAt.Add(row.Timeout + giveUpAfter)):
		row.LastError = truncate("abandoned after retries: "+cause.Error(), 2048)
		row.FinishedAt = ptr.Of(now)
		logging.WarnContext(ctx, "[workflowcheck] gave up on a check that could not be written", "check_id", row.ID, "name", row.Name, "error", cause)
	}
	row.UpdatedAt = now
	if err := s.store.SaveWorkflowCheckRunSync(ctx, row); err != nil {
		if errors.Is(err, core.ErrWorkflowCheckLeaseLost) {
			// Another syncer holds the row now: neither this failure nor
			// the abandon is recorded over its state.
			return fmt.Errorf("%w (after: %v)", err, cause)
		}
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
func (s *service) push(l *rowLease, row *core.WorkflowCheckRun, st state, out Output, detailsURL string, now time.Time) error {
	ctx := l.ctx
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
	if err := l.mayCall(); err != nil {
		return err
	}
	gh, err := s.github.CheckRuns(ctx, Identity{
		UserID: row.UserID, OrgID: ptr.From(row.OrgID), ActingIdentity: row.ActingIdentity, Automation: row.Automation,
	}, row.Owner, row.Repo)
	if err != nil {
		return err
	}
	runs := leasedRuns{runs: gh, l: l}
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
				err := withoutRefusedDetails(w, func(w Write) error {
					var err error
					id, err = runs.Create(ctx, w)
					return err
				})
				if err != nil {
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
			err := withoutRefusedDetails(w, func(w Write) error { return runs.Update(ctx, *row.GitHubCheckRunID, w) })
			if err != nil {
				return fmt.Errorf("update: %w", err)
			}
			row.GitHubStatus, row.GitHubConclusion, row.GitHubDigest = status, conclusion, want
		}
	}
	for status == core.CheckStatusCompleted && row.AnnotationsSent < len(out.Annotations) {
		end := min(row.AnnotationsSent+AnnotationBatch, len(out.Annotations))
		page := Output{Title: out.Title, Summary: out.Summary, Text: out.Text, Annotations: out.Annotations[row.AnnotationsSent:end]}
		if err := runs.Update(ctx, *row.GitHubCheckRunID, Write{Output: &page}); err != nil {
			if !invalidWrite(err) {
				return fmt.Errorf("annotations %d-%d: %w", row.AnnotationsSent, end, err)
			}
			// GitHub refused something in this batch: the check is
			// already concluded, so the batch is skipped rather than the
			// rest held behind it.
			logging.WarnContext(ctx, "[workflowcheck] GitHub refused a batch of the check's annotations; skipped it", "check_id", row.ID, "from", row.AnnotationsSent, "to", end, "error", err)
		}
		row.AnnotationsSent = end
	}
	return nil
}

// withoutRefusedDetails makes a status write with do. When GitHub refuses it
// as invalid (422) and it carried details, it is sent once more with details
// that say so instead: the check still moves on, and concludes, rather than
// staying where it was because of what it reported.
func withoutRefusedDetails(w Write, do func(Write) error) error {
	err := do(w)
	if err == nil || w.Output == nil || !invalidWrite(err) {
		return err
	}
	w.Output = &Output{
		Title:   "GitHub refused this check's details",
		Summary: truncate(fmt.Sprintf("Reliant reported this check, but GitHub refused the details it carried (%v). The run's page has them.", err), maxTextBytes),
	}
	if err2 := do(w); err2 != nil {
		return fmt.Errorf("after GitHub refused the check's details (%v): %w", err, err2)
	}
	return nil
}

func (s *service) link(chatID string) string {
	if chatID == "" || s.runURL == nil {
		return ""
	}
	return s.runURL(chatID)
}

// heartbeat renews a pass's leases every renewEvery until the pass ends,
// and keeps each row's lease expiry as this syncer last knew it held: the
// claim's, then each successful renewal's. A row the renewal finds taken is
// lost. A row whose expiry passes with no renewal since may be taken by
// another syncer, so it is treated as lost too: fail closed.
type heartbeat struct {
	s      *service
	mu     sync.Mutex
	held   map[string]core.WorkflowCheckLease // claimed and not yet synced
	until  map[string]time.Time               // each held row's lease expiry
	lost   map[string]bool
	active *rowLease // the row being synced
	done   chan struct{}
	wg     sync.WaitGroup
}

func (s *service) startHeartbeat(ctx context.Context, rows []*core.WorkflowCheckRun) *heartbeat {
	hb := &heartbeat{
		s:     s,
		held:  make(map[string]core.WorkflowCheckLease, len(rows)),
		until: make(map[string]time.Time, len(rows)),
		lost:  map[string]bool{},
		done:  make(chan struct{}),
	}
	for _, r := range rows {
		hb.held[r.ID] = core.WorkflowCheckLease{ID: r.ID, Generation: r.LeaseGeneration}
		// The claim's expiry; a store that returned none is held for no
		// longer than the claim asked.
		until := s.now().UTC().Add(lease)
		if r.ClaimedUntil != nil && r.ClaimedUntil.Before(until) {
			until = *r.ClaimedUntil
		}
		hb.until[r.ID] = until
	}
	tick, stopTick := s.ticker(renewEvery)
	hb.wg.Add(1)
	go func() {
		defer hb.wg.Done()
		defer stopTick()
		for {
			select {
			case <-ctx.Done():
				return
			case <-hb.done:
				return
			case <-tick:
				s.renew(ctx, hb)
			}
		}
	}()
	return hb
}

// renew extends the leases the pass still holds. A renewal that fails moves
// no expiry: each row then stops writing at the expiry it had, unless a later
// renewal succeeds first. A lease the renewal does not return is lost now.
func (s *service) renew(ctx context.Context, hb *heartbeat) {
	hb.mu.Lock()
	leases := make([]core.WorkflowCheckLease, 0, len(hb.held))
	for _, l := range hb.held {
		leases = append(leases, l)
	}
	hb.mu.Unlock()
	if len(leases) == 0 {
		return
	}
	// The new expiry is measured from before the call, so this syncer never
	// believes it holds a row longer than the database does.
	until := s.now().UTC().Add(lease)
	renewed, err := s.store.RenewWorkflowCheckRunLeases(ctx, s.holder, leases, until)
	if s.renewed != nil {
		defer s.renewed(renewed, err)
	}
	if err != nil {
		logging.WarnContext(ctx, "[workflowcheck] could not renew check leases; each stops writing at its expiry unless a renewal succeeds first", "error", err)
		return
	}
	kept := make(map[string]bool, len(renewed))
	for _, id := range renewed {
		kept[id] = true
	}
	hb.mu.Lock()
	defer hb.mu.Unlock()
	for _, l := range leases {
		if _, still := hb.held[l.ID]; !still {
			continue
		}
		active := hb.active != nil && hb.active.id == l.ID
		if kept[l.ID] {
			if until.After(hb.until[l.ID]) {
				hb.until[l.ID] = until
				if active {
					hb.arm(hb.active, until)
				}
			}
			continue
		}
		hb.lost[l.ID] = true
		delete(hb.held, l.ID)
		if active {
			hb.active.cancel(fmt.Errorf("%w: another syncer holds it now", core.ErrWorkflowCheckLeaseLost))
		}
	}
}

// begin starts row id's sync under its lease, or returns nil when the lease
// is lost or too near its expiry to start: either way the row is left to
// the next claim.
func (hb *heartbeat) begin(ctx context.Context, id string) *rowLease {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	until := hb.until[id]
	if _, ok := hb.held[id]; !ok || hb.lost[id] || !hb.s.now().Before(until.Add(-writeMargin)) {
		return nil
	}
	rowCtx, cancel := context.WithCancelCause(ctx)
	l := &rowLease{hb: hb, id: id, ctx: rowCtx, cancel: cancel}
	hb.active = l
	hb.arm(l, until)
	return l
}

// arm cancels l at until, unless it is armed again (a renewal) or done
// first. hb.mu is held.
func (hb *heartbeat) arm(l *rowLease, until time.Time) {
	if l.stopTimer != nil {
		l.stopTimer()
	}
	l.arms++
	arm := l.arms
	l.stopTimer = hb.s.afterFunc(until.Sub(hb.s.now()), func() {
		hb.mu.Lock()
		current := hb.active == l && l.arms == arm
		hb.mu.Unlock()
		if current {
			l.cancel(fmt.Errorf("%w: it expired at %s with no renewal since", core.ErrWorkflowCheckLeaseLost, until.Format(time.RFC3339)))
		}
	})
}

func (hb *heartbeat) stop() {
	close(hb.done)
	hb.wg.Wait()
}

// rowLease is one row's sync under its lease: the context its reads and
// GitHub calls run in, cancelled with ErrWorkflowCheckLeaseLost as its cause
// when the lease is lost or expires with no renewal.
type rowLease struct {
	hb     *heartbeat
	id     string
	ctx    context.Context
	cancel context.CancelCauseFunc
	// Guarded by hb.mu.
	stopTimer func() bool
	arms      int
}

// mayCall is asked before every GitHub call: the context must be live, and
// the lease must not be within writeMargin of its expiry. Too near, the row
// is stopped (fail closed) and nothing more is called for it.
func (l *rowLease) mayCall() error {
	if l.ctx.Err() != nil {
		return context.Cause(l.ctx)
	}
	l.hb.mu.Lock()
	until := l.hb.until[l.id]
	l.hb.mu.Unlock()
	if !l.hb.s.now().Before(until.Add(-writeMargin)) {
		l.cancel(fmt.Errorf("%w: it expires at %s with no renewal since, too soon to start a GitHub call", core.ErrWorkflowCheckLeaseLost, until.Format(time.RFC3339)))
		return context.Cause(l.ctx)
	}
	return nil
}

// stopped reports whether the row was stopped for its lease.
func (l *rowLease) stopped() bool {
	return errors.Is(context.Cause(l.ctx), core.ErrWorkflowCheckLeaseLost)
}

// done ends the row's sync: its expiry timer stops and its lease is no longer
// renewed (the save released it).
func (l *rowLease) done() {
	hb := l.hb
	hb.mu.Lock()
	if hb.active == l {
		hb.active = nil
	}
	if l.stopTimer != nil {
		l.stopTimer()
	}
	delete(hb.held, l.id)
	delete(hb.until, l.id)
	hb.mu.Unlock()
	l.cancel(nil)
}

// leasedRuns asks the row's lease before every call to GitHub.
type leasedRuns struct {
	runs CheckRuns
	l    *rowLease
}

func (r leasedRuns) Find(ctx context.Context, headSHA, name, externalID string) (int64, error) {
	if err := r.l.mayCall(); err != nil {
		return 0, err
	}
	return r.runs.Find(ctx, headSHA, name, externalID)
}

func (r leasedRuns) Create(ctx context.Context, w Write) (int64, error) {
	if err := r.l.mayCall(); err != nil {
		return 0, err
	}
	return r.runs.Create(ctx, w)
}

func (r leasedRuns) Update(ctx context.Context, id int64, w Write) error {
	if err := r.l.mayCall(); err != nil {
		return err
	}
	return r.runs.Update(ctx, id, w)
}
