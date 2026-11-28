package trigger

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// ListTriggers returns the caller's triggers.
func (s *Service) ListTriggers(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTriggersRequest],
) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// The filter carries the caller's id, so this can never return another
	// user's triggers regardless of what project id was asked for.
	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	filters := core.TriggerFilters{UserID: userID, ProjectID: req.Msg.ProjectId, Org: scope}
	stored, err := s.deps.Triggers.ListTriggersForUser(ctx, filters)
	if err != nil {
		return nil, triggerDBError("list triggers", err)
	}
	// A trigger of an org the caller has left is not listed, named org or
	// not: the list shows what GetTrigger would.
	stored = authz.Visible(ctx, userID, stored, authz.CanReadTrigger)

	// One query for every trigger's recent firings: health and last_event are
	// computed from it, so the list costs a constant number of queries.
	ids := make([]string, len(stored))
	for i, t := range stored {
		ids[i] = t.ID
	}
	// Every activation resolves its workflow, that workflow's children and
	// presets, and where it reads them from; most of them against the same
	// machine catalog and the same account drafts. Read each once for the
	// whole list (launch.NewRequestResolver), the drafts in one query.
	resolver := launch.NewRequestResolver(s.deps.Repo)

	// The firings, the source states, the drafts and the next fire times are
	// independent, so they are read together: one after another they cost
	// their sum (prod trace dfc1d42c…, 2026-11-27: 279ms of firings, then
	// 19ms of registrations, then 449ms of Temporal Describes, before the
	// first activation rendered). None fails the list.
	var (
		recent map[string][]*core.TriggerEventWithRun
		regs   map[string]*core.TriggerRegistration
		next   map[string]*time.Time
		wg     sync.WaitGroup
	)
	wg.Go(func() {
		var err error
		if recent, err = s.deps.Firings.RecentTriggerFirings(ctx, userID, ids, triggers.HealthWindow); err != nil {
			logging.WarnContext(ctx, "could not resolve triggers' recent firings", "error", err)
			recent = nil
		}
	})
	// And one for every polled trigger's source state, which health folds in.
	wg.Go(func() {
		var err error
		if regs, err = s.deps.Registrations.ListTriggerRegistrationsForUser(ctx, userID, ids); err != nil {
			logging.WarnContext(ctx, "could not resolve triggers' source state", "error", err)
			regs = nil
		}
	})
	wg.Go(func() { triggers.PrefetchActivationDrafts(ctx, resolver, stored, "trigger list") })
	wg.Go(func() { next = s.nextFireTimes(ctx, stored) })
	wg.Wait()

	workflows := triggers.NewCachedWorkflows(triggers.LaunchWorkflows{Repo: resolver})
	sources := newWorkflowDescriber(resolver)
	out := make([]*reliantv1.Trigger, 0, len(stored))
	for _, t := range stored {
		out = append(out, s.renderWithWorkflows(ctx, t, next[t.ID], recent[t.ID], regs[t.ID], workflows, sources))
	}
	return connect.NewResponse(&reliantv1.ListTriggersResponse{Triggers: out}), nil
}
