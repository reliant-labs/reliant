// Copyright (c) 2025 Reliant Labs
package grants

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// GrantApproved writes the grants a capability-grant card asks for, now that
// approverID approved it. It is the only way a grant is made, and it is
// called by the approval service inside the transaction that marks the card
// approved, so the card and its grants commit together or not at all.
//
// The approver must own both the activation and the pool, read now: a card
// raised before either changed hands grants nothing. Granting what is
// already granted is a no-op that returns the live grant. Errors are
// svcerr-typed for the approval service's caller.
func (s *Service) GrantApproved(ctx context.Context, approverID string, card *core.Approval) ([]*core.PipelineCapabilityGrant, error) {
	req := card.CapabilityGrantRequest()
	if req == nil {
		return nil, svcerr.InvalidArgument("this approval does not ask for a pipeline capability grant")
	}
	caps, err := capabilitiesOf(req.Capabilities)
	if err != nil {
		return nil, err
	}
	if approverID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	facts, err := s.deps.Store.PipelineGrantFactsFor(ctx, req.TriggerID, req.MachinePoolID)
	if err != nil {
		return nil, err
	}
	switch {
	case !facts.TriggerFound:
		return nil, svcerr.FailedPrecondition("the activation this card asks a grant for no longer exists; nothing was granted")
	case !facts.PoolFound:
		return nil, svcerr.FailedPrecondition("the machine pool this card asks a grant on no longer exists; nothing was granted")
	case !authz.CanAccessPersonal(approverID, facts.TriggerUserID) || !authz.CanAccessPersonal(approverID, facts.PoolUserID):
		return nil, svcerr.PermissionDenied("only the owner of both the activation and the machine pool can grant it a pipeline capability; nothing was granted")
	}
	out := make([]*core.PipelineCapabilityGrant, 0, len(caps))
	for _, c := range caps {
		g, created, err := s.deps.Store.InsertPipelineCapabilityGrant(ctx, core.PipelineCapabilityGrantNew{
			UserID: approverID, OrgID: facts.TriggerOrgID, TriggerID: req.TriggerID, MachinePoolID: req.MachinePoolID,
			RepoID: req.RepoID, Capability: c, ApprovalID: card.ID,
		})
		if err != nil {
			return nil, err
		}
		s.log.InfoContext(ctx, "pipeline capability granted", "grant_id", g.ID, "created", created, "capability", string(c),
			"trigger_id", req.TriggerID, "machine_pool_id", req.MachinePoolID, "repo_id", req.RepoID,
			"user_id", approverID, "approval_id", card.ID)
		out = append(out, g)
	}
	return out, nil
}

// List is the grants userID made, newest first: the live ones, or every one
// with includeRevoked; only triggerID's when it is set.
func (s *Service) List(ctx context.Context, userID, triggerID string, includeRevoked bool) ([]*core.PipelineCapabilityGrant, error) {
	if userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	return s.deps.Store.ListPipelineCapabilityGrantsForUser(ctx, userID, triggerID, includeRevoked)
}

// Revoke revokes userID's grant id, at once: the next Authorize refuses it.
// It needs no card, because it only takes power away. A grant already
// revoked is returned unchanged; another user's, or one that does not
// exist, is NotFound, so grant ids cannot be probed.
func (s *Service) Revoke(ctx context.Context, userID, id string) (*core.PipelineCapabilityGrant, error) {
	if userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	g, err := s.deps.Store.RevokePipelineCapabilityGrant(ctx, userID, id, s.now())
	if errors.Is(err, core.ErrCapabilityGrantNotFound) {
		return nil, svcerr.NotFound("pipeline capability grant")
	}
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "pipeline capability revoked", "grant_id", g.ID, "capability", string(g.Capability),
		"trigger_id", g.TriggerID, "machine_pool_id", g.MachinePoolID, "user_id", userID)
	return g, nil
}

// capabilitiesOf is a request's capabilities, deduplicated and in
// AllPipelineCapabilities order; InvalidArgument for none or one this
// package does not know.
func capabilitiesOf(in []core.PipelineCapability) ([]core.PipelineCapability, error) {
	if len(in) == 0 {
		return nil, svcerr.InvalidArgument("a grant names at least one capability")
	}
	for _, c := range in {
		if !slices.Contains(core.AllPipelineCapabilities, c) {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("%q is not a pipeline capability (approve or land)", c))
		}
	}
	var out []core.PipelineCapability
	for _, c := range core.AllPipelineCapabilities {
		if slices.Contains(in, c) {
			out = append(out, c)
		}
	}
	return out, nil
}
