// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"go.temporal.io/sdk/activity"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// A `uses: reliant/<ns>.<verb>@<major>` node is a Reliant server action
// (internal/serveraction), not an integration action: it runs here on the
// worker against Reliant's own state, as the run's principal. It never
// resolves a connection or a secret, never makes an outbound call of its
// own, and never needs or wakes a machine (preflight and MachineRequirements
// count it as server-placed). Every one is refused while the server's
// pipeline.enabled switch is off.

// WithServerActions routes reliant/ action nodes to d. A nil dispatcher
// refuses every one of them as switched off.
func (a *ActionActivity) WithServerActions(d *serveraction.Dispatcher) *ActionActivity {
	clone := *a
	clone.server = d
	return &clone
}

// executeServer dispatches a server action and records its outcome as the
// node's ActionOutput. Only a context that ended is an activity error (the
// node is retried or cancelled, as any activity's); every other failure is
// an output the graph branches on.
func (a *ActionActivity) executeServer(ctx context.Context, input ActivityInput, uses string) (*reliantv1.ActionOutput, error) {
	args := input.Node.GetAction()
	params := make(map[string]any, len(args.GetWith()))
	for name, value := range args.GetWith() {
		params[name] = value.AsInterface()
	}
	rtx := input.Runtime
	call := serveraction.Call{
		Uses:       uses,
		Node:       input.Node,
		Params:     params,
		Invocation: serverActionInvocation(ctx, rtx),
		Run: serveraction.RunFacts{
			CatalogPin:     rtx.CatalogPin,
			ActingIdentity: rtx.ActingIdentity,
			Automation:     rtx.Automation,
		},
	}
	if f := rtx.ServerActionFrom; f != nil {
		call.From = &serveraction.NodeRecord{NodeID: f.NodeID, Type: f.Type, Role: f.Role, Review: f.Review, Ran: f.Ran, Output: f.Output}
	}
	if rtx.Telemetry != nil {
		call.Run.WorkflowRef = rtx.Telemetry.WorkflowRef
		call.Run.Trigger = rtx.Telemetry.Trigger
	}
	outcome := a.server.Dispatch(ctx, call)
	if outcome.Err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := &reliantv1.ActionOutput{Uses: uses}
	if outcome.Name != "" {
		out.Uses = outcome.Name
	}
	if outcome.Err != nil {
		out.IsError = true
		out.ErrorCode = outcome.Err.Code
		out.Content = outcome.Err.Message
		out.Retryable = outcome.Err.Retryable
		return out, nil
	}
	out.Content = outcome.Result.Content
	if outcome.Result.Data != nil {
		data, err := structpb.NewStruct(outcome.Result.Data)
		if err != nil {
			return failedAction(out.Uses, serveraction.CodeInternal, out.Uses+" returned an output that is not JSON"), nil
		}
		out.Data = data
	}
	return out, nil
}

// serverActionInvocation places the call in its run from the runtime's own
// record (RuntimeContext) and, inside an activity, the scheduled execution.
func serverActionInvocation(ctx context.Context, rtx RuntimeContext) serveraction.Invocation {
	nodePath := rtx.NodePath
	if nodePath == "" {
		nodePath = rtx.StepID
	}
	inv := serveraction.Invocation{
		RunID: rtx.WorkflowID, ChatID: rtx.ChatID, ThreadID: rtx.Thread,
		NodeID: rtx.StepID, NodePath: nodePath,
		LoopNodeID: rtx.LoopNodeID, LoopIteration: rtx.LoopIteration,
		PositionKey: invokeToolCallID(rtx.WorkflowID, nodePath, rtx.LoopNodeID, rtx.LoopIteration),
	}
	inv.ExecutionKey = inv.PositionKey
	if activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		inv.TemporalRunID = info.WorkflowExecution.RunID
		inv.ActivityID = info.ActivityID
		inv.Attempt = info.Attempt
		inv.ExecutionKey = inv.PositionKey + "/" + inv.TemporalRunID + "/" + inv.ActivityID
	}
	return inv
}

// serverActionRuns is the worker's serveraction.Resolver: it reads who a run
// acts for and where its workflow came from, the same way the run's own loads
// resolved them (LoadWorkflowActivity).
type serverActionRuns struct {
	store LoadWorkflowStore
	runs  WorkflowLoader
}

// NewServerActionResolver is the serveraction.Resolver over the run's
// records: store is the run's chat and pinned workflow catalog (*db.Repo),
// runs its workflows row. Both must be set.
func NewServerActionResolver(store LoadWorkflowStore, runs WorkflowLoader) serveraction.Resolver {
	return &serverActionRuns{store: store, runs: runs}
}

