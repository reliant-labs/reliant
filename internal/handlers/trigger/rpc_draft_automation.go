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
	if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, msg.GetProjectId(), userID); svcerr.IsNotFound(err) {
		return nil, svcerr.NotFound("project")
	} else if err != nil {
		return nil, triggerDBError("get project", err)
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
		Description: description,
		Workflow:    strings.TrimSpace(msg.GetWorkflow()),
		Timezone:    strings.TrimSpace(msg.GetTimezone()),
	})
	if err != nil {
		s.deps.Logger.WarnContext(ctx, "draft automation failed", "error", err)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, svcerr.Unavailable("drafting took too long; try again, or fill in the form")
		}
		return nil, svcerr.Unavailable("couldn't draft that automation; try rewording it, or fill in the form")
	}
	return connect.NewResponse(&pb.DraftAutomationResponse{Draft: draft}), nil
}
