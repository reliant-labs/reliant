// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/nomachine"
)

// ToolSpawn is the virtual spawn tool. It is built per node from the node's
// `spawn:` declaration and executed by the workflow runtime, so it is never in
// the registry.
const ToolSpawn = "spawn"

// Capabilities is one agent turn's tool capability set: what the model was
// offered, what load_tool may add, and at what tier.
//
// It is resolved ONCE per turn, by ResolveCapabilities in call_llm, from
// durable inputs, and travels through workflow history to the execute_tools
// that runs that turn's calls (research/TOOL_CAPABILITIES.md). It replaced a
// process-global store keyed by (chat, thread): activities run on a shared,
// non-sticky task queue, so call_llm and execute_tools routinely land on
// different workers, and a restart emptied the store between them.
//
// Names only, never schemas — it rides in history twice per turn.
type Capabilities struct {
	// Offered is every tool name in the request's tool array, sorted. A call
	// to anything else is refused at execution.
	Offered []string
	// LoadableAll means load_tool may reach any reachable tool.
	LoadableAll bool
	// Loadable is what load_tool may reach when LoadableAll is false, sorted.
	Loadable []string
	// Permission is the resolved tier. Never empty on a resolved set.
	Permission string
	// NoMachine mirrors chats.no_machine: nothing that needs the user's
	// machine is offered or loadable, and the tools that exist only for a run
	// without one (request_machine) are reachable.
	NoMachine bool
	// MCPTools are the connected MCP tool names, sorted. Only populated when
	// the node's filters can reach an MCP tool, and never on a no-machine run.
	MCPTools []string
	// SpawnPresets are the presets the spawn tool was offered with, sorted.
	SpawnPresets []string
	// WithheldIntegrations maps each connection-gated integration this turn's
	// declaration reaches, but the run's owner cannot use, to the reason. Its
	// tools are neither offered nor loadable. Keyed by integration rather than
	// by tool: one entry withholds every action of that integration, which
	// keeps the set small however many actions a manifest exposes.
	WithheldIntegrations map[string]string
	// BoundParams are the parameters a human bound on each offered tool,
	// keyed by tool and then parameter: removed from the schema the model was
	// offered, and merged over its calls at execution (ExecutionBindings).
	// Only tools with a bound parameter appear.
	BoundParams map[string]map[string]BoundParam
	// Unattended: nobody is attending the run (runtime.IsUnattended). A tool
	// UnattendedWithholding names is neither offered nor loadable, unless it
	// is in UnattendedOptIn.
	Unattended bool
	// UnattendedOptIn are the tools UnattendedWithholding names that this
	// turn's declaration names exactly (ToolAccess.Named), sorted: the step's
	// author decided it does that work, so an unattended run keeps them. Only
	// populated on an unattended turn.
	UnattendedOptIn []string
}

// BoundParam is one bound parameter as a turn's capability set records it.
type BoundParam struct {
	// Value is the bound value, when the set carries it: a binding from the
	// workflow's tools_config.tools, which is already in workflow history as
	// call_llm's own input.
	Value BoundValue
	// Global marks a parameter bound by the run owner's global setting. The
	// set records its name only, and execution re-reads the value from the
	// setting. A binding can carry a secret — an Authorization header on the
	// http integration — and a setting is not otherwise in workflow history,
	// which is retained, shown in the Temporal UI, and checked into replay
	// fixtures.
	Global bool
}

