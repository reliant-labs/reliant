// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepool"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// A `daemon: {pool: <name>}` selector does not route each call: it is PLACED
// once on one machine — the
// workflow-level one once per run, a node-level one once per node per run —
// and the run (or node) then routes to that machine as to any configured id
// pin. The placement is the PlaceRunOnMachinePool activity's
// (machinepool.PlaceRunActivityName): the pool's least-loaded eligible member,
// under the pool's slot guard, recorded so asking again returns the same one.

// daemonPoolPlacementChangeID gates placement. It is asked for only when a
// selector needs placement, so a run without one records no marker; a history
// recorded before placement existed reads DefaultVersion and keeps routing by
// the selector as written (a labels-only one as a per-call filter).
const daemonPoolPlacementChangeID = "daemon-pool-placement"

const (
	// placeRunStartToClose bounds one PlaceRunOnMachinePool attempt: a few
	// row-locked queries, never a wait (waiting is the workflow's, below).
	placeRunStartToClose = 30 * time.Second
	// placeRunWaitFirst and placeRunWaitMax bound the timer a run sleeps on
	// between asks while no member can take it, doubling from one to the
	// other. placeRunWaitMax stays under the reconciler's progress-stall
	// window (10m), as the machine wait's recheck does.
	placeRunWaitFirst = 15 * time.Second
	placeRunWaitMax   = 5 * time.Minute
	// placeRunWaitCap is how long a run waits for a member before it fails
	// with the last reason the pool gave.
	placeRunWaitCap = 24 * time.Hour
)

// RunPlacement is a placed selector's answer: the machine it was placed on.
// An empty DaemonID is a placement that pinned nothing (a scenario's stub),
// and the selector is then left unpinned.
type RunPlacement struct {
	DaemonID string `json:"daemon_id,omitempty"`
	// Pool names the pool it was placed on, so a node's preflight can move
	// a node routed here to another member (node_preflight.go). Empty for a
	// placement recorded before it was kept.
	Pool string `json:"pool,omitempty"`
}

// selector is the routing a placement gives: the placed machine as a
// configured id pin (not the chat's pin: it never yields to a workspace
// owner), or nil when the placement pinned nothing.
func (p *RunPlacement) selector() *DaemonSelectorValue {
	if p == nil || p.DaemonID == "" {
		return nil
	}
	return &DaemonSelectorValue{ID: p.DaemonID, Placed: true, PlacedPool: p.Pool}
}

// placeDaemonSelector places ds, a selector that NeedsPlacement, for the
// run's workflow at scope ("" for the workflow-level selector, the node's
// path for a node's). It returns nil, nil for a history recorded before
// placement existed: the caller routes by ds as written. Otherwise it asks
// until a member takes it, waiting on a timer while the pool says to wait,
// and fails once that has lasted placeRunWaitCap or the pool does not exist.
func placeDaemonSelector(ctx workflow.Context, chatID, workflowID, scope string, ds *DaemonSelectorValue) (*RunPlacement, error) {
	if workflow.GetVersion(ctx, daemonPoolPlacementChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil, nil
	}
	return askPlacement(ctx, chatID, workflowID, scope, ds, nil)
}

// errPoolExhausted is a placement whose pool has members and every one is
// excluded (machinepool.PlaceRunOutput.Exhausted): a node's preflight failed
// on all of them.
type errPoolExhausted struct{ reason string }

func (e errPoolExhausted) Error() string { return e.reason }

