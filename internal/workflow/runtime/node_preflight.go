// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/machinepreflight"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// A node that sets `preflight:` (Node.preflight) has the machine it routes
// to check itself before its work starts (internal/machinepreflight, the
// daemon's machine.preflight). A machine that fails is never given the node:
//
//   - a node on a machine pool — its own `daemon: {pool:}`, or the pool the
//     run (or an enclosing loop or workflow node) was placed on — is placed
//     on another member, the failed ones excluded for this node, and checked
//     there. Its own placement moves (machinepool.PlaceRunInput.Exclude); a
//     node that inherited its machine gets a placement of its own, at its
//     path, and only it (and, for a loop or workflow node, its body) moves;
//   - a node on any other machine has nowhere to go, and fails at once.
//
// The check is its own activity before the node's, so a failed machine
// spends none of the node's attempts: its activity is dispatched once, on the
// machine that passed. The machines a node may try are bounded — MaxMachines
// (default 3), and every member of a smaller pool — after which the node
// fails with NoHealthyMachinePrefix and every machine's reason.
//
// What the node routes to after a move is kept on the run's placements
// (runPlacements.preflight), keyed like a node placement, so every later
// start of the node — a loop's next iteration — starts on the machine that
// passed, and checks it again.

// MachinePreflightActivityName is the node's machine self-check
// (handlers.MachinePreflightActivity).
const MachinePreflightActivityName = "MachinePreflight"

// nodePreflightChangeID marks a run that checks a node's machine. It is asked
// for only by a node that sets preflight, which no run recorded before the
// field existed has.
const nodePreflightChangeID = "node-preflight"

// NoHealthyMachinePrefix starts the error of a node no machine could take:
// every machine it tried failed its preflight. A failed preflight is never
// one of the node's attempts; this is the node's one failure, and a caller
// counting attempts (pipeline_attempts) reads the prefix to tell it apart.
const NoHealthyMachinePrefix = "no healthy machine"

// machinePreflightTimeout bounds one check: a daemon round trip over a
// handful of stat calls.
const machinePreflightTimeout = time.Minute

// MachinePreflightInput is MachinePreflightActivityName's input.
type MachinePreflightInput struct {
	ChatID     string `json:"chat_id"`
	WorkflowID string `json:"workflow_id"`
	// NodePath is the node's path, for the record of a failure.
	NodePath string `json:"node_path"`
	// DaemonSelector is the machine to check (the node's selector).
	DaemonSelector map[string]interface{} `json:"daemon_selector,omitempty"`
	// Pool is the pool the machine was placed from, "" when none.
	Pool string                `json:"pool,omitempty"`
	Spec machinepreflight.Spec `json:"spec"`
}

// MachinePreflightOutput is the check's verdict. Zero is a machine that
// passed (and what a scenario's stub answers).
type MachinePreflightOutput struct {
	// Failed: the machine is not healthy for the node.
	Failed bool `json:"failed,omitempty"`
	// Reason is the first failed check (machinepreflight.Reason); Message
	// every failed check, for a person.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// DaemonID is the machine that was checked.
	DaemonID   string `json:"daemon_id,omitempty"`
	DaemonName string `json:"daemon_name,omitempty"`
}

// nodePreflightEntry is one node's preflight state in a run.
type nodePreflightEntry struct {
	// excluded is every machine the node failed its preflight on, in order.
	excluded []string
	// reasons is what each excluded machine failed with.
	reasons []string
	// moved is the machine the node routes to after a move; nil until one.
	moved *DaemonSelectorValue
}

// preflightState is (workflowID, scope)'s preflight state, created on first
// use. A run without placements (a bare test context) keeps it for one
// start only.
func (r *runPlacements) preflightState(workflowID, scope string) *nodePreflightEntry {
	if r == nil {
		return &nodePreflightEntry{}
	}
	if r.preflights == nil {
		r.preflights = map[string]*nodePreflightEntry{}
	}
	key := placementKey(workflowID, scope)
	entry, ok := r.preflights[key]
	if !ok {
		entry = &nodePreflightEntry{}
		r.preflights[key] = entry
	}
	return entry
}

// movedSelector is the machine a preflight moved (workflowID, scope) to.
func (r *runPlacements) movedSelector(workflowID, scope string) (*DaemonSelectorValue, bool) {
	if r == nil || r.preflights == nil {
		return nil, false
	}
	entry, ok := r.preflights[placementKey(workflowID, scope)]
	if !ok || entry.moved == nil {
		return nil, false
	}
	return entry.moved, true
}

// preflightPool is the pool a node may move to when its preflight fails
// (review B1 on #3790): its own selector's pool, else the pool its machine
// was placed from. A routing value can lose PlacedPool (it is in-memory
// only, and an inherited selector may arrive serialized, or come from a
// placement recorded before Pool was kept), so the run's own workflow-level
// placement is read too. "" when the node is not on a pool at all.
func preflightPool(r *runPlacements, workflowID string, own, sel *DaemonSelectorValue) string {
	switch {
	case own.NeedsPlacement():
		return own.Pool
	case sel != nil && sel.Placed && sel.PlacedPool != "":
		return sel.PlacedPool
	case sel.NeedsPlacement():
		return sel.Pool
	}
	if own == nil {
		if entry, ok := r.lookup(workflowID, ""); ok && entry.placement != nil {
			if sel == nil || sel.ID == "" || sel.ID == entry.placement.DaemonID {
				return entry.placement.Pool
			}
		}
	}
	return ""
}