// CapabilityInputs is everything one turn's tool reach is decided from. Every
// field is durable: the node's declaration, the chat row, the RuntimeContext,
// the load_tool grants recorded in workflow state, a re-discovery of the
// connected MCP tools, and the owner's integration connections.
type CapabilityInputs struct {
	// Access is the node's declaration, expanded by ResolveToolAccess. The
	// caller narrows it for a no-machine run first (handlers/no_machine.go),
	// which is also where request_machine is preloaded.
	Access ToolAccess
	// Permission is the node's tier, already capped to its parent's.
	Permission string
	// NoMachine is chats.no_machine.
	NoMachine bool
	// Grants are the tools load_tool granted in this scope so far.
	Grants []string
	// MCPTools are the connected MCP tool names.
	MCPTools []string
	// MailboxReachable: the agent has a counterpart to message (spawn_send).
	MailboxReachable bool
	// CanSpawnChildren: the agent may spawn, so it may also stop what it
	// spawned (spawn_stop).
	CanSpawnChildren bool
	// OwnChildren: the thread has started sub-agents of its own — a spawn
	// tool_calls row that started a child. Read from the database, so it
	// survives a worker restart and holds after the node's spawn config has
	// gone away: the children it started have not.
	OwnChildren bool
	// InheritedChildren: the thread is a branch of a conversation that had
	// spawned sub-agents before the fork point. The original conversation
	// still owns them, so the branch may look at them and never control them.
	InheritedChildren bool
	// SpawnPresets are the presets of the node's spawn declarations; any
	// preset at all is what offers the spawn tool.
	SpawnPresets []string
	// ResponseTool is the node's structured-output tool name, if any.
	ResponseTool string
	// UsableIntegrations are the connection-gated integrations the run's
	// owner can authenticate now (ToolsFactory.UsableIntegrations). It FAILS
	// CLOSED: every gated integration the declaration reaches that is not
	// named here is withheld, so a caller that could not ask — or never asked
	// — offers no tool that would answer with a 401.
	UsableIntegrations map[string]bool
	// Unattended is the run's runtime.IsUnattended, carried on the
	// RuntimeContext: a trigger-fired run, every sub-workflow and spawned
	// sub-agent of one, and never a person's turn.
	Unattended bool
}

// ResolveCapabilities computes a turn's capability set. Pure: no I/O and no
// state beyond the static registry and integration catalog.
//
// A name is REACHABLE when nothing about the run excludes it (see exclusion):
// it is a registry tool or a connected MCP tool, it does not need a machine on
// a no-machine run, it is not a no-machine-only tool on a run that has one,
// its integration (if it needs a connection) is one the owner can use, an
// unattended run is not withheld it, and it is within the tier. What is
// offered is the reachable preloaded tools, the reachable grants the
// declaration may load, and the structural tools every agent of this shape is
// handed.
func ResolveCapabilities(in CapabilityInputs) *Capabilities {
	caps := &Capabilities{
		LoadableAll:  in.Access.LoadableAll,
		Permission:   NormalizePermission(in.Permission),
		NoMachine:    in.NoMachine,
		SpawnPresets: sortedUnique(in.SpawnPresets),
		Unattended:   in.Unattended,
	}
	if in.Unattended {
		for _, name := range in.Access.Named {
			if UnattendedWithholding(name) != "" {
				caps.UnattendedOptIn = append(caps.UnattendedOptIn, name)
			}
		}
		caps.UnattendedOptIn = sortedUnique(caps.UnattendedOptIn)
	}
	if !caps.LoadableAll {
		caps.Loadable = sortedUnique(in.Access.Loadable)
	}
	// Every user-configured MCP server is daemon-placed, so a no-machine run
	// has none to reach.
	if !in.NoMachine {
		caps.MCPTools = sortedUnique(in.MCPTools)
	}
	for _, id := range ReachedGatedIntegrations(in.Access) {
		if in.UsableIntegrations[id] {
			continue
		}
		if caps.WithheldIntegrations == nil {
			caps.WithheldIntegrations = map[string]string{}
		}
		caps.WithheldIntegrations[id] = fmt.Sprintf(
			"it needs a %s connection, and the run's owner has none it can use (connect one in Settings → Integrations)",
			integrationDisplayName(id))
	}

	registry := registryIndex()
	offered := make(map[string]bool)
	for _, name := range in.Access.Preloaded {
		if caps.exclusion(registry, name) == reachable {
			offered[name] = true
		}
	}
	// A grant is intersected with what this declaration may load rather than
	// appended past it, so a grant made under a wider node — or while the run
	// had no machine, or while the owner had a connection — cannot outlive the
	// change when the same thread runs on.
	for _, name := range in.Grants {
		if in.Access.CanLoad(name) && caps.exclusion(registry, name) == reachable {
			offered[name] = true
		}
	}

	// Structural tools are handed to an agent by its shape, not by its
	// declaration:
	//   - load_tool when, and only when, the declaration names something for
	//     it to reach. A node that declared no loadable tools wants exactly
	//     what it preloaded, and a discovery tool with an empty reach is a
	//     schema the model has to read and can never use.
	//   - spawn_status once the thread has sub-agents to look at, its own or
	//     inherited. It is the read side: worthless before the first spawn
	//     and needed on the very next turn after it, which is when an
	//     orchestrator used to have to stop and load_tool it by hand.
	//   - spawn_send when there is a counterpart to message — a parent, or
	//     children of its own. A preset author was never asked to remember a
	//     mailbox tool.
	//   - spawn_stop when the agent may spawn or has spawned. It refuses
	//     anything but the caller's own direct child, so an agent with none
	//     could only misuse it.
	// A branch's inherited sub-agents grant spawn_status alone: the original
	// conversation owns them.
	structural := map[string]bool{
		ToolLoadTool:    caps.LoadableAll || len(caps.Loadable) > 0,
		ToolSpawnStatus: in.OwnChildren || in.InheritedChildren,
		ToolSpawnSend:   in.MailboxReachable || in.OwnChildren,
		ToolSpawnStop:   in.CanSpawnChildren || in.OwnChildren,
	}
	for name, grant := range structural {
		if grant && caps.exclusion(registry, name) == reachable {
			offered[name] = true
		}
	}
	// spawn is granted by the node's spawn declaration, not by the tier — see
	// MinimumPermissionForTool. The response tool is the node's own output
	// channel. Neither is a registry tool, so neither goes through exclusion.
	if len(caps.SpawnPresets) > 0 {
		offered[ToolSpawn] = true
	}
	if in.ResponseTool != "" {
		offered[in.ResponseTool] = true
	}

	caps.Offered = sortedNameSet(offered)
	return caps
}

