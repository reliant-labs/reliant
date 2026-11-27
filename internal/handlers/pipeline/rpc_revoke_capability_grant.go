package pipeline

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// RevokeCapabilityGrant revokes one of the caller's grants, at once. Any
// credential of the grant's owner may revoke it: revoking only takes power
// away, so it needs neither a card nor a person's own session (a machine of
// the owner's stopping its own activation is harmless). Another user's grant
// is NotFound.
func (s *Service) RevokeCapabilityGrant(
	ctx context.Context,
	req *connect.Request[pb.RevokeCapabilityGrantRequest],
) (*connect.Response[pb.RevokeCapabilityGrantResponse], error) {
	if err := s.enabled(); err != nil {
		return nil, svcerr.Wrap(err)
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if s.deps.Grants == nil {
		return nil, svcerr.Wrap(svcerr.Unavailable("capability grants are not available on this server"))
	}
	id := strings.TrimSpace(req.Msg.GetGrantId())
	if id == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("grant_id is required"))
	}
	g, err := s.deps.Grants.Revoke(ctx, userID, id)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	s.deps.Logger.InfoContext(ctx, "pipeline: capability grant revoked", "grant_id", g.ID, "capability", string(g.Capability),
		"trigger_id", g.TriggerID, "machine_pool_id", g.MachinePoolID, "user_id", userID,
		"credential", auth.CredentialFromContext(ctx).Label())
	return connect.NewResponse(&pb.RevokeCapabilityGrantResponse{Grant: grantProto(g)}), nil
}
