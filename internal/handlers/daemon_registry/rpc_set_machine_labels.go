package daemon_registry

import (
	"context"
	"fmt"
	"regexp"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// labelSlug is what a label key or value may be: a short slug, so a label
// reads the same in a selector, a URL and a log line.
var labelSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// maxMachineLabels bounds how many labels one machine carries.
const maxMachineLabels = 32

// validateLabels refuses labels that are not short slugs.
func validateLabels(labels map[string]string, what string) error {
	if len(labels) > maxMachineLabels {
		return svcerr.InvalidArgument(fmt.Sprintf("%s: at most %d labels", what, maxMachineLabels))
	}
	for k, v := range labels {
		if !labelSlug.MatchString(k) || !labelSlug.MatchString(v) {
			return svcerr.InvalidArgument(fmt.Sprintf("%s: %q=%q: keys and values are lowercase slugs (a-z, 0-9, '.', '_', '-'), at most 63 characters", what, k, v))
		}
	}
	return nil
}

// SetMachineLabels replaces one of the caller's machines' durable labels.
func (s *Service) SetMachineLabels(
	ctx context.Context,
	req *connect.Request[pb.SetMachineLabelsRequest],
) (*connect.Response[pb.SetMachineLabelsResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	msg := req.Msg
	if msg.GetDaemonId() == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}
	if err := validateLabels(msg.GetLabels(), "labels"); err != nil {
		return nil, err
	}
	found, err := s.deps.Repo.SetDaemonLabels(ctx, userID, msg.GetDaemonId(), msg.GetLabels())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to set machine labels"))
	}
	if !found {
		return nil, svcerr.NotFound("machine")
	}
	s.applyMemberIdleTimeouts(ctx, userID, []string{msg.GetDaemonId()})
	d, err := s.deps.Repo.GetDaemon(ctx, msg.GetDaemonId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the machine"))
	}
	return connect.NewResponse(&pb.SetMachineLabelsResponse{Daemon: daemonToProto(d, nil)}), nil
}