// ReachedGatedIntegrations lists, sorted, the connection-gated integrations a
// declaration can reach at all — preloaded, loadable, or anything when it may
// load anything. These are the integrations whose usability is worth asking
// the credential source about; a declaration that reaches none never asks.
func ReachedGatedIntegrations(access ToolAccess) []string {
	lists := [][]string{access.Preloaded, access.Loadable}
	if access.LoadableAll {
		gated := make([]string, 0, len(connectionGated()))
		for name := range connectionGated() {
			gated = append(gated, name)
		}
		lists = append(lists, gated)
	}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, name := range list {
			if id, gated := ConnectionGatedIntegration(name); gated {
				seen[id] = true
			}
		}
	}
	return sortedNameSet(seen)
}

// exclusion is why the run excludes a tool regardless of what the node
// declared. It is the ONE place each run-level rule lives, so the menu,
// load_tool, its advertised list, its search and execution-time refusals
// cannot disagree about any of them.
type exclusion int

const (
	reachable exclusion = iota
	// excludedUnknown: neither a registry tool nor an MCP tool.
	excludedUnknown
	// excludedNotConnected: an MCP tool that is not connected.
	excludedNotConnected
	// excludedNeedsMachine: needs the user's machine, and the run has none.
	excludedNeedsMachine
	// excludedHasMachine: exists only for a run with no machine
	// (OnlyWithoutMachine — request_machine), and the run has one. Holds
	// however it is named: preloaded, matched by a glob, loadable via "*".
	excludedHasMachine
	// excludedUnusableIntegration: a connection-gated integration the run's
	// owner cannot use (WithheldIntegrations).
	excludedUnusableIntegration
	// excludedUnattended: the run is unattended, the tool is one
	// UnattendedWithholding names, and the declaration did not name it.
	excludedUnattended
	// excludedTier: above the run's tier.
	excludedTier
)

func (c *Capabilities) exclusion(registry map[string]ToolDefinition, name string) exclusion {
	if strings.HasPrefix(name, MCPToolPrefix) {
		switch {
		case c.NoMachine:
			return excludedNeedsMachine
		case !containsSorted(c.MCPTools, name):
			return excludedNotConnected
		}
		// MCP tools are gated by MCP configuration, not the permission ladder.
		return reachable
	}
	if _, known := registry[name]; !known {
		return excludedUnknown
	}
	switch {
	case c.NoMachine && NeedsMachine(name):
		return excludedNeedsMachine
	case !c.NoMachine && OnlyWithoutMachine(name):
		return excludedHasMachine
	case c.withheldIntegration(name) != "":
		return excludedUnusableIntegration
	case c.withheldUnattended(name) != "":
		return excludedUnattended
	case !PermissionAtLeast(c.Permission, MinimumPermissionForTool(name)):
		return excludedTier
	}
	return reachable
}

