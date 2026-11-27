// Copyright (c) 2025 Reliant Labs
package grants

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/approvalkind"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// What reliant/pipeline.request_grant@1 answers in its output's status.
const (
	// RequestGranted: every capability asked for is already granted.
	RequestGranted = "granted"
	// RequestPending: a card asking for them is already waiting for the
	// owner.
	RequestPending = "pending"
	// RequestRequested: a new card now asks the owner.
	RequestRequested = "requested"
	// RequestDenied: this execution's card was denied, expired or closed.
	RequestDenied = "denied"
)

// Handlers binds the grant actions, by canonical name, for
// serveraction.Config.Handlers.
func (s *Service) Handlers() map[string]serveraction.Handler {
	return map[string]serveraction.Handler{
		serveraction.PipelineRequestGrant: serveraction.HandlerFunc(s.RequestGrant),
	}
}

// RequestGrant is reliant/pipeline.request_grant@1: it asks the run's
// owner, with an approval card in the run's chat, to grant capabilities to
// the run's activation on the activation's machine pool. It never grants
// anything itself, and the run does not wait on the card: the owner may
// decide it later, from any device, and approving it writes the grants
// (GrantApproved). Asking again while a card is waiting, or for what is
// already granted, raises no second card.
//
// It is refused for a run no activation launched, an activation on no
// machine pool, and a run, activation or pool with different owners.
func (s *Service) RequestGrant(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	if s.deps.Cards == nil {
		return nil, serveraction.Errorf(serveraction.CodeUnavailable, "capability grants cannot be requested on this server")
	}
	pr, inv := req.Principal, req.Invocation
	if pr.TriggerID == "" {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied,
			"only a run an activation launched can ask for a pipeline capability: the grant is made to the activation")
	}
	if inv.ChatID == "" || pr.UserID == "" {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "the run has no chat to ask its owner in")
	}
	caps, err := requestedCapabilities(req.Params["capabilities"])
	if err != nil {
		return nil, err
	}
	trigger, err := s.deps.Store.PipelineGrantFactsFor(ctx, pr.TriggerID, "")
	if err != nil {
		return nil, err
	}
	if !trigger.TriggerFound {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "the run's activation no longer exists")
	}
	if trigger.TriggerPoolID == "" {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition,
			"activation %q runs on no machine pool; a pipeline capability is granted on a pool, so set the activation's machine to a pool first", trigger.TriggerName)
	}
	facts, err := s.deps.Store.PipelineGrantFactsFor(ctx, pr.TriggerID, trigger.TriggerPoolID)
	if err != nil {
		return nil, err
	}
	if !facts.PoolFound || !authz.CanAccessPersonal(pr.UserID, facts.TriggerUserID) || !authz.CanAccessPersonal(pr.UserID, facts.PoolUserID) {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied,
			"the run, its activation and its machine pool must have one owner to ask for a pipeline capability")
	}
	repoID, repoName, err := s.requestRepo(ctx, req)
	if err != nil {
		return nil, err
	}
	poolID := trigger.TriggerPoolID

	// A retried execution answers with the card it raised.
	entityID := "pipeline_capability_grant:" + inv.ExecutionKey
	if inv.ExecutionKey == "" {
		entityID = "pipeline_capability_grant:" + uuid.NewString()
	}
	if card, err := s.deps.Store.GetCapabilityGrantCard(ctx, entityID); err != nil {
		return nil, err
	} else if card != nil {
		return cardResult(card, caps), nil
	}
	if granted, ids, err := s.alreadyGranted(ctx, pr.TriggerID, poolID, repoID, caps); err != nil {
		return nil, err
	} else if granted {
		return &serveraction.Result{
			Content: fmt.Sprintf("Activation %q already holds %s on machine pool %q.", facts.TriggerName, joinCaps(caps), facts.PoolName),
			Data:    map[string]any{"status": RequestGranted, "capabilities": capStrings(caps), "grant_ids": ids},
		}, nil
	}
	pending, err := s.deps.Store.ListPendingCapabilityGrantCards(ctx, pr.TriggerID)
	if err != nil {
		return nil, err
	}
	for _, card := range pending {
		r := card.CapabilityGrantRequest()
		if r.MachinePoolID == poolID && r.RepoID == repoID && covers(r.Capabilities, caps) {
			return cardResult(card, caps), nil
		}
	}

	reason, _ := req.Params["reason"].(string)
	meta := core.CapabilityGrantRequest{
		Kind: core.ApprovalKindPipelineCapabilityGrant, TriggerID: pr.TriggerID, MachinePoolID: poolID,
		RepoID: repoID, Capabilities: caps, RequestedByRunID: inv.RunID,
	}
	card, err := s.raiseCard(ctx, inv, entityID, meta, grantCard{
		Activation: facts.TriggerName, TriggerID: pr.TriggerID, Pool: facts.PoolName, PoolID: poolID,
		Repository: repoName, RepoID: repoID, Capabilities: caps, Reason: reason, RunID: inv.RunID,
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "pipeline capability grant requested", "approval_id", card.ID, "trigger_id", pr.TriggerID,
		"machine_pool_id", poolID, "repo_id", repoID, "capabilities", capStrings(caps), "run_id", inv.RunID, "chat_id", inv.ChatID)
	return &serveraction.Result{
		Content: fmt.Sprintf("Asked the owner to grant activation %q %s on machine pool %q: approve the card in this chat.",
			facts.TriggerName, joinCaps(caps), facts.PoolName),
		Data: map[string]any{"status": RequestRequested, "approval_id": card.ID, "capabilities": capStrings(caps)},
	}, nil
}

