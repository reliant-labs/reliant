package forge

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// GetEnvShape projects one environment's render into the declaration the
// control plane records for it.
//
// This is the BOOTSTRAP hop, and it is the only reason the Register path
// touches a daemon at all: an environment declared only in the user's KCL has
// no control-plane row, so nothing can read it and nothing can hold a secret
// for it. Preview reads the shape here; the browser creates the row itself
// against control-plane, with the user's session. See the RPC comment in
// proto/reliant/v1/forge.proto, and cmd_forge_shape.go on why the daemon
// stays a transport over forge's own projection.
//
// A KCL render under the env-status budget: no cluster reads, same shape of
// work as `env status` for one environment.
func (s *Service) GetEnvShape(
	ctx context.Context,
	req *connect.Request[reliantv1.GetForgeEnvShapeRequest],
) (*connect.Response[reliantv1.GetForgeEnvShapeResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	target, err := s.forgeTarget(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
		OrgID       string `json:"org_id,omitempty"`
		ProjectID   string `json:"project_id,omitempty"`
	}{ProjectPath: target.Path, Env: env, OrgID: req.Msg.GetOrgId(), ProjectID: target.Project.ID}

	reply, unreachable, err := s.forgeDispatch(ctx, userID, target, "forge.env_shape", payload, forgeEnvStatusTimeoutMs, true)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetForgeEnvShapeResponse{
		Meta:       forgeMeta(reply, true, unreachable),
		ReportJson: reply.reportJSON(),
	}), nil
}
