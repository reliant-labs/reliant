// Copyright (c) 2025 Reliant Labs
package writeledger

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
)

// GitPushAction is the ledger action id of a branch push made by a git_publish
// node. It is not an integration action: the push goes over git's own protocol
// from the user's machine, so Recorder.Wrap never sees it.
const GitPushAction = "git.push"

// Push is a branch push about to be made.
type Push struct {
	// Repo is "owner/name".
	Repo string
	// Branch is the branch pushed to.
	Branch string
	// SHA is the commit that will be the branch's head. It is known before the
	// push, which is what lets the push event be matched to this write.
	SHA string
	// PullNumber, when non-zero, is the pull request whose head this is. Its
	// push also arrives as a pull_request.synchronize event.
	PullNumber int64
	// RunID is the run making the push; NodeID the node.
	RunID  string
	NodeID string
	// Actor is whose credential makes the push: core.IntegrationActorOwner or
	// core.IntegrationActorApp. Empty is the owner. This is the seam the acting
	// identity work plugs into: the activity passes the identity it resolved,
	// and the ledger records it so an event is attributed to the bot or the
	// person.
	Actor string
}

// PushIntent is the ledger rows of a push that has not finished.
type PushIntent struct {
	rec  *Recorder
	rows []*core.IntegrationWrite
}

// PushOutcome is how a push ended.
type PushOutcome struct {
	// Pushed is true when the branch was updated.
	Pushed bool
	// Ambiguous is true when the push may have applied although it reported
	// failure (the machine went away mid-push): its echo still counts.
	Ambiguous bool
	// Error says why a push that did not happen did not.
	Error string
}

// BeginPush records the intent to push, before the push is made, and returns
// the handle that settles it.
//
// Like Wrap, it refuses rather than let an unattributable write happen: if the
// ledger is configured and the intent cannot be recorded, the error is returned
// and nothing may be pushed. A nil Recorder (no ledger in this process) returns
// a nil intent, and a nil intent settles to nothing.
//
// Two rows are written for a pull request's head: the push event is a branch
// echo, the synchronize event a pull request echo, and the origin matcher
// compares a row with an echo of the same object kind.
func (r *Recorder) BeginPush(ctx context.Context, p Push) (*PushIntent, error) {
	if r == nil {
		return nil, nil
	}
	repo := strings.ToLower(strings.TrimSpace(p.Repo))
	if repo == "" || strings.TrimSpace(p.Branch) == "" || strings.TrimSpace(p.SHA) == "" {
		return nil, fmt.Errorf("record a push: repo, branch and sha are required")
	}
	scope := provenance.ScopeOf(repo)
	actor := core.IntegrationActorOwner
	if p.Actor == core.IntegrationActorApp {
		actor = core.IntegrationActorApp
	}
	base := func() *core.IntegrationWrite {
		w := &core.IntegrationWrite{
			ID:          uuid.NewString(),
			NodeID:      p.NodeID,
			Integration: "github",
			Action:      GitPushAction,
			Actor:       actor,
			Scope:       scope,
			Status:      core.IntegrationWriteIntent,
			CreatedAt:   r.now().UTC(),
		}
		r.attributeRun(ctx, w, p.RunID)
		return w
	}

	push := base()
	push.Target = provenance.BranchTargetOf(scope, p.Branch)
	push.ObjectID = p.SHA
	push.Correlation = provenance.Correlation{
		ObjectKind: provenance.KindBranch, Events: []string{"push"},
		Branch: p.Branch, HeadSHA: p.SHA,
	}.Map()
	rows := []*core.IntegrationWrite{push}

	if p.PullNumber > 0 {
		sync := base()
		sync.ObjectID = p.SHA
		sync.Target = provenance.TargetOf(scope, strconv.FormatInt(p.PullNumber, 10))
		sync.Correlation = provenance.Correlation{
			ObjectKind: provenance.KindPullRequest, Events: []string{"pull_request.synchronize"},
			Branch: p.Branch, HeadSHA: p.SHA,
		}.Map()
		rows = append(rows, sync)
	}

	for i, w := range rows {
		if err := r.store.CreateIntegrationWrite(ctx, w); err != nil {
			// Rows already written stay as intents; settle them failed so they
			// do not sit open claiming a push that never happened.
			(&PushIntent{rec: r, rows: rows[:i]}).Settle(ctx, PushOutcome{Error: "intent could not be recorded"})
			slog.Warn("integration write ledger: could not record a push intent; the push was not made",
				"repo", repo, "branch", p.Branch, "run_id", p.RunID, "error", err)
			return nil, fmt.Errorf("Reliant could not record this push before making it, so nothing was pushed: %w", err) //nolint:staticcheck // ST1005: shown to the user verbatim, as a sentence.
		}
	}
	return &PushIntent{rec: r, rows: rows}, nil
}

// Settle completes the intent from the push's outcome. A failure to settle is
// logged and nothing else: the push already happened or did not, and an intent
// left open still matches its echo by target and content.
func (i *PushIntent) Settle(ctx context.Context, out PushOutcome) {
	if i == nil {
		return
	}
	res := core.IntegrationWriteResult{At: i.rec.now().UTC()}
	switch {
	case out.Pushed:
		res.Status = core.IntegrationWriteDone
	default:
		res.Status = core.IntegrationWriteFailed
		res.Error = truncate(out.Error, 1000)
		res.Ambiguous = out.Ambiguous
	}
	ctx, cancel := settleContext(ctx)
	defer cancel()
	for _, w := range i.rows {
		// Completing a row replaces its object id with the result's, so the
		// sha the intent recorded is carried through: it is what an exact match
		// on the push event looks the row up by.
		res.ObjectID = w.ObjectID
		if _, err := i.rec.store.CompleteIntegrationWrite(ctx, w.ID, res); err != nil {
			slog.Warn("integration write ledger: could not settle a push; its intent row stays open",
				"write_id", w.ID, "error", err)
		}
	}
}

// IDs are the ledger row ids of the push, for the run's own audit output.
func (i *PushIntent) IDs() []string {
	if i == nil {
		return nil
	}
	ids := make([]string, len(i.rows))
	for n, w := range i.rows {
		ids[n] = w.ID
	}
	return ids
}
