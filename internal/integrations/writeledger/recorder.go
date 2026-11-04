// Copyright (c) 2025 Reliant Labs
package writeledger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/observe"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// settleTimeout bounds completing a row after the call returned. The caller's
// context may already be cancelled (the call timed out); the row still has to
// be settled, because an intent that stays "intent" forever looks pending.
const settleTimeout = 10 * time.Second

// Recorder is the Service: it builds the decorated runner.
type Recorder struct {
	store Store
	runs  RunLookup
	now   func() time.Time
}

// New builds the write ledger.
func New(deps Deps) Service {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Recorder{store: deps.Store, runs: deps.Runs, now: now}
}

// Wrap returns inner with every mutating call recorded.
// A nil Recorder wraps nothing: inner is returned as it is.
func (r *Recorder) Wrap(inner Runner) Runner {
	if r == nil {
		return inner
	}
	return &recorded{rec: r, inner: inner}
}

type recorded struct {
	rec   *Recorder
	inner Runner
}

// RunAuthenticated records the intent, makes the call, and settles the row.
//
// A mutating call whose intent cannot be recorded is NOT made: the origin of its
// echo could never be told apart from a stranger's, and an unattributable write
// is how two workflows loop forever. The refusal is a retryable error result,
// and nothing was sent, so retrying is safe.
func (d *recorded) RunAuthenticated(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error) {
	if !a.GetMutates() {
		return d.inner.RunAuthenticated(ctx, m, a, params, src, site)
	}
	r := d.rec
	p := planFor(m.GetId(), a.GetId(), params)
	actor := core.IntegrationActorOwner
	if site.ActingIdentity == httpaction.IdentityApp {
		actor = core.IntegrationActorApp
	}
	w := &core.IntegrationWrite{
		ID:          uuid.NewString(),
		NodeID:      site.NodeID,
		ToolCallID:  site.ToolCallID,
		Integration: m.GetId(),
		Action:      a.GetId(),
		Actor:       actor,
		Scope:       p.scope,
		Target:      p.target,
		Correlation: p.correlation.Map(),
		Status:      core.IntegrationWriteIntent,
		CreatedAt:   r.now().UTC(),
	}
	r.attributeRun(ctx, w, site.RunID)
	if err := r.store.CreateIntegrationWrite(ctx, w); err != nil {
		slog.WarnContext(ctx, "integration write ledger: could not record intent; the call was not made",
			"integration", w.Integration, "action", w.Action, "run_id", site.RunID, "error", err)
		return &httpaction.Result{
			Content:   "Reliant could not record this write before sending it, so nothing was sent. Retry shortly. (" + err.Error() + ")",
			IsError:   true,
			Retryable: true,
		}, nil
	}

	res, err := d.inner.RunAuthenticated(ctx, m, a, params, src, site)
	r.settle(ctx, w, p, res, err)
	return res, err
}

// settle completes the intent row from the call's outcome. A failure to settle
// is logged and nothing else: the call already happened, and an intent row left
// open still matches its echo by target and content.
func (r *Recorder) settle(ctx context.Context, w *core.IntegrationWrite, p plan, res *httpaction.Result, callErr error) {
	out := core.IntegrationWriteResult{At: r.now().UTC()}
	switch {
	case callErr != nil:
		out.Status = core.IntegrationWriteFailed
		out.Error = callErr.Error()
		// A credential or validation refusal never reached the provider; any
		// other error may have (a timeout after the request was written).
		var ce *httpaction.CredentialError
		out.Ambiguous = !errors.As(callErr, &ce)
	case res == nil:
		out.Status = core.IntegrationWriteFailed
		out.Error = "the call returned no result"
		out.Ambiguous = true
	case res.IsError:
		out.Status = core.IntegrationWriteFailed
		out.Error = res.Content
		out.ConnectionID = res.ConnectionID
		// A 429 or a 4xx changed nothing. A 5xx or a status that never arrived
		// may have.
		out.Ambiguous = res.StatusCode == 0 || res.StatusCode >= 500
	default:
		out.Status = core.IntegrationWriteDone
		out.ConnectionID = res.ConnectionID
		out.Target, out.ObjectID, out.ObjectURL = p.completion(res.Data)
	}
	out.Error = truncate(out.Error, 1000)

	ctx, cancel := settleContext(ctx)
	defer cancel()
	if _, err := r.store.CompleteIntegrationWrite(ctx, w.ID, out); err != nil {
		slog.WarnContext(ctx, "integration write ledger: could not settle a write; its intent row stays open",
			"write_id", w.ID, "integration", w.Integration, "action", w.Action, "error", err)
	}
}

// settleContext is the context a row is settled under: the caller's values, but
// not its cancellation, bounded by settleTimeout.
func settleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
}

// attributeRun fills in the run a call belongs to. Every lookup is best effort:
// the call's own authentication already resolved the owner, and an unresolved
// field only makes the echo less attributable, never the call less correct.
func (r *Recorder) attributeRun(ctx context.Context, w *core.IntegrationWrite, runID string) {
	if r.runs == nil || runID == "" {
		return
	}
	chatID := runID
	if wf, err := r.runs.GetWorkflow(ctx, runID); err == nil && wf != nil {
		if wf.ChatID != "" {
			chatID = wf.ChatID
		}
		if wf.OwnerUserID != nil {
			w.UserID = *wf.OwnerUserID
		}
	}
	chat, err := r.runs.GetChat(ctx, chatID)
	if err != nil || chat == nil {
		return
	}
	w.ChatID = chat.ID
	if w.UserID == "" {
		w.UserID = chat.UserID
	}
	if chat.WorkflowName != nil {
		w.WorkflowName = *chat.WorkflowName
	}
	ev, err := r.runs.GetTriggerEventByChatID(ctx, chat.ID)
	if err != nil || ev == nil || ev.TriggerID == nil {
		return
	}
	w.TriggerID = *ev.TriggerID
	if t, err := r.runs.GetTrigger(ctx, *ev.TriggerID); err == nil && t != nil {
		w.TriggerName = triggerName(t)
	}
}

// triggerName is the name a workflow knows a trigger by: the declared trigger's
// name for an activation, the row's own name otherwise.
func triggerName(t *core.Trigger) string {
	if t.WorkflowTrigger != nil && strings.TrimSpace(*t.WorkflowTrigger) != "" {
		return strings.TrimSpace(*t.WorkflowTrigger)
	}
	return t.Name
}

// Prune deletes entries older than Retention. It reports how many.
func (r *Recorder) Prune(ctx context.Context, pruner Pruner) (int64, error) {
	n, err := pruner.PruneIntegrationWrites(ctx, r.now().Add(-Retention))
	if err != nil {
		return 0, fmt.Errorf("prune integration writes: %w", err)
	}
	return n, nil
}

// PruneLoop prunes hourly until ctx is done.
func (r *Recorder) PruneLoop(ctx context.Context, pruner Pruner) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		var n int64
		err := observe.RunRoot(ctx, "writeledger.prune", func(ctx context.Context) error {
			var err error
			n, err = r.Prune(ctx, pruner)
			return err
		})
		if err != nil {
			slog.WarnContext(ctx, "integration write ledger: prune failed", "error", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "integration write ledger: pruned", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