// requestRepo is the enrolled repository the request names, which must be
// the run's project's, in its org: ("", "", nil) for none.
func (s *Service) requestRepo(ctx context.Context, req *serveraction.Request) (string, string, error) {
	id, _ := req.Params["repo_id"].(string)
	if id = strings.TrimSpace(id); id == "" {
		return "", "", nil
	}
	repo, err := s.deps.Cards.GetPipelineRepo(ctx, id)
	if err != nil {
		return "", "", err
	}
	pr := req.Principal
	if repo == nil || pr.ProjectID == "" || repo.ProjectID != pr.ProjectID || repo.OrgID != pr.OrgID {
		return "", "", serveraction.Errorf(serveraction.CodeNotFound, "no enrolled repository %s in this run's project", id)
	}
	return repo.ID, repo.Repository, nil
}

// alreadyGranted reports whether every capability is live for the
// activation on the pool, for the repository scope, with the grants' ids.
func (s *Service) alreadyGranted(ctx context.Context, triggerID, poolID, repoID string, caps []Capability) (bool, []string, error) {
	var ids []string
	for _, c := range caps {
		live, err := s.deps.Store.ListLivePipelineCapabilityGrants(ctx, triggerID, c, repoID)
		if err != nil {
			return false, nil, err
		}
		i := slices.IndexFunc(live, func(g *core.PipelineCapabilityGrant) bool { return g.MachinePoolID == poolID })
		if i < 0 {
			return false, nil, nil
		}
		ids = append(ids, live[i].ID)
	}
	return true, ids, nil
}

// raiseCard writes the card, marks the chat unread and announces the card
// on the chat's feed, in one transaction. The card waits on no run (it has
// no workflow to signal), so the stale-approval sweep leaves it pending
// until the owner decides it or the chat is archived.
func (s *Service) raiseCard(ctx context.Context, inv serveraction.Invocation, entityID string, meta core.CapabilityGrantRequest, card grantCard) (*core.Approval, error) {
	metadata := map[string]any{}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	// The run that asked, so a client replaying the feed attributes the
	// card to it (db.AttributeApprovalUpdate).
	metadata["workflow_id"] = inv.RunID
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	metaStr := string(metaJSON)
	minRole := string(approvalkind.Owner)
	approval := &core.Approval{
		ID: uuid.NewString(), ChatID: inv.ChatID, ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL),
		EntityID: entityID, Status: int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title: card.title(), Metadata: &metaStr, CreatedAt: s.now(), MinRole: &minRole,
	}
	if inv.ThreadID != "" {
		thread := inv.ThreadID
		approval.ThreadID = &thread
	}
	err = s.deps.Cards.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(ctx context.Context) error {
		if err := s.deps.Cards.CreateApproval(ctx, approval); err != nil {
			return fmt.Errorf("create capability grant card: %w", err)
		}
		if err := s.deps.Cards.UpdateChatUnread(ctx, inv.ChatID, true, "approval_required"); err != nil {
			return fmt.Errorf("mark chat unread: %w", err)
		}
		update := map[string]any{
			"update_type": "approval", "id": approval.ID, "approval_type": "tool_call", "status": "pending",
			"title": approval.Title, "created_at": approval.CreatedAt.Format(time.RFC3339),
		}
		db.AttributeApprovalUpdate(update, approval)
		data, err := json.Marshal(update)
		if err != nil {
			return err
		}
		return s.deps.Cards.CreateChatUpdate(ctx, inv.ChatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL, approval.ID, string(data))
	})
	if err != nil {
		return nil, err
	}
	return approval, nil
}

// cardResult answers with an existing card's state.
func cardResult(card *core.Approval, caps []Capability) *serveraction.Result {
	status := RequestDenied
	content := "The owner did not grant it: the card was denied, expired or closed."
	switch reliantv1.ApprovalStatus(card.Status) {
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING:
		status, content = RequestPending, "A card asking the owner for it is already waiting."
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED:
		status, content = RequestGranted, "The owner approved it."
	}
	return &serveraction.Result{Content: content, Data: map[string]any{"status": status, "approval_id": card.ID, "capabilities": capStrings(caps)}}
}

// requestedCapabilities parses the action's capabilities param.
func requestedCapabilities(v any) ([]Capability, error) {
	list, _ := v.([]any)
	in := make([]Capability, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		in = append(in, Capability(s))
	}
	caps, err := capabilitiesOf(in)
	if err != nil {
		return nil, serveraction.Errorf(serveraction.CodeInvalidArgument, "capabilities must name approve, land or both")
	}
	return caps, nil
}

func covers(have, want []Capability) bool {
	for _, c := range want {
		if !slices.Contains(have, c) {
			return false
		}
	}
	return true
}

func capStrings(caps []Capability) []any {
	out := make([]any, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return out
}

func joinCaps(caps []Capability) string {
	names := make([]string, 0, len(caps))
	for _, c := range caps {
		names = append(names, string(c))
	}
	return strings.Join(names, " and ")
}