// withheldIntegration is the reason name's integration is withheld, or "".
func (c *Capabilities) withheldIntegration(name string) string {
	if id, gated := ConnectionGatedIntegration(name); gated {
		return c.WithheldIntegrations[id]
	}
	return ""
}

// withheldUnattended is why this unattended run is not handed name, or "".
func (c *Capabilities) withheldUnattended(name string) string {
	if !c.Unattended || containsSorted(c.UnattendedOptIn, name) {
		return ""
	}
	return UnattendedWithholding(name)
}

// RecordOffered replaces Offered with the names actually in the request's tool
// array. call_llm calls it once the array is final, so "offered" means
// literally what the model was sent — including a factory that declined to
// build a tool, and the spawn and response tools appended after resolution.
func (c *Capabilities) RecordOffered(names []string) {
	c.Offered = sortedUnique(names)
}

// RecordBoundParams records what the request's tool array binds. call_llm
// calls it beside RecordOffered, from the same final array, so a parameter is
// recorded exactly when it was hidden from the model.
func (c *Capabilities) RecordBoundParams(bound map[string]map[string]BoundParam) {
	c.BoundParams = nil
	for tool, params := range bound {
		if len(params) == 0 {
			continue
		}
		if c.BoundParams == nil {
			c.BoundParams = make(map[string]map[string]BoundParam, len(bound))
		}
		c.BoundParams[tool] = params
	}
}

// Binds reports whether tool has a bound parameter on this turn.
func (c *Capabilities) Binds(tool string) bool {
	return c != nil && len(c.BoundParams[tool]) > 0
}

// BindsFromGlobalSetting reports whether a call to tool needs the run owner's
// global setting for it read before ExecutionBindings can resolve it.
func (c *Capabilities) BindsFromGlobalSetting(tool string) bool {
	if c == nil {
		return false
	}
	for _, param := range c.BoundParams[tool] {
		if param.Global {
			return true
		}
	}
	return false
}

// ExecutionBindings is what a call to tool runs with: each carried value as
// recorded, and each global parameter's value from global — the run owner's
// setting for this tool, re-read by the caller.
//
// A global parameter the setting no longer binds is an error, not an open
// parameter. The model was not shown it, so the call it made has no value
// there; running it without the bound one would run a call nobody configured.
// The next turn's set is resolved from the setting as it is then.
func (c *Capabilities) ExecutionBindings(tool string, global Bindings) (Bindings, error) {
	if !c.Binds(tool) {
		return nil, nil
	}
	recorded := c.BoundParams[tool]
	bindings := make(Bindings, len(recorded))
	var gone []string
	for name, param := range recorded {
		if !param.Global {
			bindings[name] = param.Value
			continue
		}
		value, ok := global[name]
		if !ok || (value.Literal == nil && value.Expr == "") {
			gone = append(gone, name)
			continue
		}
		bindings[name] = value
	}
	if len(gone) > 0 {
		sort.Strings(gone)
		return nil, fmt.Errorf("The value of '%s' on '%s' was fixed by a setting when this tool was offered, and that setting no longer fixes it, so the call was not run. Call '%s' again.",
			strings.Join(gone, "', '"), tool, tool)
	}
	return bindings, nil
}

// Offers reports whether name was in this turn's tool array.
func (c *Capabilities) Offers(name string) bool {
	return c != nil && containsSorted(c.Offered, name)
}

// CanLoad reports whether load_tool may grant name on this turn.
func (c *Capabilities) CanLoad(name string) bool {
	return c.LoadRefusal(name) == ""
}

// AllowsPreset reports whether spawn was offered with preset.
func (c *Capabilities) AllowsPreset(preset string) bool {
	return c != nil && containsSorted(c.SpawnPresets, preset)
}

