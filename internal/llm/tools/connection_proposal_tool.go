// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// propose_connection asks a person to connect an integration (owner
// decision on reliant #2831, 2026-11-18: "Approval card"). The call raises an
// approval card through the same flow as manage_machine and the activation
// tools (internal/workflow/runtime/activation_approval.go). The person
// approves it by making the connection in the app's existing connection
// dialog, launched from the card; ApprovalService.Approve then carries the
// new connection's id, which the server checks against the proposal
// (integration, owner, scope) before the run sees it.
//
// The agent never sees the credential: its result is only a status, the
// integration, and the connection's id and name. The approver owns the
// connection: a chat's approvals are decided only by its owner, who is the
// person this run acts for. An org-wide connection is offered only to an
// owner or admin of the chat's organization, and the server refuses one from
// anyone else, here and again when the card is approved.

const ToolProposeConnection = "propose_connection"

// PreviewKindConnectionProposal is ActivationPreview.Kind for a
// propose_connection card.
const PreviewKindConnectionProposal = "connection_proposal"

// proposalDeclineCooldown is how long after a person declined a proposal an
// identical one is refused, so a model cannot ask again in a loop.
const proposalDeclineCooldown = 10 * time.Minute

// proposalNow is the cooldown's clock; tests fix it.
var proposalNow = time.Now

// maxProposalReason bounds the reason shown on the card.
const maxProposalReason = 500

// proposableIntegrations are the integrations a proposal may name, with the
// name the card shows.
var proposableIntegrations = map[string]string{
	"github": "GitHub",
	"slack":  "Slack",
	"gmail":  "Gmail",
	"twilio": "Twilio",
	"http":   "HTTP API",
}

// ConnectionProposalState is one connection proposal a chat already raised,
// as its approval stands now.
type ConnectionProposalState struct {
	Proposal core.ConnectionProposal
	// Pending: its card still waits for the person.
	Pending bool
	// Declined: a person denied it (declined it, or closed its dialog
	// without connecting), at ResolvedAt. An expired or cancelled card is
	// neither pending nor declined.
	Declined   bool
	ResolvedAt time.Time
}

// ConnectionProposer is what propose_connection needs from the server.
// internal/app implements it over the approvals and connections tables and
// the org membership, none of which this package may import.
type ConnectionProposer interface {
	// IsOrgAdmin reports whether userID is an owner or admin of orgID right
	// now (asked fresh, never from a cache). An error is an outage.
	IsOrgAdmin(ctx context.Context, userID, orgID string) (bool, error)
	// ChatProposals is every connection proposal chatID raised.
	ChatProposals(ctx context.Context, chatID string) ([]ConnectionProposalState, error)
	// Connection reads connection id of owner, or fails when owner has no
	// such connection.
	Connection(ctx context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error)
}

// ProposeConnectionParams is a propose_connection call.
type ProposeConnectionParams struct {
	Provider string `json:"provider" jsonschema:"required,enum=github,enum=slack,enum=gmail,enum=twilio,enum=http,description=The integration to connect. http is a generic HTTP API connection with an API key or a username and password."`
	Scope    string `json:"scope,omitempty" jsonschema:"enum=personal,enum=org,description=personal: the person's own connection. org: their organization's, which only its owners and admins may make. Omit to let the person choose (an org connection is offered only to an org owner or admin)."`
	Reason   string `json:"reason" jsonschema:"required,description=Why you need the connection, in one or two plain sentences. Shown to the person on the card."`
}

// ConnectionProposalResult is everything a propose_connection result carries.
// It is deliberately only status and identifiers: no token, key, OAuth code,
// account label or connection parameter ever reaches the model.
type ConnectionProposalResult struct {
	Status         string `json:"status"`
	Provider       string `json:"provider"`
	Scope          string `json:"scope,omitempty"`
	ConnectionID   string `json:"connection_id,omitempty"`
	ConnectionName string `json:"connection_name,omitempty"`
}

