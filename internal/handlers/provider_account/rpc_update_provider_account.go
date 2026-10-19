package provider_account

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const maxLabelRunes = 80

// UpdateProviderAccount renames and/or pauses an account. An empty label restores the default
// (the detected email). Status is derived, so nothing else is writable here.
func (s *Service) UpdateProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.UpdateProviderAccountRequest],
) (*connect.Response[pb.UpdateProviderAccountResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetAccountId() == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("account_id is required"))
	}
	patch := core.AccountPatch{Paused: req.Msg.Paused}
	if req.Msg.Label != nil {
		label := strings.TrimSpace(req.Msg.GetLabel())
		if utf8.RuneCountInString(label) > maxLabelRunes {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("label is too long"))
		}
		patch.Label = &label
	}
	acct, err := s.deps.Store.PatchProviderAccount(ctx, userID, req.Msg.GetAccountId(), patch)
	if err != nil {
		return nil, wrapAccountErr(err)
	}
	if patch.Paused != nil {
		s.afterChange(ctx, userID, acct.Provider)
	}
	limits, err := s.deps.Store.ListAccountLimits(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.UpdateProviderAccountResponse{Account: proto(acct, limits, time.Now())}), nil
}
