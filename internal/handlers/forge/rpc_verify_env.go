package forge

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// VerifyEnv reconciles one environment against its live cluster.
func (s *Service) VerifyEnv(
	ctx context.Context,
	req *connect.Request[reliantv1.VerifyForgeEnvRequest],
) (*connect.Response[reliantv1.VerifyForgeEnvResponse], error) {
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

	reply, unreachable, err := s.forgeDispatch(ctx, userID, target, "forge.env_verify", payload, forgeEnvVerifyTimeoutMs, true)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.VerifyForgeEnvResponse{
		Meta:       forgeMeta(reply, true, unreachable),
		ReportJson: reply.reportJSON(),
	}), nil
}