const proposeConnectionDescription = `Ask the person to connect an integration you need: GitHub, Slack, Gmail, Twilio, or an HTTP API (api key or username and password). It raises an approval card in the chat; the person completes the sign-in or enters the key in Reliant's connection dialog, and owns the connection.

You never see the credential. The result is only a status — connected (with the connection's id and name), declined, or not completed — and the provider. Use the connection id wherever a connection is asked for.

- scope: personal (the person's own) or org (their organization's; only its owners and admins may make one). Omit it to let the person choose.
- reason: say what you need it for; it is shown on the card.

Propose a connection only when a step needs one that is missing (search_integrations shows what is connected). Do not ask again after the person declines unless they ask you to; an identical proposal already waiting, or declined in the last few minutes, is refused. On Reliant hosting GitHub is usually connected through the Reliant GitHub App: propose github only when a saved GitHub connection is needed. Interactive chats only.`

type proposeConnectionTool struct {
	Tool
	repo     runCallerStore
	proposer ConnectionProposer
}

// Preserve the approval hook when a workflow/global setting binds parameters.
func (t *proposeConnectionTool) Bindings() Bindings { return t.Tool.(BindableTool).Bindings() }
func (t *proposeConnectionTool) WithBindings(bindings Bindings) (Tool, error) {
	bound, err := t.Tool.(BindableTool).WithBindings(bindings)
	if err != nil {
		return t, err
	}
	clone := *t
	clone.Tool = bound
	return &clone, nil
}

type proposeConnectionTypedTool struct{ outer *proposeConnectionTool }

func (t *proposeConnectionTypedTool) Name() string        { return ToolProposeConnection }
func (t *proposeConnectionTypedTool) Description() string { return proposeConnectionDescription }
func (t *proposeConnectionTypedTool) RequiresPermission(ProposeConnectionParams) (bool, error) {
	return true, nil
}

func NewProposeConnectionTool(repo runCallerStore, proposer ConnectionProposer) Tool {
	t := &proposeConnectionTool{repo: repo, proposer: proposer}
	t.Tool = NewToolWrapper[ProposeConnectionParams, ToolResponse](&proposeConnectionTypedTool{outer: t})
	return t
}

// proposal validates p for the chat and returns what its card asks for.
func (p ProposeConnectionParams) proposal(chat *core.Chat) (core.ConnectionProposal, error) {
	provider := strings.ToLower(strings.TrimSpace(p.Provider))
	name, ok := proposableIntegrations[provider]
	if !ok {
		return core.ConnectionProposal{}, fmt.Errorf("provider %q is not one of github, slack, gmail, twilio, http", p.Provider)
	}
	scope := strings.ToLower(strings.TrimSpace(p.Scope))
	if scope != "" && scope != core.ConnectionProposalPersonal && scope != core.ConnectionProposalOrg {
		return core.ConnectionProposal{}, fmt.Errorf("scope %q is not one of personal, org", p.Scope)
	}
	reason := flattenLine(p.Reason)
	if reason == "" {
		return core.ConnectionProposal{}, errors.New("reason is required: say why you need the connection")
	}
	if utf8.RuneCountInString(reason) > maxProposalReason {
		reason = string([]rune(reason)[:maxProposalReason-1]) + "…"
	}
	orgID := ""
	if chat != nil && chat.OrgID != nil {
		orgID = *chat.OrgID
	}
	if scope == core.ConnectionProposalOrg && orgID == "" {
		return core.ConnectionProposal{}, errors.New("this chat is not in an organization, so there is no organization to connect for; propose a personal connection instead")
	}
	if scope == core.ConnectionProposalPersonal {
		orgID = ""
	}
	return core.ConnectionProposal{IntegrationID: provider, IntegrationName: name, Scope: scope, OrgID: orgID, Reason: reason}, nil
}

// flattenLine flattens s to one line, so a reason cannot forge a card row.
func flattenLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// connectionProposalHash binds an approval to exactly the proposal its card
// showed.
func connectionProposalHash(p core.ConnectionProposal) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return "connection:" + hex.EncodeToString(sum[:])
}

