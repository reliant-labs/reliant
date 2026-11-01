// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// list_machines and manage_machine act on the CALLER's own machines — what
// Settings -> Machines shows — through the control-plane rpcs the web uses,
// as the user. An agent's shell holds only its machine's own credential,
// which the control plane refuses for these rpcs ("missing owner in
// context"), so before these tools an agent could not start or resize the
// user's other machines at all.
//
// start and resize spend money. An interactive call raises an approval card
// (the activation approval flow: internal/workflow/runtime/
// activation_approval.go) and runs only once the user approves exactly that
// action, machine and size. stop saves money and runs without a card, but
// never on the machine this run executes on.
//
// An unattended run is handed manage_machine only when its step names it
// (unattended.go), and may then only start the machine its trigger
// activation targets, with that activation's delegated daemon:resume token.
// It can neither stop nor resize anything.

const (
	ToolListMachines  = "list_machines"
	ToolManageMachine = "manage_machine"
)

// MachineSummary is one of the user's machines as the agent sees it.
type MachineSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	State string `json:"state,omitempty"`
	Size  string `json:"size,omitempty"`
	// Labels are the machine's durable labels; Pools the owner's machine
	// pools whose selector they match (dev-docs/design/machine-pools.md).
	Labels map[string]string `json:"labels,omitempty"`
	Pools  []string          `json:"pools,omitempty"`
	// LiveRuns is, per pool, the live runs that pool placed on this machine.
	LiveRuns map[string]int `json:"pool_live_runs,omitempty"`
}

// MachineController reads and acts on the user's machines. internal/app
// implements it over the daemon registry, the daemon router and the control
// plane, none of which this package may import.
type MachineController interface {
	ListMachines(ctx context.Context, userID string) ([]MachineSummary, error)
	// StartMachine wakes daemonID as the signed-in user. Unattended, it may
	// instead present the delegated token bound to exactly daemonID.
	StartMachine(ctx context.Context, userID, daemonID string, unattended bool) error
	StopMachine(ctx context.Context, userID, daemonID string) error
	ResizeMachine(ctx context.Context, userID, daemonID, size string) error
	// ActivationMachine is the machine the stored trigger activation that
	// launched chatID targets, or "" when none launched it.
	ActivationMachine(ctx context.Context, chatID string) (string, error)
}

// machineSizes are the sizes resize accepts, smallest first.
var machineSizes = []string{"s1", "s2", "m", "l", "xl", "2xl", "3xl"}

// =============================================================================
// list_machines
// =============================================================================

type ListMachinesParams struct{}

const listMachinesDescription = `List the user's machines (Settings -> Machines): each one's id, name, type (managed or connected), state and size. The machine this run executes on is marked. Use the id with manage_machine to start, stop or resize one.`

type listMachinesTool struct {
	repo       runCallerStore
	controller MachineController
}

func NewListMachinesTool(repo runCallerStore, controller MachineController) Tool {
	return NewToolWrapper[ListMachinesParams, ToolResponse](&listMachinesTool{repo: repo, controller: controller})
}

func (l *listMachinesTool) Name() string        { return ToolListMachines }
func (l *listMachinesTool) Description() string { return listMachinesDescription }
func (l *listMachinesTool) RequiresPermission(ListMachinesParams) (bool, error) {
	return false, nil
}

func (l *listMachinesTool) Execute(ctx *rctx.ToolContext, _ ListMachinesParams) (ToolResponse, error) {
	caller, failure := resolveRunCaller(ctx, l.repo)
	if failure != nil {
		return *failure, nil
	}
	if l.controller == nil {
		return NewTextErrorResponse("Machine management is not available in this runtime."), nil
	}
	list, err := l.controller.ListMachines(ctx.Context, caller.userID)
	if err != nil {
		return NewTextErrorResponse("Could not list machines: " + err.Error()), nil
	}
	if len(list) == 0 {
		return NewTextResponse("The user has no machines."), nil
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	own := runMachineID(caller)
	var b strings.Builder
	fmt.Fprintf(&b, "%d machine(s):\n", len(list))
	for _, m := range list {
		fmt.Fprintf(&b, "- %s (id %s)", m.Name, m.ID)
		for _, part := range []string{m.Type, m.State, m.Size} {
			if part != "" {
				b.WriteString(", " + part)
			}
		}
		if m.ID == own {
			b.WriteString(" — this run executes on it")
		}
		b.WriteString("\n")
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), list), nil
}

// =============================================================================
// manage_machine
// =============================================================================