// askPlacement asks PlaceRunOnMachinePool for (workflowID, scope) until a
// member takes it, excluding the members in exclude, waiting on a timer while
// the pool says to wait. An exhausted pool is errPoolExhausted.
func askPlacement(ctx workflow.Context, chatID, workflowID, scope string, ds *DaemonSelectorValue, exclude []string) (*RunPlacement, error) {
	logger := workflow.GetLogger(ctx)
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: placeRunStartToClose,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        2 * time.Second,
			MaximumAttempts:        infrastructureActivityMaxAttempts,
			NonRetryableErrorTypes: []string{machinepool.PoolNotFoundErrorType},
		},
	})
	in := machinepool.PlaceRunInput{
		ChatID:     chatID,
		WorkflowID: workflowID,
		Scope:      scope,
		Pool:       ds.Pool,
		Exclude:    exclude,
	}

	start := workflow.Now(ctx)
	wait := placeRunWaitFirst
	for {
		var out machinepool.PlaceRunOutput
		if err := workflow.ExecuteActivity(actCtx, machinepool.PlaceRunActivityName, in).Get(ctx, &out); err != nil {
			return nil, err
		}
		if out.DaemonID != "" {
			logger.Info("[Workflow Runtime] Placed on a machine",
				"scope", scope, "pool", out.PoolName, "daemonID", out.DaemonID, "daemonName", out.DaemonName)
			return &RunPlacement{DaemonID: out.DaemonID, Pool: ds.Pool}, nil
		}
		if out.Exhausted {
			return nil, errPoolExhausted{reason: out.Reason}
		}
		if !out.Waiting {
			logger.Info("[Workflow Runtime] Placement pinned no machine; leaving the selector unpinned", "scope", scope)
			return &RunPlacement{}, nil
		}
		if workflow.Now(ctx).Sub(start) >= placeRunWaitCap {
			return nil, fmt.Errorf("no machine could take this run within %s: %s", placeRunWaitCap, out.Reason)
		}
		logger.Info("[Workflow Runtime] Waiting for a machine to place on",
			"scope", scope, "pool", ds.Pool, "reason", out.Reason, "recheck", wait)
		if err := workflow.Sleep(ctx, wait); err != nil {
			return nil, err
		}
		wait = min(wait*2, placeRunWaitMax)
	}
}

// placeWorkflowDaemon places the run's workflow-level selector when it needs
// placement and returns what the run routes by: the placed machine, ds as
// written (it needs none, or the history predates placement), or nil. The
// placement is recorded on execCtx, so a continue-as-new successor reuses it
// instead of asking again.
func placeWorkflowDaemon(ctx workflow.Context, execCtx *ExecutionContext, chatID, workflowID string, ds *DaemonSelectorValue) (*DaemonSelectorValue, error) {
	if !ds.NeedsPlacement() {
		return ds, nil
	}
	if execCtx.WorkflowPlacement != nil {
		return execCtx.WorkflowPlacement.selector(), nil
	}
	placement, err := placeDaemonSelector(ctx, chatID, workflowID, "", ds)
	if err != nil {
		return nil, err
	}
	if placement == nil {
		return ds, nil
	}
	execCtx.WorkflowPlacement = placement
	return placement.selector(), nil
}

// resolveNodeDaemonSelector is node's own daemon selector, its expression
// evaluated in scope (built only when it is one), or nil when the node
// declares none or the expression resolves to nothing.
func resolveNodeDaemonSelector(node *reliantv1.Node, scope func() *wfcel.NodeResolutionContext) *DaemonSelectorValue {
	cds := node.GetDaemon()
	if cds == nil {
		return nil
	}
	if lit := cds.GetLiteral(); lit != nil {
		return ResolveDaemonSelectorProto(lit)
	}
	if cds.GetExpr() == "" {
		return nil
	}
	ds, err := ResolveCelDaemonSelector(cds, scope())
	if err != nil {
		logging.Warn("[Workflow Runtime] failed to resolve node-level daemon CEL expression", "stepID", node.GetId(), "error", err)
		return nil
	}
	return ds
}