// LoadRefusal is why load_tool may not grant name, or "" when it may. An
// offered tool is always loadable — it is already in the agent's hands.
func (c *Capabilities) LoadRefusal(name string) string {
	if c.Offers(name) {
		return ""
	}
	isMCP := strings.HasPrefix(name, MCPToolPrefix)
	label := "Tool"
	if isMCP {
		label = "MCP tool"
	}
	// A no-machine-only tool is handed to a no-machine run directly and is
	// never something to load, so a grant of one can never exist to outlive
	// the run's no-machine state.
	if OnlyWithoutMachine(name) {
		return fmt.Sprintf("Tool '%s' is not loadable: a chat with no machine already has it, and a chat on a machine never does.", name)
	}
	switch c.exclusion(registryIndex(), name) {
	case excludedUnknown:
		return fmt.Sprintf("Tool '%s' not found in the registry. Use load_tool with query to search for available tools.", name)
	case excludedNeedsMachine:
		return fmt.Sprintf("%s '%s' needs the user's computer, and this run has no machine, so it cannot be loaded.", label, name)
	case excludedUnusableIntegration:
		return fmt.Sprintf("Tool '%s' was not loaded: %s.", name, c.withheldIntegration(name))
	case excludedUnattended:
		return fmt.Sprintf("Tool '%s' was not loaded: %s. %s", name, c.withheldUnattended(name), unattendedNote)
	}
	if !c.declaresLoadable(name) {
		return fmt.Sprintf("%s '%s' is not loadable in this workflow (see loadable_tools).", label, name)
	}
	switch c.exclusion(registryIndex(), name) {
	case excludedNotConnected:
		return fmt.Sprintf("MCP tool '%s' is not available in this environment. Use load_tool with a query to discover connected MCP tools.", name)
	case excludedTier:
		required := MinimumPermissionForTool(name)
		return fmt.Sprintf("Tool '%s' requires '%s' permission, but agent has '%s' permission.", name, required, c.Permission)
	}
	return ""
}

// Explain is why a call to name is refused at execution: the tool was not in
// this turn's tool array. Derived from the set alone, so execute_tools can say
// why on whichever worker it runs. "" when name was offered.
func (c *Capabilities) Explain(name string) string {
	if c.Offers(name) {
		return ""
	}
	if name == ToolSpawn {
		return "Spawning sub-agents is not available to this agent on this turn, so the spawn call was not run."
	}
	switch c.exclusion(registryIndex(), name) {
	case excludedNeedsMachine:
		return nomachine.Refusal(name)
	case excludedHasMachine:
		return fmt.Sprintf("Tool '%s' is only for a chat with no machine, and this chat has one, so the call was not run. Continue with the tools you have.", name)
	case excludedUnknown:
		return fmt.Sprintf("Tool '%s' does not exist, so the call was not run. Use only the tools you were given.", name)
	case excludedNotConnected:
		return fmt.Sprintf("MCP tool '%s' is not connected in this environment, so it was not offered and the call was not run.", name)
	case excludedUnusableIntegration:
		return fmt.Sprintf("Tool '%s' is not available to this agent, so the call was not run: %s.", name, c.withheldIntegration(name))
	case excludedUnattended:
		return fmt.Sprintf("Tool '%s' is not available in this run, so the call was not run: %s. %s Do not attempt it another way; say in your final response what you would have done.",
			name, c.withheldUnattended(name), unattendedNote)
	case excludedTier:
		return fmt.Sprintf("Tool '%s' requires '%s' permission, but the current permission level is '%s', so the call was not run.",
			name, MinimumPermissionForTool(name), c.Permission)
	}
	if c.CanLoad(name) {
		return fmt.Sprintf("Tool '%s' was not offered on this turn, so the call was not run. Load it first with load_tool(name=%q); it becomes callable on your next turn.", name, name)
	}
	return fmt.Sprintf("Tool '%s' was not offered to this agent, so the call was not run: this workflow step does not grant it. Use only the tools you were given.", name)
}

// ExplainPreset is why a spawn naming preset is refused.
func (c *Capabilities) ExplainPreset(preset string) string {
	return fmt.Sprintf("Preset '%s' is not available. The LLM may have hallucinated this preset. Available presets: %v", preset, c.SpawnPresets)
}

// Deferred is what load_tool can add that was not offered: the "Additional
// tools available" list load_tool's description advertises. It agrees with
// enforcement by construction — the model reads that list as a promise, and a
// name there that loading would refuse cannot be told apart from a malfunction.
func (c *Capabilities) Deferred() []string {
	var deferred []string
	for _, def := range GetToolRegistry() {
		if !c.Offers(def.Name) && c.CanLoad(def.Name) {
			deferred = append(deferred, def.Name)
		}
	}
	for _, name := range c.MCPTools {
		if !c.Offers(name) && c.CanLoad(name) {
			deferred = append(deferred, name)
		}
	}
	return sortedUnique(deferred)
}