// connectionProposalTitle is the card: a lead, labelled rows and technical
// lines (the layout frontends/web/src/lib/approvalCard.ts reads). Every
// value is one line.
func connectionProposalTitle(p core.ConnectionProposal) string {
	owner := "You choose: your own connection, or your organization's if you are its owner or admin"
	switch {
	case p.Scope == core.ConnectionProposalPersonal || (p.Scope == "" && p.OrgID == ""):
		owner = "You (a personal connection)"
	case p.Scope == core.ConnectionProposalOrg:
		owner = "Your organization (its owners and admins only)"
	}
	return strings.Join([]string{
		fmt.Sprintf("The agent asks you to connect %s.", p.IntegrationName),
		"Reason: " + p.Reason,
		"Owner: " + owner,
		"Credential: You enter it in the connection dialog; the agent never sees it",
		"",
		cardDetailsHeading,
		"Operation: " + ToolProposeConnection,
		"Integration: " + p.IntegrationID,
	}, "\n")
}

// cardDetailsHeading separates a card's rows from its technical lines; keep
// it equal to DETAILS_HEADING in frontends/web/src/lib/approvalCard.ts.
const cardDetailsHeading = "Technical details"

// decode reads a call's input as Run will, bindings applied.
func (t *proposeConnectionTool) decode(call ToolCall) (ProposeConnectionParams, error) {
	input := normalizeModelInput(call.Input, t.ParamSchema())
	if bindings := t.Bindings(); len(bindings) > 0 {
		var err error
		if input, err = ApplyBindings(ToolProposeConnection, input, bindings); err != nil {
			return ProposeConnectionParams{}, RefuseActivation("tool bindings could not be applied", err)
		}
	}
	var p ProposeConnectionParams
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return ProposeConnectionParams{}, RefuseActivation("tool input is not a valid propose_connection call", err)
	}
	return p, nil
}