// placeStructuralDaemon is placement for a structural node — a loop, a
// `workflow:` node, or the workflow a router dispatches — which runs no
// activity of its own and so never reaches StepExecutor.Start, where a step's
// own selector is placed. When the node's selector names a pool, it is placed
// once per run at scope (the node's path, as a step's), and the node's body
// runs under a copy of base routed to the placed machine: every body step
// that declares no selector of its own routes there, and one that does still
// wins. Any other selector leaves base as it is. A placement that pinned
// nothing (a scenario's stub) also leaves base as it is.
func placeStructuralDaemon(ctx workflow.Context, chatID, workflowID, scope string, node *reliantv1.Node, celScope func() *wfcel.NodeResolutionContext, base *ExecutionContext) (*ExecutionContext, error) {
	ds := resolveNodeDaemonSelector(node, celScope)
	if !ds.NeedsPlacement() {
		return base, nil
	}
	placement, err := runPlacementsFrom(ctx).place(ctx, chatID, workflowID, scope, ds)
	if err != nil {
		return nil, fmt.Errorf("could not place %s on a machine: %s", scope, preflightFailureMessage(err))
	}
	sel := placement.selector()
	if sel == nil {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("could not route %s to its placed machine: the node has no execution context", scope)
	}
	// A copy, not Clone: Clone drops fields (RouterDecision, SpawnDepth,
	// ParentPermission) the body still needs, and base is shared with the
	// node's parent, which must keep routing as before.
	placed := *base
	placed.DaemonSelector = sel
	return &placed, nil
}

// runPlacements holds the run's node-level placements, keyed by workflow id
// and node path, so every iteration of a loop — and every reader of the
// node's selector — routes to the machine the node's first start placed it
// on. One per DynamicWorkflow execution, carried on its workflow.Context
// (not a command, so safe for every history); inline sub-workflows and
// spawned agents run on the same context and key by their own workflow id.
type runPlacements struct {
	byKey map[string]*placementEntry
	// preflights is each node's preflight state (node_preflight.go): the
	// machines it failed on and the one it moved to. Nil until a node with
	// a preflight starts.
	preflights map[string]*nodePreflightEntry
}

// placementEntry is one node's placement: done is set once it is decided, so
// a parallel iteration that starts the node meanwhile waits for it rather
// than asking again. placement nil means the history predates placement.
type placementEntry struct {
	done      workflow.Future
	placement *RunPlacement
	err       error
}

type runPlacementsKey struct{}

func withRunPlacements(ctx workflow.Context) workflow.Context {
	return workflow.WithValue(ctx, runPlacementsKey{}, &runPlacements{byKey: map[string]*placementEntry{}})
}

func runPlacementsFrom(ctx workflow.Context) *runPlacements {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(runPlacementsKey{}).(*runPlacements)
	return p
}

func placementKey(workflowID, scope string) string {
	return workflowID + "\x00" + scope
}

// lookup returns a decided placement for (workflowID, scope).
func (r *runPlacements) lookup(workflowID, scope string) (*placementEntry, bool) {
	if r == nil {
		return nil, false
	}
	entry, ok := r.byKey[placementKey(workflowID, scope)]
	if !ok || !entry.done.IsReady() {
		return nil, false
	}
	return entry, true
}

// place returns the node's placement, deciding it on first use. A failed
// placement is not kept: the step it failed fails, and a later start asks
// again.
func (r *runPlacements) place(ctx workflow.Context, chatID, workflowID, scope string, ds *DaemonSelectorValue) (*RunPlacement, error) {
	if r == nil {
		return placeDaemonSelector(ctx, chatID, workflowID, scope, ds)
	}
	key := placementKey(workflowID, scope)
	if entry, ok := r.byKey[key]; ok {
		_ = entry.done.Get(ctx, nil)
		return entry.placement, entry.err
	}
	done, settle := workflow.NewFuture(ctx)
	entry := &placementEntry{done: done}
	r.byKey[key] = entry
	entry.placement, entry.err = placeDaemonSelector(ctx, chatID, workflowID, scope, ds)
	if entry.err != nil {
		delete(r.byKey, key)
	}
	settle.SetValue(nil)
	return entry.placement, entry.err
}