// Searchable reports whether load_tool's keyword search may list name: what
// it could grant, what is already offered, and what only the tier keeps from
// it — listed with that status so the agent can tell the user why. A tool
// that is handed over rather than loaded (request_machine) is never listed.
func (c *Capabilities) Searchable(name string) bool {
	if OnlyWithoutMachine(name) {
		return false
	}
	if c.Offers(name) || c.CanLoad(name) {
		return true
	}
	return c.exclusion(registryIndex(), name) == excludedTier && c.declaresLoadable(name)
}

// declaresLoadable is the declaration half of CanLoad: what loadable_tools
// said, before reachability.
func (c *Capabilities) declaresLoadable(name string) bool {
	return c.LoadableAll || containsSorted(c.Loadable, name)
}

// Proto is the set's wire form, recorded in CallLLMOutput.capabilities and
// copied onto ExecuteToolsArgs.capabilities.
func (c *Capabilities) Proto() *reliantv1.ToolCapabilities {
	if c == nil {
		return nil
	}
	return &reliantv1.ToolCapabilities{
		Offered:              c.Offered,
		LoadableAll:          c.LoadableAll,
		Loadable:             c.Loadable,
		Permission:           c.Permission,
		NoMachine:            c.NoMachine,
		McpTools:             c.MCPTools,
		SpawnPresets:         c.SpawnPresets,
		WithheldIntegrations: c.WithheldIntegrations,
		BoundParams:          boundParamsProto(c.BoundParams),
		Unattended:           c.Unattended,
		UnattendedOptIn:      c.UnattendedOptIn,
	}
}

// boundParamsProto is the wire form of a set's bound parameters. A literal is
// JSON by construction — every scope that binds one decodes it from JSON or
// from a protobuf Value — so the conversion cannot fail for a real binding;
// one that somehow did is recorded as a JSON null rather than dropped, so the
// parameter still reads as bound at execution.
func boundParamsProto(bound map[string]map[string]BoundParam) map[string]*reliantv1.ToolBoundParams {
	if len(bound) == 0 {
		return nil
	}
	out := make(map[string]*reliantv1.ToolBoundParams, len(bound))
	for tool, params := range bound {
		wire := &reliantv1.ToolBoundParams{Params: make(map[string]*reliantv1.BoundParam, len(params))}
		for name, param := range params {
			switch {
			case param.Global:
				wire.Params[name] = &reliantv1.BoundParam{Value: &reliantv1.BoundParam_Global{Global: true}}
			case param.Value.IsExpr():
				wire.Params[name] = &reliantv1.BoundParam{Value: &reliantv1.BoundParam_Expr{Expr: param.Value.Expr}}
			default:
				wire.Params[name] = &reliantv1.BoundParam{Value: &reliantv1.BoundParam_Literal{Literal: literalValue(param.Value.Literal)}}
			}
		}
		out[tool] = wire
	}
	return out
}

func literalValue(literal any) *structpb.Value {
	if raw, err := json.Marshal(literal); err == nil {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			if value, err := structpb.NewValue(decoded); err == nil {
				return value
			}
		}
	}
	return structpb.NewNullValue()
}

func boundParamsFromProto(wire map[string]*reliantv1.ToolBoundParams) map[string]map[string]BoundParam {
	if len(wire) == 0 {
		return nil
	}
	out := make(map[string]map[string]BoundParam, len(wire))
	for tool, params := range wire {
		if len(params.GetParams()) == 0 {
			continue
		}
		decoded := make(map[string]BoundParam, len(params.GetParams()))
		for name, param := range params.GetParams() {
			switch value := param.GetValue().(type) {
			case *reliantv1.BoundParam_Global:
				decoded[name] = BoundParam{Global: true}
			case *reliantv1.BoundParam_Expr:
				decoded[name] = BoundParam{Value: ExprBinding(value.Expr)}
			case *reliantv1.BoundParam_Literal:
				decoded[name] = BoundParam{Value: LiteralBinding(value.Literal.AsInterface())}
			}
		}
		out[tool] = decoded
	}
	return out
}

