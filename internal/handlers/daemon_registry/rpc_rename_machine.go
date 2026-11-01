package daemon_registry

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// machineName is what a machine may be called: the control plane's rule
// (internal/svcdaemon isValidName), checked here too so a bad name is refused
// before a round trip, in the same words.
var machineName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

const machineNameRule = "a machine name is 1-63 lowercase letters, digits or hyphens, and starts and ends with a letter or digit"

// RenameMachine renames one of the caller's machines: at the control plane,
// which owns the name, then on this registry row, so the list, the pickers,
// triggers and the list_machines tool show it at once rather than at the
// next registry snapshot. Only the name changes — never the id, the
// workspace or anything named after them.
func (s *Service) RenameMachine(
	ctx context.Context,
	req *connect.Request[pb.RenameMachineRequest],
) (*connect.Response[pb.RenameMachineResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	daemonID := req.Msg.GetDaemonId()
	if daemonID == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if !machineName.MatchString(name) {
		return nil, svcerr.InvalidArgument(machineNameRule)
	}
	if s.deps.Names == nil {
		return nil, svcerr.Unimplemented("renaming a machine needs the Reliant control plane, which owns machine names")
	}
	d, err := s.deps.Repo.GetDaemon(ctx, daemonID)
	if err != nil || d == nil || d.UserID != userID {
		return nil, svcerr.NotFound("machine")
	}
	if d.Name == name {
		return connect.NewResponse(&pb.RenameMachineResponse{Daemon: daemonToProto(d, nil)}), nil
	}
	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		return nil, svcerr.ErrUnauthenticated
	}
	if err := s.deps.Names.RenameDaemon(ctx, jwt, daemonID, name); err != nil {
		return nil, controlPlaneRefusal(err)
	}

	// The control plane holds the new name now, and its mirror (a lifecycle
	// event, or the registry snapshot within minutes) carries it here too.
	// Writing it ourselves is what makes it immediate; a failure here is
	// logged, not returned, because the rename itself succeeded.
	typ := ""
	if d.DaemonType != nil {
		typ = *d.DaemonType
	}
	if _, err := s.deps.Repo.UpsertDaemonIdentity(ctx, db.DaemonIdentity{
		DaemonID: daemonID, UserID: userID, Name: name, DaemonType: typ, CreatedAt: d.CreatedAt,
	}); err != nil {
		logging.WarnContext(ctx, "[DaemonRegistry] machine renamed at the control plane; the registry row takes the name at the next snapshot",
			"error", err, "userID", userID, "daemonID", daemonID)
	}
	d.Name = name
	return connect.NewResponse(&pb.RenameMachineResponse{Daemon: daemonToProto(d, nil)}), nil
}

// controlPlaneRefusal carries a control-plane refusal to the caller in its
// own words — "a machine named "x" already exists" is the answer the user
// needs. Anything that is not a refusal is an internal error.
func controlPlaneRefusal(err error) error {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return svcerr.WrapOr(err, svcerr.Internal("failed to rename the machine"))
	}
	msg := ce.Message()
	switch ce.Code() {
	case connect.CodeAlreadyExists:
		return svcerr.WithCause(svcerr.AlreadyExists(msg), err)
	case connect.CodeInvalidArgument:
		return svcerr.WithCause(svcerr.InvalidArgument(msg), err)
	case connect.CodeNotFound, connect.CodePermissionDenied:
		return svcerr.WithCause(svcerr.NotFound("machine"), err)
	case connect.CodeUnauthenticated:
		return svcerr.WithCause(svcerr.ErrUnauthenticated, err)
	default:
		return svcerr.WrapOr(err, svcerr.Internal("failed to rename the machine"))
	}
}
