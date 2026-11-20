// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// Machine labels and machine pools (dev-docs/design/machine-pools.md), as an
// agent changes them: manage_machine's label action and manage_machine_pool.
// Both change which machines a pool-bound activation's runs execute on, so an
// interactive call raises an approval card that shows the exact result (the
// machine's full label set; the pool's full definition and its members after)
// and the approved hash covers exactly that result. An unattended run can do
// neither. The writes go through the same DaemonRegistryService rpcs the app
// uses (MachineController), as the user, and machinepool's rules are checked
// before the card so an approved change is not then refused.

const ToolManageMachinePool = "manage_machine_pool"

// MachinePoolSpec is a pool's definition (reliant.v1.MachinePoolSpec).
type MachinePoolSpec struct {
	Name              string            `json:"name"`
	Selector          map[string]string `json:"selector,omitempty"`
	MinMembers        int               `json:"min_members,omitempty"`
	MaxMembers        int               `json:"max_members,omitempty"`
	MemberSize        string            `json:"member_size,omitempty"`
	MaxRunsPerMember  int               `json:"max_runs_per_member,omitempty"`
	MemberIdleTimeout string            `json:"member_idle_timeout,omitempty"`
}

// MachinePoolMember is one of a pool's machines as it is now.
type MachinePoolMember struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state,omitempty"`
	LiveRuns int    `json:"live_runs,omitempty"`
}

// MachinePoolSummary is one of the user's pools with its members.
type MachinePoolSummary struct {
	ID       string              `json:"id"`
	Spec     MachinePoolSpec     `json:"spec"`
	Members  []MachinePoolMember `json:"members,omitempty"`
	Warnings []string            `json:"warnings,omitempty"`
}