type ManageMachineParams struct {
	MachineID string `json:"machine_id" jsonschema:"required,description=Machine id from list_machines. Never guess one."`
	Action    string `json:"action" jsonschema:"required,enum=start,enum=stop,enum=resize,description=start wakes a suspended machine. stop suspends a running one. resize changes a suspended machine's size; it takes effect at its next start."`
	Size      string `json:"size,omitempty" jsonschema:"enum=s1,enum=s2,enum=m,enum=l,enum=xl,enum=2xl,enum=3xl,description=The new size. resize only."`
}

const manageMachineDescription = `Start, stop or resize one of the user's own machines (from list_machines), exactly as Settings -> Machines does.

- start: wake a suspended machine. Compute is billed while it runs, so an interactive call raises an approval card and runs only once the user approves it.
- stop: suspend a running machine. No card. Never the machine this run executes on.
- resize: change a SUSPENDED machine's size (s1, s2, m, l, xl, 2xl, 3xl); it takes effect when it next starts. Needs an approval card. To resize a running machine: stop, resize, then start.

Unattended runs may only start the machine their trigger activation targets.`

type machineTool struct {
	Tool
	repo       runCallerStore
	controller MachineController
}

// Preserve the approval hook when a workflow/global setting binds parameters.
func (t *machineTool) Bindings() Bindings { return t.Tool.(BindableTool).Bindings() }
func (t *machineTool) WithBindings(bindings Bindings) (Tool, error) {
	bound, err := t.Tool.(BindableTool).WithBindings(bindings)
	if err != nil {
		return t, err
	}
	clone := *t
	clone.Tool = bound
	return &clone, nil
}

type machineTypedTool struct{ outer *machineTool }

func (t *machineTypedTool) Name() string        { return ToolManageMachine }
func (t *machineTypedTool) Description() string { return manageMachineDescription }
func (t *machineTypedTool) RequiresPermission(ManageMachineParams) (bool, error) {
	return true, nil
}

func NewManageMachineTool(repo runCallerStore, controller MachineController) Tool {
	t := &machineTool{repo: repo, controller: controller}
	t.Tool = NewToolWrapper[ManageMachineParams, ToolResponse](&machineTypedTool{outer: t})
	return t
}

// normalize validates p and returns its canonical action and size.
func (p ManageMachineParams) normalize() (action, size string, err error) {
	action = strings.ToLower(strings.TrimSpace(p.Action))
	size = strings.ToLower(strings.TrimSpace(p.Size))
	if strings.TrimSpace(p.MachineID) == "" {
		return "", "", errors.New("machine_id is required")
	}
	switch action {
	case "start", "stop":
		if size != "" {
			return "", "", fmt.Errorf("size applies only to resize, not %s", action)
		}
	case "resize":
		known := false
		for _, s := range machineSizes {
			known = known || s == size
		}
		if !known {
			return "", "", fmt.Errorf("resize needs size, one of %s", strings.Join(machineSizes, ", "))
		}
	default:
		return "", "", fmt.Errorf("action %q is not one of start, stop, resize", p.Action)
	}
	return action, size, nil
}

// machineApprovalHash binds an approval to exactly one action, machine and
// size: the card the user approved is the call that runs.
func machineApprovalHash(action, machineID, size string) string {
	sum := sha256.Sum256([]byte(action + "\x00" + strings.TrimSpace(machineID) + "\x00" + size))
	return "machine:" + hex.EncodeToString(sum[:])
}

// runMachineID is the machine the calling run executes on, or "".
func runMachineID(caller *runCaller) string {
	if caller == nil || caller.chat == nil || caller.chat.ActiveDaemonID == nil {
		return ""
	}
	return *caller.chat.ActiveDaemonID
}