// CapabilitiesFromProto reads a recorded set. It returns nil for an absent
// one: a resolved set always carries a tier, and an output that predates the
// set decodes to a zero-valued message, not a nil one, once the workflow has
// normalized it.
func CapabilitiesFromProto(p *reliantv1.ToolCapabilities) *Capabilities {
	if p == nil || p.GetPermission() == "" {
		return nil
	}
	caps := &Capabilities{
		Offered:      sortedUnique(p.GetOffered()),
		LoadableAll:  p.GetLoadableAll(),
		Loadable:     sortedUnique(p.GetLoadable()),
		Permission:   NormalizePermission(p.GetPermission()),
		NoMachine:    p.GetNoMachine(),
		MCPTools:     sortedUnique(p.GetMcpTools()),
		SpawnPresets: sortedUnique(p.GetSpawnPresets()),
		BoundParams:  boundParamsFromProto(p.GetBoundParams()),
		Unattended:   p.GetUnattended(),
	}
	if caps.Unattended {
		caps.UnattendedOptIn = sortedUnique(p.GetUnattendedOptIn())
	}
	if len(p.GetWithheldIntegrations()) > 0 {
		caps.WithheldIntegrations = make(map[string]string, len(p.GetWithheldIntegrations()))
		for id, reason := range p.GetWithheldIntegrations() {
			caps.WithheldIntegrations[id] = reason
		}
	}
	return caps
}

// LegacyCapabilities is the policy for a batch whose call_llm recorded no set
// — a run that predates it. It is what a worker restart already produced:
// load_tool may reach anything within the chat row's machine boundary, at the
// base tier, and nothing is known to be offered. The next call_llm records a
// set, so a run spends at most one batch here.
func LegacyCapabilities(noMachine bool) *Capabilities {
	return &Capabilities{LoadableAll: true, Permission: PermissionMutating, NoMachine: noMachine}
}

type capabilitiesContextKey struct{}

// WithCapabilities hands the turn's set to the tools execute_tools runs —
// load_tool reads it to decide what it may grant. A context, not a store: the
// set arrives with the activity's input on whichever worker runs it.
func WithCapabilities(ctx context.Context, caps *Capabilities) context.Context {
	if caps == nil {
		return ctx
	}
	return context.WithValue(ctx, capabilitiesContextKey{}, caps)
}

// CapabilitiesFrom returns the set WithCapabilities attached, or nil.
func CapabilitiesFrom(ctx context.Context) *Capabilities {
	if ctx == nil {
		return nil
	}
	caps, _ := ctx.Value(capabilitiesContextKey{}).(*Capabilities)
	return caps
}

type skillsContextKey struct{}

// WithSkills hands the project's skills to the skill tool execute_tools runs.
// execute_tools reads them from the project's config row; the executor's
// factory is built at startup and has none of its own.
func WithSkills(ctx context.Context, skills []config.StoredSkill) context.Context {
	if len(skills) == 0 {
		return ctx
	}
	return context.WithValue(ctx, skillsContextKey{}, skills)
}

// SkillsFrom returns the skills WithSkills attached, or nil.
func SkillsFrom(ctx context.Context) []config.StoredSkill {
	if ctx == nil {
		return nil
	}
	skills, _ := ctx.Value(skillsContextKey{}).([]config.StoredSkill)
	return skills
}

// registryIndex is the registry keyed by name, built once per resolution
// rather than once per name.
func registryIndex() map[string]ToolDefinition {
	registry := GetToolRegistry()
	index := make(map[string]ToolDefinition, len(registry))
	for _, def := range registry {
		index[def.Name] = def
	}
	return index
}

func containsSorted(sorted []string, name string) bool {
	i := sort.SearchStrings(sorted, name)
	return i < len(sorted) && sorted[i] == name
}

func sortedUnique(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			seen[name] = true
		}
	}
	return sortedNameSet(seen)
}

func sortedNameSet(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// integrationDisplayName is a connection-gated integration's display name,
// for a refusal the model can repeat to the user.
func integrationDisplayName(id string) string {
	for _, m := range connectionGated() {
		if m.GetId() == id {
			if name := m.GetDisplayName(); name != "" {
				return name
			}
			break
		}
	}
	return id
}