// Resolve reads the run's principal and its root workflow's provenance. A
// read that fails is an error; a record that is absent leaves its fields
// empty (an empty UserID is refused by the dispatcher, an unplaceable
// definition is SourceUnknown).
func (r *serverActionRuns) Resolve(ctx context.Context, inv serveraction.Invocation, run serveraction.RunFacts) (serveraction.Principal, serveraction.Provenance, error) {
	principal := serveraction.Principal{ActingIdentity: run.ActingIdentity, Automation: run.Automation}
	provenance := serveraction.Provenance{WorkflowRef: run.WorkflowRef, CatalogPin: run.CatalogPin, Trigger: run.Trigger}

	var chat *core.Chat
	if inv.ChatID != "" {
		c, err := chatviews.Of(r.store).GetChat(ctx, inv.ChatID)
		switch {
		case err != nil && !svcerr.IsNotFound(err):
			return principal, provenance, fmt.Errorf("read chat %s: %w", inv.ChatID, err)
		case err == nil:
			chat = c
		}
	}
	var row *core.Workflow
	if inv.RunID != "" {
		w, err := r.runs.LoadWorkflow(ctx, inv.RunID)
		switch {
		case err != nil && !svcerr.IsNotFound(err):
			return principal, provenance, fmt.Errorf("read run %s: %w", inv.RunID, err)
		case err == nil:
			row = w
		}
	}
	// The run's own record first, its chat's second: the order the rest of
	// the engine reads an owner and an org in (connections.Resolver.actorOf).
	if row != nil {
		principal.UserID = derefString(row.OwnerUserID)
		principal.OrgID = derefString(row.OrgID)
	}
	if chat != nil {
		if principal.UserID == "" {
			principal.UserID = chat.UserID
		}
		if principal.OrgID == "" {
			principal.OrgID = derefString(chat.OrgID)
		}
		principal.ProjectID = chat.ProjectID
	}

	if err := r.place(ctx, chat, &provenance); err != nil {
		return principal, provenance, err
	}
	return principal, provenance, nil
}

// place resolves where the run's root workflow came from. A builtin:// ref
// is definitive: nothing shadows it. A project or account workflow is placed
// only through the catalog the run pinned at its start, re-resolved with the
// run's own rule (its chat's precedence), so it names the definition the run
// executes; without a pin the live catalog may differ, and it stays unknown.
func (r *serverActionRuns) place(ctx context.Context, chat *core.Chat, p *serveraction.Provenance) error {
	ref, err := workflowref.Parse(p.WorkflowRef)
	if p.WorkflowRef == "" || err != nil {
		return nil
	}
	if ref.Kind == workflowref.Builtin {
		p.Source = serveraction.SourceBuiltin
		return nil
	}
	if p.CatalogPin == "" || chat == nil {
		return nil
	}
	wfCtx, err := (&LoadWorkflowActivity{repo: r.store}).resolveWorkflowContext(ctx, chat.ID)
	if err != nil {
		return err
	}
	wfCtx.catalogPin = p.CatalogPin
	described, err := workflowsource.Describe(ctx, r.store, wfCtx.sourceOptions(), p.WorkflowRef)
	if err != nil {
		var storeErr *workflowsource.StoreError
		if errors.As(err, &storeErr) {
			return err
		}
		return nil // the ref no longer resolves against the pin: unplaceable
	}
	switch described.Source {
	case workflowref.SourceUser:
		p.Source = serveraction.SourceUser
	case workflowref.SourceProject:
		p.Source = serveraction.SourceProject
		p.Path = described.Path
		pinned, found, err := r.store.GetPinnedProjectCatalog(ctx, p.CatalogPin)
		if err != nil {
			return fmt.Errorf("read pinned catalog %s: %w", p.CatalogPin, err)
		}
		if found {
			p.Repo = repoOfPath(pinned.RepoStates, described.Path)
		}
	case workflowref.SourceBuiltin:
		p.Source = serveraction.SourceBuiltin
	}
	return nil
}

// repoOfPath is the synced repo state a project workflow file belongs to: the
// nested repo whose path prefixes it (Entry.Location writes a nested repo's
// file project-relative), else the project root's.
func repoOfPath(states []config.SyncedRepoState, file string) *serveraction.RepoState {
	var best *config.SyncedRepoState
	for i := range states {
		s := &states[i]
		switch {
		case s.Repo == "":
			if best == nil {
				best = s
			}
		case strings.HasPrefix(path.Clean(file), path.Clean(s.Repo)+"/"):
			if best == nil || best.Repo == "" || len(s.Repo) > len(best.Repo) {
				best = s
			}
		}
	}
	if best == nil {
		return nil
	}
	return &serveraction.RepoState{Repo: best.Repo, Branch: best.Branch, Commit: best.Commit, Dirty: best.Dirty, DefaultBranch: best.DefaultBranch}
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
