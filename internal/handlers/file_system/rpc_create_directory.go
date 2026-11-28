package file_system

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// CreateDirectory creates a new directory at an arbitrary absolute path by
// forwarding an fs.mkdir command to the machine the request names, else the
// user's default machine. Mirrors ListDirectory:
// the path is an absolute filesystem path on the daemon (not project-scoped).
func (s *Service) CreateDirectory(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateDirectoryRequest],
) (*connect.Response[reliantv1.CreateDirectoryResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	path := req.Msg.Path
	if path == "" {
		return nil, svcerr.InvalidArgument("path is required")
	}
	// The path is executed on the daemon's filesystem, which may be Windows
	// even when this server is Linux, so it is judged OS-agnostically and
	// forwarded verbatim rather than being cleaned into the host's convention.
	if !ospath.IsAbs(path) {
		return nil, svcerr.InvalidArgument("path must be absolute")
	}

	cmdReq := map[string]any{
		"path": path,
	}
	target, err := s.pickerMachine(ctx, userID, req.Msg.GetDaemonId())
	if err != nil {
		return nil, err
	}
	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, target, "fs.mkdir", cmdReq, &cmdResp, pickerTimeoutMs); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CreateDirectoryResponse{
		Path:     path,
		DaemonId: pickerEcho(target),
	}), nil
}
