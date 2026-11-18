# Agent connection proposals (`propose_connection`)

Owner decision on reliant #2831 (2026-11-18, "Approval card"): an agent may ask
a person to set up an integration connection. It may not make one, and it never
sees the credential.

## Flow

1. The agent calls `propose_connection(provider, scope?, reason)`. `provider` is
   `github`, `slack`, `gmail`, `twilio` or `http` (an API-key or basic HTTP
   connection). `scope` is `personal` or `org`; leaving it out lets the person
   choose. The tool is orchestrator-tier, loaded with `load_tool`, and withheld
   from unattended runs.
2. The workflow runtime previews the call with the tool-call approval flow that
   `manage_machine` and the activation tools use
   (`internal/workflow/runtime/activation_approval.go`). `PrepareAutomation`
   (`internal/llm/tools/connection_proposal_tool.go`) refuses, on the server:
   - an `org` proposal from someone who is not an owner or admin of the chat's
     organization (the role is read fresh from control-plane);
   - an `org` proposal in a chat with no organization;
   - a proposal identical to one already pending in the chat, or to one the
     person declined in the last 10 minutes. Identical means the same
     integration, scope and org, whatever the reason.

   Identical calls in one turn raise one card, and the rest are answered
   "already pending" (`dedupeConnectionCards`).
3. `ApprovalCreate` records the proposal in the approval's metadata
   (`connection_proposal`). `Approval.connection_proposal` carries it to
   clients.
4. The web card (`components/Chat/ConnectionProposalCard.tsx`) opens the
   existing `ConnectIntegrationDialog`, preselected to the integration and, for
   an org admin who chooses it, the org. Making the connection is the approval:
   `Approve` is called with `connection_id`.
   - On the desktop, and for API-key and basic auth, the dialog calls
     `onConnected`.
   - A web OAuth sign-in leaves the page. It returns through
     `/connections/oauth/callback`, which approves the card named by the
     `connection_proposal` return-path parameter.
   - Closing the dialog without connecting denies the card with
     `action_taken: not_completed`. Decline denies it plainly.
   - The Inbox can decline a proposal, or open its chat to connect it.
5. `ApprovalService.Approve` (`internal/handlers/approval/connection_proposal.go`)
   rejects a proposal approved without `connection_id`, or as part of a batch.
   It checks the connection against the proposal:
   - it must be for the proposed integration;
   - it must belong to the approver, who is the chat's owner, or to the chat's
     organization;
   - an org connection also requires the approver to be an org owner or admin
     (`authz.RequireOrgAdmin`).

   It records `connection_id` on the approval and signals it to the run.
6. The run records the connection id for the call (`ApprovedConnections`).
   The tool reads the connection's metadata back, checks it again, and answers
   the model with status, provider, scope, connection id and name, and nothing
   else (`ConnectionProposalResult`; `TestProposeConnectionCarriesNoSecretField`).
   Every other answer is a status line: `declined`, `not completed`,
   `already pending`, or `not allowed`.

## Ownership

The approver owns the connection. Only a chat's owner can decide its approvals,
so the approver is always the person the run acts for. An org connection is the
org's (`owner_kind org`), and the connection service records the admin who
created it.

## Compatibility

Everything here is additive:

- `Approval.connection_proposal`, `ApproveRequest.connection_id` and
  `ConnectionProposal` / `ConnectionProposalScope` are new proto fields and
  types.
- An older client shows the card's title and an Approve button. Approving
  without a connection is refused with reason `connection_required`, so the
  person can still decline it.