// formatLabels is labels as "k=v, k2=v2", sorted by key.
func formatLabels(labels map[string]string) string {
	keys := slices.Sorted(maps.Keys(labels))
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// describeSpec is spec in one line, every field named.
func describeSpec(spec MachinePoolSpec) string {
	size := spec.MemberSize
	if size == "" {
		size = "none (never grows)"
	}
	return fmt.Sprintf("selector %s; max_runs_per_member %d (0 is no limit); min_members %d; max_members %d; member_size %s; member_idle_timeout %s",
		orNone(formatLabels(spec.Selector)), spec.MaxRunsPerMember, spec.MinMembers, spec.MaxMembers, size, spec.MemberIdleTimeout)
}

// writeMachinePools appends list_machines' pool section.
func writeMachinePools(b *strings.Builder, pools []MachinePoolSummary) {
	if len(pools) == 0 {
		b.WriteString("\nNo machine pools. manage_machine_pool defines one.\n")
		return
	}
	fmt.Fprintf(b, "\n%d machine pool(s):\n", len(pools))
	for _, p := range pools {
		fmt.Fprintf(b, "- %s (id %s): %s\n", p.Spec.Name, p.ID, describeSpec(p.Spec))
		members := make([]string, len(p.Members))
		for i, m := range p.Members {
			members[i] = fmt.Sprintf("%s (id %s, %s, %d live run(s))", m.Name, m.ID, orNone(m.State), m.LiveRuns)
		}
		fmt.Fprintf(b, "  members: %s\n", orNone(strings.Join(members, "; ")))
		for _, w := range p.Warnings {
			fmt.Fprintf(b, "  warning: %s\n", w)
		}
	}
}

// =============================================================================
// manage_machine label
// =============================================================================

// nextLabels is the machine's label set once p is applied to current.
func (p ManageMachineParams) nextLabels(current map[string]string) map[string]string {
	next := make(map[string]string, len(current)+len(p.Labels))
	maps.Copy(next, current)
	maps.Copy(next, p.Labels)
	for _, k := range p.Remove {
		delete(next, k)
	}
	return next
}

// An approval of a label or pool change is two digests, "<kind>:<what>:<where>":
// <what> is the change itself (the machine's resulting labels; the pool's
// resulting definition and the pool it replaces) and <where> is which
// machines that puts in which pools, exactly as the card showed it. Both are
// recomputed at apply time; a change to either refuses the call, and the
// split lets the refusal say which moved.

// approvalDigest is the digest of one approval part.
func approvalDigest(v any) string {
	raw, _ := json.Marshal(v) // struct fields in order, map keys sorted
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// approvalParts splits a two-part approval hash of kind into its parts.
func approvalParts(kind, hash string) (what, where string, ok bool) {
	rest, ok := strings.CutPrefix(hash, kind+":")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, ":")
}

// labelApprovalHash binds a label approval to one of owner's machines, its
// exact resulting label set, and the pools that label set moves it into and
// out of (the card's "joins" and "leaves"): a pool whose selector changed
// since the card would move the machine somewhere nobody approved.
func labelApprovalHash(owner, machineID string, labels map[string]string, joins, leaves []string) string {
	if labels == nil {
		labels = map[string]string{}
	}
	what := approvalDigest(struct {
		Owner, Machine string
		Labels         map[string]string
	}{owner, strings.TrimSpace(machineID), labels})
	where := approvalDigest(struct{ Joins, Leaves []string }{sortedCopy(joins), sortedCopy(leaves)})
	return "machine_label:" + what + ":" + where
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	if out == nil {
		out = []string{}
	}
	sort.Strings(out)
	return out
}

// labelRefusal is the refusal for a label set the registry would not store.
const labelRefusal = "labels are lowercase slugs (a-z, 0-9, '.', '_', '-'), at most 63 characters each, and a machine carries at most 32"

// poolMoves are the pools a machine with labels from would leave and join
// with labels to.
func poolMoves(pools []MachinePoolSummary, from, to map[string]string) (joins, leaves []string) {
	for _, p := range pools {
		was, is := machinepool.Matches(from, p.Spec.Selector), machinepool.Matches(to, p.Spec.Selector)
		switch {
		case is && !was:
			joins = append(joins, p.Spec.Name)
		case was && !is:
			leaves = append(leaves, p.Spec.Name)
		}
	}
	return joins, leaves
}

// prepareLabel is label's card: the machine's labels before and after, and
// the pools that moves it into and out of. A label call that changes nothing
// needs none.
func (t *machineTool) prepareLabel(ctx context.Context, userID, id string, p ManageMachineParams) (ActivationPreview, bool, error) {
	machine, err := t.ownedMachine(ctx, userID, id)
	if err != nil {
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	next := p.nextLabels(machine.Labels)
	if err := machinepool.ValidateLabels(next, "labels"); err != nil {
		return ActivationPreview{}, false, RefuseActivation(labelRefusal, err)
	}
	if maps.Equal(next, machine.Labels) {
		return ActivationPreview{}, false, nil
	}
	pools, err := t.controller.ListMachinePools(ctx, userID)
	if err != nil {
		err = fmt.Errorf("could not read the user's machine pools, so the card cannot say which pools the machine joins or leaves: %w", err)
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	joins, leaves := poolMoves(pools, machine.Labels, next)
	title := fmt.Sprintf("Set the labels of machine %s (%s) to: %s (now: %s)? It joins machine pool(s): %s; it leaves: %s. A pool's members run the activations bound to it.",
		machine.Name, machine.ID, orNone(formatLabels(next)), orNone(formatLabels(machine.Labels)), orNone(strings.Join(joins, ", ")), orNone(strings.Join(leaves, ", ")))
	return ActivationPreview{Title: title, Hash: labelApprovalHash(userID, id, next, joins, leaves)}, true, nil
}

// label applies an approved label call: the label set approved is the one
// written, and a machine whose labels, or the pools they put it in, changed
// since the card needs a new one.
func (t *machineTool) label(ctx *rctx.ToolContext, userID, id string, p ManageMachineParams, approved string) ToolResponse {
	machine, err := t.ownedMachine(ctx.Context, userID, id)
	if err != nil {
		return NewTextErrorResponse(err.Error() + ". Nothing changed.")
	}
	next := p.nextLabels(machine.Labels)
	if maps.Equal(next, machine.Labels) {
		return NewTextResponse(fmt.Sprintf("Machine %s already has those labels (%s); nothing changed.", id, orNone(formatLabels(next))))
	}
	pools, err := t.controller.ListMachinePools(ctx.Context, userID)
	if err != nil {
		return NewTextErrorResponse("Could not read the user's machine pools to check which pools machine " + id + " would join or leave: " + err.Error() + ". Nothing changed.")
	}
	joins, leaves := poolMoves(pools, machine.Labels, next)
	if want := labelApprovalHash(userID, id, next, joins, leaves); approved != want {
		gotWhat, _, ok := approvalParts("machine_label", approved)
		wantWhat, _, _ := approvalParts("machine_label", want)
		if ok && gotWhat == wantWhat {
			return NewTextErrorResponse(fmt.Sprintf("The machine pools machine %s would join or leave changed since you approved (now it joins: %s; leaves: %s); ask again. It was not run.",
				id, orNone(strings.Join(joins, ", ")), orNone(strings.Join(leaves, ", "))))
		}
		return NewTextErrorResponse(fmt.Sprintf("Labelling machine %s requires the user's approval card in an interactive chat, for exactly the resulting labels (they may have changed since a card was approved). It was not run.", id))
	}
	if err := t.controller.SetMachineLabels(ctx.Context, userID, id, next); err != nil {
		return NewTextErrorResponse("Could not set the labels of machine " + id + ": " + err.Error())
	}
	return NewTextResponse(fmt.Sprintf("Machine %s now has labels: %s.", id, orNone(formatLabels(next))))
}

// =============================================================================
// manage_machine_pool
// =============================================================================

type ManageMachinePoolParams struct {
	Action            string            `json:"action" jsonschema:"required,enum=put,enum=delete,description=put creates the pool or changes its definition: fields you omit keep the pool's current values (a new pool takes the defaults). delete removes the pool's definition; its machines and their labels are untouched."`
	Name              string            `json:"name" jsonschema:"required,description=The pool's name: a lowercase slug such as pipeline. Activations bind to it with machine_pool."`
	Selector          map[string]string `json:"selector,omitempty" jsonschema:"description=put only: the labels a machine must carry to be a member (manage_machine label sets them). A new pool without one selects {\"pool\": <name>}."`
	MinMembers        *int              `json:"min_members,omitempty" jsonschema:"description=put only: fewest members the pool should have. Advisory: warned about when unmet; nothing is created."`
	MaxMembers        *int              `json:"max_members,omitempty" jsonschema:"description=put only: most members a scale-up may grow the pool to; 0 never grows."`
	MemberSize        *string           `json:"member_size,omitempty" jsonschema:"description=put only: the machine size a scale-up creates (e.g. 2xl); empty never grows."`
	MaxRunsPerMember  *int              `json:"max_runs_per_member,omitempty" jsonschema:"description=put only: at most this many live runs the pool places on one member; 0 is no limit. A full member is passed over and a pool with every member full queues the run."`
	MemberIdleTimeout *string           `json:"member_idle_timeout,omitempty" jsonschema:"description=put only: how long a member may sit idle before it sleeps\\, between 1m and 4h (default 10m)."`
}

const manageMachinePoolDescription = `Define or delete one of the user's machine pools: a named set of their machines (those whose labels match the pool's selector) that a trigger activation can run on instead of one machine (create/update_trigger_activation machine_pool). Each run goes to the least-loaded member, so machines join or leave by their labels with no edit to the activations. list_machines shows the pools, their members and every machine's labels; manage_machine label changes a machine's labels.

- put: create the pool, or change its definition. Fields you omit keep the pool's current values; a new pool takes the defaults (selector {"pool": <name>}, member_idle_timeout 10m, no limits).
- delete: remove the pool's definition. Its machines and their labels are untouched. Refused while an activation runs on the pool.

Both change where runs execute, so an interactive call raises an approval card showing the exact resulting definition and members, and runs only once the user approves it. Unattended runs cannot use this tool.`

type machinePoolTool struct {
	Tool
	repo       runCallerStore
	controller MachineController
}

// Preserve the approval hook when a workflow/global setting binds parameters.
func (t *machinePoolTool) Bindings() Bindings { return t.Tool.(BindableTool).Bindings() }
func (t *machinePoolTool) WithBindings(bindings Bindings) (Tool, error) {
	bound, err := t.Tool.(BindableTool).WithBindings(bindings)
	if err != nil {
		return t, err
	}
	clone := *t
	clone.Tool = bound
	return &clone, nil
}

type machinePoolTypedTool struct{ outer *machinePoolTool }

func (t *machinePoolTypedTool) Name() string        { return ToolManageMachinePool }
func (t *machinePoolTypedTool) Description() string { return manageMachinePoolDescription }
func (t *machinePoolTypedTool) RequiresPermission(ManageMachinePoolParams) (bool, error) {
	return true, nil
}

func NewManageMachinePoolTool(repo runCallerStore, controller MachineController) Tool {
	t := &machinePoolTool{repo: repo, controller: controller}
	t.Tool = NewToolWrapper[ManageMachinePoolParams, ToolResponse](&machinePoolTypedTool{outer: t})
	return t
}

// poolChange is one validated manage_machine_pool call against the pools as
// they are now: what the card shows and the hash binds.
type poolChange struct {
	action string
	// owner is the user the pool and every member belong to: members come
	// from ListMachines(owner), which lists only that user's machines, as the
	// registry's member query reads only the pool owner's machines.
	owner   string
	current *MachinePoolSummary // nil: put creates the pool
	next    MachinePoolSpec     // put's resulting definition
	members []MachineSummary    // put: the machines its selector matches now
}

// noop reports a put that would leave the pool exactly as it is.
func (c poolChange) noop() bool {
	return c.action == "put" && c.current != nil && specEqual(c.current.Spec, c.next)
}

func specEqual(a, b MachinePoolSpec) bool {
	return a.Name == b.Name && maps.Equal(a.Selector, b.Selector) && a.MinMembers == b.MinMembers && a.MaxMembers == b.MaxMembers &&
		a.MemberSize == b.MemberSize && a.MaxRunsPerMember == b.MaxRunsPerMember && a.MemberIdleTimeout == b.MemberIdleTimeout
}

// memberIDs are the machines the card names as the pool's members, sorted:
// put's members after the change, delete's members now.
func (c poolChange) memberIDs() []string {
	var ids []string
	if c.action == "delete" {
		for _, m := range c.current.Members {
			ids = append(ids, m.ID)
		}
	} else {
		for _, m := range c.members {
			ids = append(ids, m.ID)
		}
	}
	return sortedCopy(ids)
}

// hash binds an approval to the exact resulting definition, to the pool as
// it was when the card was raised, and to the members the card showed (and
// whose they are), so a pool changed since, or a machine labelled into or
// out of it, needs a new card.
func (c poolChange) hash() string {
	var current any
	if c.current != nil {
		current = struct {
			ID   string
			Spec MachinePoolSpec
		}{c.current.ID, c.current.Spec}
	}
	what := approvalDigest(struct {
		Action, Owner string
		Current       any
		Next          MachinePoolSpec
	}{c.action, c.owner, current, c.next})
	where := approvalDigest(struct {
		Owner   string
		Members []string
	}{c.owner, c.memberIDs()})
	return "machine_pool:" + what + ":" + where
}

// membersChanged reports whether approved approves this very change but
// with other members: a machine was labelled into or out of the pool since
// the card.
func (c poolChange) membersChanged(approved string) bool {
	gotWhat, gotWhere, ok := approvalParts("machine_pool", approved)
	wantWhat, wantWhere, _ := approvalParts("machine_pool", c.hash())
	return ok && gotWhat == wantWhat && gotWhere != wantWhere
}

// title is the card.
func (c poolChange) title() string {
	if c.action == "delete" {
		return fmt.Sprintf("Delete machine pool %s (id %s)? Its definition is removed; its %d machine(s) and their labels are untouched. Refused while an activation runs on it.",
			c.current.Spec.Name, c.current.ID, len(c.current.Members))
	}
	names := make([]string, len(c.members))
	for i, m := range c.members {
		names[i] = fmt.Sprintf("%s (%s)", m.Name, m.ID)
	}
	members := orNone(strings.Join(names, ", "))
	if c.current == nil {
		return fmt.Sprintf("Create machine pool %s: %s. Members: %s. Activations bound to it run on its members.", c.next.Name, describeSpec(c.next), members)
	}
	return fmt.Sprintf("Change machine pool %s (id %s): %s. Result: %s. Members after: %s. Activations bound to it run on its members.",
		c.next.Name, c.current.ID, specDiff(c.current.Spec, c.next), describeSpec(c.next), members)
}

// specDiff names each field that changes, from and to.
func specDiff(a, b MachinePoolSpec) string {
	var out []string
	add := func(field string, from, to any) {
		if fmt.Sprint(from) != fmt.Sprint(to) {
			out = append(out, fmt.Sprintf("%s %v -> %v", field, from, to))
		}
	}
	add("selector", orNone(formatLabels(a.Selector)), orNone(formatLabels(b.Selector)))
	add("max_runs_per_member", a.MaxRunsPerMember, b.MaxRunsPerMember)
	add("min_members", a.MinMembers, b.MinMembers)
	add("max_members", a.MaxMembers, b.MaxMembers)
	add("member_size", orNone(a.MemberSize), orNone(b.MemberSize))
	add("member_idle_timeout", a.MemberIdleTimeout, b.MemberIdleTimeout)
	return strings.Join(out, "; ")
}

// change validates p against the user's pools as they are now.
func (t *machinePoolTool) change(ctx context.Context, userID string, p ManageMachinePoolParams) (poolChange, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return poolChange{}, errors.New("name is required")
	}
	pools, err := t.controller.ListMachinePools(ctx, userID)
	if err != nil {
		return poolChange{}, fmt.Errorf("could not read the user's machine pools: %w", err)
	}
	c := poolChange{action: action, owner: userID}
	for i := range pools {
		if pools[i].Spec.Name == name {
			c.current = &pools[i]
		}
	}
	switch action {
	case "delete":
		if p.Selector != nil || p.MinMembers != nil || p.MaxMembers != nil || p.MemberSize != nil || p.MaxRunsPerMember != nil || p.MemberIdleTimeout != nil {
			return poolChange{}, errors.New("delete accepts only name")
		}
		if c.current == nil {
			return poolChange{}, fmt.Errorf("the user has no machine pool named %q; list_machines lists them", name)
		}
		return c, nil
	case "put":
	default:
		return poolChange{}, fmt.Errorf("action %q is not one of put, delete", p.Action)
	}
	next := core.MachinePool{Name: name}
	if c.current != nil {
		s := c.current.Spec
		next = core.MachinePool{Name: name, Selector: maps.Clone(s.Selector), MinMembers: s.MinMembers, MaxMembers: s.MaxMembers,
			MemberSize: s.MemberSize, MaxRunsPerMember: s.MaxRunsPerMember, MemberIdleTimeout: s.MemberIdleTimeout}
	}
	if p.Selector != nil {
		next.Selector = maps.Clone(p.Selector)
	}
	if p.MinMembers != nil {
		next.MinMembers = *p.MinMembers
	}
	if p.MaxMembers != nil {
		next.MaxMembers = *p.MaxMembers
	}
	if p.MemberSize != nil {
		next.MemberSize = strings.ToLower(strings.TrimSpace(*p.MemberSize))
	}
	if next.MemberSize != "" && !slices.Contains(machineSizes, next.MemberSize) {
		return poolChange{}, fmt.Errorf("member_size must be one of %s, or empty", strings.Join(machineSizes, ", "))
	}
	if p.MaxRunsPerMember != nil {
		next.MaxRunsPerMember = *p.MaxRunsPerMember
	}
	if p.MemberIdleTimeout != nil {
		next.MemberIdleTimeout = strings.TrimSpace(*p.MemberIdleTimeout)
	}
	if err := machinepool.NormalizeSpec(&next); err != nil {
		msg := err.Error()
		if ce := svcerr.ToConnectV2(err); ce != nil {
			msg = ce.Message()
		}
		return poolChange{}, fmt.Errorf("the pool definition is not valid: %s", msg)
	}
	c.next = MachinePoolSpec{Name: next.Name, Selector: next.Selector, MinMembers: next.MinMembers, MaxMembers: next.MaxMembers,
		MemberSize: next.MemberSize, MaxRunsPerMember: next.MaxRunsPerMember, MemberIdleTimeout: next.MemberIdleTimeout}
	machines, err := t.controller.ListMachines(ctx, userID)
	if err != nil {
		return poolChange{}, fmt.Errorf("could not list the user's machines to show the pool's members: %w", err)
	}
	sort.SliceStable(machines, func(i, j int) bool { return machines[i].Name < machines[j].Name })
	for _, m := range machines {
		if machinepool.Matches(m.Labels, c.next.Selector) {
			c.members = append(c.members, m)
		}
	}
	return c, nil
}

// decode reads call as Run will: model input repaired, bindings applied,
// unknown fields refused.
func (t *machinePoolTool) decode(call ToolCall) (ManageMachinePoolParams, error) {
	input := normalizeModelInput(call.Input, t.ParamSchema())
	if bindings := t.Bindings(); len(bindings) > 0 {
		var err error
		if input, err = ApplyBindings(ToolManageMachinePool, input, bindings); err != nil {
			return ManageMachinePoolParams{}, RefuseActivation("tool bindings could not be applied", err)
		}
	}
	var p ManageMachinePoolParams
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return ManageMachinePoolParams{}, RefuseActivation("tool input is not a valid manage_machine_pool call", err)
	}
	return p, nil
}

// unattendedPoolRefusal: a pool decides where unattended runs execute.
const unattendedPoolRefusal = "unattended runs cannot define or delete machine pools; ask the user in an interactive chat"

// PrepareAutomation implements AutomationApprovalTool: every change needs a
// card when attended and is refused unattended; a put that changes nothing
// needs none.
func (t *machinePoolTool) PrepareAutomation(ctx *rctx.ToolContext, call ToolCall, unattended bool) (ActivationPreview, bool, error) {
	caller, failure := resolveRunCaller(ctx, t.repo)
	if failure != nil {
		return ActivationPreview{}, false, RefuseActivation("the calling chat could not be resolved", fmt.Errorf("%s", failure.Content))
	}
	if t.controller == nil {
		return ActivationPreview{}, false, RefuseActivation("machine management is unavailable on this server", errors.New("no machine controller"))
	}
	if unattended {
		return ActivationPreview{}, false, RefuseActivation(unattendedPoolRefusal, errors.New(unattendedPoolRefusal))
	}
	p, err := t.decode(call)
	if err != nil {
		return ActivationPreview{}, false, err
	}
	c, err := t.change(ctx.Context, caller.userID, p)
	if err != nil {
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	if c.noop() {
		return ActivationPreview{}, false, nil
	}
	return ActivationPreview{Title: c.title(), Hash: c.hash()}, true, nil
}

func (t *machinePoolTypedTool) Execute(ctx *rctx.ToolContext, p ManageMachinePoolParams) (ToolResponse, error) {
	o := t.outer
	caller, failure := resolveRunCaller(ctx, o.repo)
	if failure != nil {
		return *failure, nil
	}
	if o.controller == nil {
		return NewTextErrorResponse("Machine management is not available in this runtime."), nil
	}
	exec := automationpolicy.From(ctx.Context)
	if exec.Unattended {
		return NewTextErrorResponse(unattendedPoolRefusal + ". Nothing changed."), nil
	}
	c, err := o.change(ctx.Context, caller.userID, p)
	if err != nil {
		return NewTextErrorResponse(err.Error() + ". Nothing changed."), nil
	}
	if c.noop() {
		return NewTextResponse(fmt.Sprintf("Machine pool %s already has that definition; nothing changed.", c.next.Name)), nil
	}
	callID := ""
	if call, ok := ctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
		callID = call.CurrentToolCallID
	}
	if approved := exec.Approvals[callID]; approved != c.hash() {
		if c.membersChanged(approved) {
			return NewTextErrorResponse(fmt.Sprintf("The pool's members changed since you approved (now: %s); ask again. It was not run.", orNone(strings.Join(c.memberIDs(), ", ")))), nil
		}
		return NewTextErrorResponse("Changing a machine pool requires the user's approval card in an interactive chat, for exactly this definition (the pool may have changed since a card was approved). It was not run."), nil
	}
	if c.action == "delete" {
		if err := o.controller.DeleteMachinePool(ctx.Context, caller.userID, c.current.Spec.Name); err != nil {
			return NewTextErrorResponse("Could not delete machine pool " + c.current.Spec.Name + ": " + err.Error()), nil
		}
		return NewTextResponse(fmt.Sprintf("Deleted machine pool %s (id %s); its machines and their labels are untouched.", c.current.Spec.Name, c.current.ID)), nil
	}
	stored, err := o.controller.PutMachinePool(ctx.Context, caller.userID, c.next)
	if err != nil {
		return NewTextErrorResponse("Could not write machine pool " + c.next.Name + ": " + err.Error()), nil
	}
	var b strings.Builder
	verb := "Changed"
	if c.current == nil {
		verb = "Created"
	}
	fmt.Fprintf(&b, "%s machine pool:\n", verb)
	writeMachinePools(&b, []MachinePoolSummary{stored})
	// The registry computes members itself; one labelled in or out between
	// the check above and the write is reported, never hidden.
	written := make([]string, 0, len(stored.Members))
	for _, m := range stored.Members {
		written = append(written, m.ID)
	}
	if approvedMembers := c.memberIDs(); !slices.Equal(sortedCopy(written), approvedMembers) {
		fmt.Fprintf(&b, "warning: the pool's members changed while it was written: approved %s, now %s; review them with list_machines.\n",
			orNone(strings.Join(approvedMembers, ", ")), orNone(strings.Join(sortedCopy(written), ", ")))
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), stored), nil
}