// PrepareAutomation implements AutomationApprovalTool: a valid proposal
// always needs its card. It refuses, server-side, an org proposal from a
// person who is not an owner or admin of the chat's organization, and a
// proposal identical to one waiting in the chat or declined in it moments ago.
func (t *proposeConnectionTool) PrepareAutomation(ctx *rctx.ToolContext, call ToolCall, unattended bool) (ActivationPreview, bool, error) {
	caller, failure := resolveRunCaller(ctx, t.repo)
	if failure != nil {
		return ActivationPreview{}, false, RefuseActivation("the calling chat could not be resolved", fmt.Errorf("%s", failure.Content))
	}
	if unattended {
		return ActivationPreview{}, false, RefuseActivation("status: not completed. Nobody is attending this run to connect an integration; nothing was proposed.", errors.New("unattended"))
	}
	if t.proposer == nil {
		return ActivationPreview{}, false, RefuseActivation("connection proposals are unavailable on this server", errors.New("no connection proposer"))
	}
	p, err := t.decode(call)
	if err != nil {
		return ActivationPreview{}, false, err
	}
	proposal, err := p.proposal(caller.chat)
	if err != nil {
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	if proposal.Scope == core.ConnectionProposalOrg {
		admin, err := t.proposer.IsOrgAdmin(ctx.Context, caller.userID, proposal.OrgID)
		if err != nil {
			return ActivationPreview{}, false, RefuseActivation("the person's role in the organization could not be checked; try again later", err)
		}
		if !admin {
			return ActivationPreview{}, false, RefuseActivation(
				"status: not allowed. Only an owner or admin of the organization can set up an org-wide connection, and the person in this chat is not one. "+
					"Propose a personal connection instead (scope personal), or ask them to have an org admin connect "+proposal.IntegrationName+" in Settings > Integrations.",
				errors.New("not an org admin"))
		}
	}
	if err := t.refuseRepeat(ctx.Context, caller.chat.ID, proposal); err != nil {
		return ActivationPreview{}, false, err
	}
	return ActivationPreview{
		Title:        connectionProposalTitle(proposal),
		Hash:         connectionProposalHash(proposal),
		CardRequired: true,
		Kind:         PreviewKindConnectionProposal,
		Connection:   &proposal,
	}, true, nil
}

// refuseRepeat refuses a proposal identical to one already waiting in the
// chat, or to one a person declined in it within proposalDeclineCooldown.
func (t *proposeConnectionTool) refuseRepeat(ctx context.Context, chatID string, proposal core.ConnectionProposal) error {
	states, err := t.proposer.ChatProposals(ctx, chatID)
	if err != nil {
		return RefuseActivation("the chat's earlier proposals could not be read; try again later", err)
	}
	key := proposal.Key()
	for _, s := range states {
		if s.Proposal.Key() != key {
			continue
		}
		if s.Pending {
			return RefuseActivation(fmt.Sprintf("status: already pending. An identical proposal to connect %s is already waiting on the person's card in this chat; wait for their answer and do not propose it again.", proposal.IntegrationName), errors.New("duplicate pending proposal"))
		}
		if s.Declined && proposalNow().Sub(s.ResolvedAt) < proposalDeclineCooldown {
			return RefuseActivation(fmt.Sprintf("status: declined. The person declined connecting %s a few minutes ago; do not propose it again unless they ask you to.", proposal.IntegrationName), errors.New("recently declined proposal"))
		}
	}
	return nil
}

func (t *proposeConnectionTypedTool) Execute(ctx *rctx.ToolContext, p ProposeConnectionParams) (ToolResponse, error) {
	o := t.outer
	caller, failure := resolveRunCaller(ctx, o.repo)
	if failure != nil {
		return *failure, nil
	}
	if o.proposer == nil {
		return NewTextErrorResponse("Connection proposals are not available in this runtime."), nil
	}
	exec := automationpolicy.From(ctx.Context)
	if exec.Unattended {
		return NewTextErrorResponse("status: not completed. Nobody is attending this run to connect an integration; nothing was proposed."), nil
	}
	proposal, err := p.proposal(caller.chat)
	if err != nil {
		return NewTextErrorResponse(err.Error() + ". Nothing was proposed."), nil
	}
	callID := ""
	if call, ok := ctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
		callID = call.CurrentToolCallID
	}
	if exec.Approvals[callID] != connectionProposalHash(proposal) {
		return NewTextErrorResponse("status: not completed. A connection proposal needs the person's approval card in an interactive chat; no connection was made."), nil
	}
	notCompleted := func() (ToolResponse, error) {
		return NewTextErrorResponse(fmt.Sprintf("status: not completed. No %s connection was made.", proposal.IntegrationName)), nil
	}
	id := exec.Connections[callID]
	if id == "" {
		return notCompleted()
	}
	conn, scope := o.approvedConnection(ctx.Context, caller.userID, proposal, id)
	if conn == nil {
		return notCompleted()
	}
	result := ConnectionProposalResult{
		Status:         "connected",
		Provider:       proposal.IntegrationID,
		Scope:          scope,
		ConnectionID:   conn.ID,
		ConnectionName: conn.Name,
	}
	return WithResponseMetadata(NewTextResponse(connectionProposalText(result)), result), nil
}

// approvedConnection reads the connection the person made and checks it is
// the one the proposal asked for: the integration, and an owner the scope
// allows (the person, or the chat's organization). The approval already
// checked this; a run never trusts it blindly.
func (o *proposeConnectionTool) approvedConnection(ctx context.Context, userID string, proposal core.ConnectionProposal, id string) (*core.Connection, string) {
	owners := []core.ConnectionOwner{}
	if proposal.Scope != core.ConnectionProposalOrg {
		owners = append(owners, core.UserConnectionOwner(userID))
	}
	if proposal.Scope != core.ConnectionProposalPersonal && proposal.OrgID != "" {
		owners = append(owners, core.OrgConnectionOwner(proposal.OrgID))
	}
	for _, owner := range owners {
		conn, err := o.proposer.Connection(ctx, owner, id)
		if err != nil || conn == nil || conn.IntegrationID != proposal.IntegrationID {
			continue
		}
		scope := core.ConnectionProposalPersonal
		if owner.Kind == core.ConnectionOwnerOrg {
			scope = core.ConnectionProposalOrg
		}
		return conn, scope
	}
	return nil, ""
}

// connectionProposalText is the model's result: the status and identifiers
// only, never a credential.
func connectionProposalText(r ConnectionProposalResult) string {
	return fmt.Sprintf("status: %s\nprovider: %s\nscope: %s\nconnection_id: %s\nconnection_name: %s",
		r.Status, r.Provider, r.Scope, r.ConnectionID, r.ConnectionName)
}