// preflightNode checks the machine sel routes node to, and returns the
// machine the node runs on: sel when it passed, another member of its pool
// when it did not and one passed there. A node that sets no preflight gets
// sel back and nothing is scheduled. own is the node's own selector (nil when
// it inherits sel); its pool, else sel's, is where the node may move.
func preflightNode(ctx workflow.Context, chatID, workflowID, scope string, node *reliantv1.Node, own, sel *DaemonSelectorValue) (*DaemonSelectorValue, error) {
	cfg, ok, err := machinepreflight.FromProto(node.GetPreflight())
	if !ok {
		return sel, nil
	}
	if err != nil {
		return nil, fmt.Errorf("node %s: preflight: %w", scope, err)
	}
	if workflow.GetVersion(ctx, nodePreflightChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return sel, nil
	}
	state := runPlacementsFrom(ctx).preflightState(workflowID, scope)
	if state.moved != nil {
		sel = state.moved
	}
	pool := preflightPool(runPlacementsFrom(ctx), workflowID, own, sel)
	logger := workflow.GetLogger(ctx)
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: machinePreflightTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    infrastructureActivityMaxAttempts,
		},
	})

	for {
		in := MachinePreflightInput{ChatID: chatID, WorkflowID: workflowID, NodePath: scope, Pool: pool, Spec: cfg.Spec}
		if sel != nil {
			in.DaemonSelector = selectorInput(sel)
		}
		var out MachinePreflightOutput
		if err := workflow.ExecuteActivity(actCtx, MachinePreflightActivityName, in).Get(ctx, &out); err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			// A check that could not run, its retries spent, is a machine
			// the node cannot be given either.
			out = MachinePreflightOutput{Failed: true, Reason: string(machinepreflight.ReasonUnreachable),
				Message: "the machine could not be checked: " + preflightFailureMessage(err)}
		}
		if !out.Failed {
			return sel, nil
		}
		id := out.DaemonID
		if id == "" && sel != nil {
			id = sel.ID
		}
		label := out.DaemonName
		if label == "" {
			label = id
		}
		if label == "" {
			label = "the run's machine"
		}
		state.reasons = append(state.reasons, fmt.Sprintf("%s: %s", label, out.Message))
		logger.Warn("[Workflow Runtime] A node's machine failed its preflight",
			"scope", scope, "pool", pool, "daemonID", id, "reason", out.Reason, "message", out.Message)

		switch {
		case pool == "":
			return nil, noHealthyMachine(scope, state.reasons, "the node is not on a machine pool, so no other machine can take it")
		case id == "":
			return nil, noHealthyMachine(scope, state.reasons, "the failed machine could not be named, so it cannot be left out of the next placement")
		}
		if !containsString(state.excluded, id) {
			state.excluded = append(state.excluded, id)
		}
		if len(state.excluded) >= cfg.MaxMachines {
			return nil, noHealthyMachine(scope, state.reasons, fmt.Sprintf("it tried %d machines (preflight.max_machines)", len(state.excluded)))
		}
		placement, err := askPlacement(ctx, chatID, workflowID, scope, &DaemonSelectorValue{Pool: pool}, append([]string(nil), state.excluded...))
		if err != nil {
			if exhausted, ok := err.(errPoolExhausted); ok { //nolint:errorlint // askPlacement returns it unwrapped
				return nil, noHealthyMachine(scope, state.reasons, exhausted.reason)
			}
			return nil, err
		}
		next := placement.selector()
		if next == nil || next.ID == "" || containsString(state.excluded, next.ID) {
			return nil, noHealthyMachine(scope, state.reasons, "no other member of pool "+pool+" was placed")
		}
		logger.Info("[Workflow Runtime] Moved a node off a machine that failed its preflight",
			"scope", scope, "pool", pool, "from", id, "to", next.ID)
		state.moved = next
		sel = next
	}
}

// noHealthyMachine is the error a node fails with when no machine passed its
// preflight: every machine's reason, then why it stopped looking.
func noHealthyMachine(scope string, reasons []string, why string) error {
	return fmt.Errorf("%s for node %s: %s; %s", NoHealthyMachinePrefix, scope, strings.Join(reasons, "; "), why)
}

// selectorInput is a selector as an activity input map (runDaemonSelector's
// shape).
func selectorInput(ds *DaemonSelectorValue) map[string]interface{} {
	sel := map[string]interface{}{"id": ds.ID, "name": ds.Name, "type": ds.Type, "labels": ds.Labels}
	if ds.Session {
		sel["session"] = true
	}
	return sel
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// preflightStructural is preflightNode for a structural node — a loop or a
// `workflow:` node (a review node is one) — whose body runs under base: it
// checks the machine base routes to, and when the node moves, returns a copy
// of base routed to the machine that passed.
func preflightStructural(ctx workflow.Context, chatID, workflowID, scope string, node *reliantv1.Node, own *DaemonSelectorValue, base *ExecutionContext) (*ExecutionContext, error) {
	if node.GetPreflight() == nil {
		return base, nil
	}
	var sel *DaemonSelectorValue
	if base != nil {
		sel = base.DaemonSelector
	}
	got, err := preflightNode(ctx, chatID, workflowID, scope, node, own, sel)
	if err != nil {
		return nil, err
	}
	if got == sel {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("could not route %s to the machine that passed its preflight: the node has no execution context", scope)
	}
	moved := *base
	moved.DaemonSelector = got
	return &moved, nil
}