// PrepareAutomation implements AutomationApprovalTool. It reports whether the
// call needs a card: start and resize do when attended; stop never does; an
// unattended start of the activation's own machine runs without one, and
// every other unattended call is refused.
func (t *machineTool) PrepareAutomation(ctx *rctx.ToolContext, call ToolCall, unattended bool) (ActivationPreview, bool, error) {
	caller, failure := resolveRunCaller(ctx, t.repo)
	if failure != nil {
		return ActivationPreview{}, false, RefuseActivation("the calling chat could not be resolved", fmt.Errorf("%s", failure.Content))
	}
	if t.controller == nil {
		return ActivationPreview{}, false, RefuseActivation("machine management is unavailable on this server", errors.New("no machine controller"))
	}
	input := normalizeModelInput(call.Input, t.ParamSchema())
	if bindings := t.Bindings(); len(bindings) > 0 {
		var err error
		if input, err = ApplyBindings(ToolManageMachine, input, bindings); err != nil {
			return ActivationPreview{}, false, RefuseActivation("tool bindings could not be applied", err)
		}
	}
	var p ManageMachineParams
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return ActivationPreview{}, false, RefuseActivation("tool input is not a valid manage_machine call", err)
	}
	action, size, err := p.normalize()
	if err != nil {
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	id := strings.TrimSpace(p.MachineID)
	if unattended {
		if err := t.unattendedStartAllowed(ctx, action, id); err != nil {
			return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
		}
		return ActivationPreview{}, false, nil
	}
	if action == "stop" {
		return ActivationPreview{}, false, nil
	}
	machine, err := t.ownedMachine(ctx.Context, caller.userID, id)
	if err != nil {
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	title := fmt.Sprintf("Start machine %s (%s%s)? Compute is billed while it runs.", machine.Name, machine.ID, sizeSuffix(machine.Size))
	if action == "resize" {
		from := machine.Size
		if from == "" {
			from = "its current size"
		}
		title = fmt.Sprintf("Resize machine %s (%s) from %s to %s? It takes effect when the machine next starts.", machine.Name, machine.ID, from, size)
	}
	return ActivationPreview{Title: title, Hash: machineApprovalHash(action, id, size)}, true, nil
}

func sizeSuffix(size string) string {
	if size == "" {
		return ""
	}
	return ", size " + size
}

// ownedMachine is machineID among the user's machines.
func (t *machineTool) ownedMachine(ctx context.Context, userID, machineID string) (MachineSummary, error) {
	list, err := t.controller.ListMachines(ctx, userID)
	if err != nil {
		return MachineSummary{}, fmt.Errorf("could not list the user's machines: %w", err)
	}
	for _, m := range list {
		if m.ID == machineID {
			return m, nil
		}
	}
	return MachineSummary{}, fmt.Errorf("machine %s is not one of the user's machines; list_machines shows them", machineID)
}

// unattendedStartAllowed is nil only for a start of the machine the run's
// trigger activation targets.
func (t *machineTool) unattendedStartAllowed(ctx *rctx.ToolContext, action, machineID string) error {
	if action != "start" {
		return fmt.Errorf("unattended runs cannot %s machines; ask the user in an interactive chat", action)
	}
	target, err := t.controller.ActivationMachine(ctx.Context, ctx.ChatID)
	if err != nil {
		return fmt.Errorf("could not resolve the machine this run's activation targets: %w", err)
	}
	if target == "" || target != machineID {
		return errors.New("an unattended run may only start the machine its trigger activation targets")
	}
	return nil
}

func (t *machineTypedTool) Execute(ctx *rctx.ToolContext, p ManageMachineParams) (ToolResponse, error) {
	o := t.outer
	caller, failure := resolveRunCaller(ctx, o.repo)
	if failure != nil {
		return *failure, nil
	}
	if o.controller == nil {
		return NewTextErrorResponse("Machine management is not available in this runtime."), nil
	}
	action, size, err := p.normalize()
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	id := strings.TrimSpace(p.MachineID)
	exec := automationpolicy.From(ctx.Context)
	if exec.Unattended {
		if err := o.unattendedStartAllowed(ctx, action, id); err != nil {
			return NewTextErrorResponse(err.Error() + ". Nothing changed."), nil
		}
		if err := o.controller.StartMachine(ctx.Context, caller.userID, id, true); err != nil {
			return NewTextErrorResponse("Could not start machine " + id + ": " + err.Error()), nil
		}
		return NewTextResponse(fmt.Sprintf("Machine %s is starting. It takes a minute or so to come up.", id)), nil
	}
	switch action {
	case "stop":
		if id == runMachineID(caller) {
			return NewTextErrorResponse("This run executes on machine " + id + "; stopping it would stop this run. Nothing changed."), nil
		}
		if err := o.controller.StopMachine(ctx.Context, caller.userID, id); err != nil {
			return NewTextErrorResponse("Could not stop machine " + id + ": " + err.Error()), nil
		}
		return NewTextResponse(fmt.Sprintf("Machine %s is stopping.", id)), nil
	}
	callID := ""
	if call, ok := ctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
		callID = call.CurrentToolCallID
	}
	if exec.Approvals[callID] != machineApprovalHash(action, id, size) {
		return NewTextErrorResponse(fmt.Sprintf("Machine %s requires the user's approval card in an interactive chat. It was not run.", action)), nil
	}
	if action == "resize" {
		if err := o.controller.ResizeMachine(ctx.Context, caller.userID, id, size); err != nil {
			return NewTextErrorResponse("Could not resize machine " + id + " (a running machine must be stopped first): " + err.Error()), nil
		}
		return NewTextResponse(fmt.Sprintf("Machine %s is now size %s; it takes effect when it next starts.", id, size)), nil
	}
	if err := o.controller.StartMachine(ctx.Context, caller.userID, id, false); err != nil {
		return NewTextErrorResponse("Could not start machine " + id + ": " + err.Error()), nil
	}
	return NewTextResponse(fmt.Sprintf("Machine %s is starting. It takes a minute or so to come up.", id)), nil
}
