// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ErrDraftNeedsConsent is a draft that only the caller's own AI account could
// make, in an org whose accounts cannot, with no consent to spending it on
// record (org-provider-accounts §10). Drafting never asks.
var ErrDraftNeedsConsent = errors.New("drafting needs your own account, which you have not agreed to use in this organization")

// draftTimeout bounds how long a person waits at the dialog for a draft.
const draftTimeout = 60 * time.Second

// DraftAutomation proposes an automation for the caller to review in the
// automation dialog. It stores nothing and creates nothing (ui-review report
// 03, §6.7 option B): where it runs and as whom stay the person's choice, and
// only their Create makes a trigger.
func (s *Service) DraftAutomation(
	ctx context.Context,
	req *connect.Request[pb.DraftAutomationRequest],
) (*connect.Response[pb.DraftAutomationResponse], error) {
	userID := auth.MustGetUserID(ctx)
	msg := req.Msg
	description := strings.TrimSpace(msg.GetDescription())
	if description == "" {
		return nil, svcerr.InvalidArgument("describe the automation: when it should run and what it should do")
	}
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, msg.GetProjectId(), userID)
	if svcerr.IsNotFound(err) {
		return nil, svcerr.NotFound("project")
	} else if err != nil {
		return nil, triggerDBError("get project", err)
	}
	// The draft is the project's org's work: its accounts first, the
	// caller's own only with their consent on record (§10).
	var orgID string
	if project != nil && project.OrgID != nil {
		orgID = *project.OrgID
	}
	if tz := strings.TrimSpace(msg.GetTimezone()); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return nil, svcerr.InvalidArgument("unknown time zone " + tz)
		}
	}
	if s.deps.Drafter == nil {
		return nil, svcerr.Unavailable("drafting automations is not available on this server; fill in the form instead")
	}

	ctx, cancel := context.WithTimeout(ctx, draftTimeout)
	defer cancel()
	draft, err := s.deps.Drafter.DraftAutomation(ctx, DraftRequest{
		UserID:      userID,
		OrgID:       orgID,
		Description: description,
		Workflow:    strings.TrimSpace(msg.GetWorkflow()),
		Timezone:    strings.TrimSpace(msg.GetTimezone()),
	})
	if err != nil {
		s.deps.Logger.WarnContext(ctx, "draft automation failed", "error", err)
		if errors.Is(err, ErrDraftNeedsConsent) {
			return nil, svcerr.FailedPrecondition("your organization's AI accounts can't draft this right now, and drafting on your own account needs your OK in Settings › AI providers; fill in the form instead")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, svcerr.Unavailable("drafting took too long; try again, or fill in the form")
		}
		return nil, svcerr.Unavailable("couldn't draft that automation; try rewording it, or fill in the form")
	}
	return connect.NewResponse(&pb.DraftAutomationResponse{Draft: draft}), nil
}
