// Copyright (c) 2025 Reliant Labs

// Package approvalkind decides which chat level may decide an approval: its
// min_role (control-plane docs/design/chat-collaboration.md §2.4).
//
// An approval row records approval_type, and that cannot tell the cases the
// owner's rules need apart: APPROVAL_TYPE_TOOL_CALL covers every tool that
// raises a card through AutomationApprovalTool, from a trigger change to a
// machine start to a connection proposal. So the kind is the TOOL, and it is
// decided here, once, when ApprovalCreate writes the row (approvals.min_role).
// Every resolve path reads the stored value back through Stored, never by
// re-deriving it.
//
// The level is fail-closed in both directions: a tool this package does not
// name is owner-only, and a row whose min_role is NULL (written by a worker
// that predates the column) or holds a value this package does not know is
// owner-only too.
//
//forge:exclude-contract: a stateless classification table; no state, nothing to mock
package approvalkind

// Role is the least chat level that may decide an approval. Levels nest:
// Owner includes Manage, which includes Reply.
type Role string

const (
	// Reply members may decide it: a workflow step and an ordinary tool
	// approval (the owner: "if people can reply to chats they can answer
	// questions / approve things").
	Reply Role = "reply"
	// Manage members may decide it: a change to a workflow definition or an
	// automation, which then runs as the owner repeatedly.
	Manage Role = "manage"
	// Only the chat's owner may decide it: the machine, a Forge Change, a
	// connection proposal, and anything unclassified.
	Owner Role = "owner"
)

// The tools that raise an approval card (every llm/tools type implementing
// AutomationApprovalTool). They mirror internal/llm/tools' constants, which
// approvalkind_test pins, so this leaf package does not import the tool set.
const (
	toolCreateTriggerActivation = "create_trigger_activation"
	toolUpdateTriggerActivation = "update_trigger_activation"
	toolEnableTrigger           = "enable_trigger"
	toolPauseTrigger            = "pause_trigger"
	toolDeleteTrigger           = "delete_trigger"
	toolManageMachine           = "manage_machine"
	toolManageMachinePool       = "manage_machine_pool"
	toolManageChange            = "manage_change"
	toolProposeConnection       = "propose_connection"
)

// toolRoles is every card-raising tool and the level that decides its card.
var toolRoles = map[string]Role{
	// A workflow change: an automation runs as the owner, again and again.
	// pause_trigger never raises a card today; it is classified with its
	// siblings so that it fails into manage, not reply, if it ever does.
	toolCreateTriggerActivation: Manage,
	toolUpdateTriggerActivation: Manage,
	toolEnableTrigger:           Manage,
	toolPauseTrigger:            Manage,
	toolDeleteTrigger:           Manage,
	// "The machine" is the owner's alone.
	toolManageMachine:     Owner,
	toolManageMachinePool: Owner,
	// A Forge Change acts outside the chat through the owner's identity
	// (design open question 3: moving it to manage is this one line).
	toolManageChange: Owner,
	// Approving a proposal attaches the APPROVER's own connection to the run
	// (approval/connection_proposal.go); a collaborator approving would put
	// their credential into the owner's run.
	toolProposeConnection: Owner,
}

// Source is what raised an approval, as ApprovalCreate knows it.
type Source struct {
	// ToolCall is true when the approval answers one of the run's own tool
	// calls (APPROVAL_TYPE_TOOL_CALL); false for a workflow step.
	ToolCall bool
	// ToolName names that call's tool. It is empty for a workflow step, and
	// also for a tool call raised by workflow code that predates the field,
	// which is then owner-only.
	ToolName string
	// ConnectionProposal is true for a propose_connection card, which is
	// owner-only whatever its tool name says.
	ConnectionProposal bool
}

// For is the min_role ApprovalCreate stamps on the approval s raised.
//
// A workflow step is Reply: an `approval` node and the agent workflow's
// manual-mode "Approve tool execution?" gate (itself an approval node,
// internal/workflow/builtin/agent.yaml) both reach ApprovalCreate with no
// tool call, and the owner's decision puts both at reply. A tool call is its
// tool's level; an unknown tool, or a tool call with no tool name, is Owner.
func For(s Source) Role {
	if s.ConnectionProposal {
		return Owner
	}
	if !s.ToolCall {
		return Reply
	}
	if r, ok := toolRoles[s.ToolName]; ok {
		return r
	}
	return Owner
}

// Stored reads an approval's stored min_role. NULL, empty, and any value this
// package does not know are Owner: a row written before the column, or by a
// worker older or newer than this one, never widens who may decide it.
func Stored(minRole *string) Role {
	if minRole == nil {
		return Owner
	}
	switch r := Role(*minRole); r {
	case Reply, Manage, Owner:
		return r
	}
	return Owner
}

// Allows reports whether a holder of level r may decide an approval whose
// min_role is need. An unknown level allows nothing.
func (r Role) Allows(need Role) bool {
	have, ok := rank(r)
	if !ok {
		return false
	}
	want, ok := rank(need)
	if !ok {
		want, _ = rank(Owner)
	}
	return have >= want
}

// String returns the stored spelling of r.
func (r Role) String() string { return string(r) }

func rank(r Role) (int, bool) {
	switch r {
	case Reply:
		return 1, true
	case Manage:
		return 2, true
	case Owner:
		return 3, true
	}
	return 0, false
}
