package forge

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// ListSecrets returns declared/present/inert secrets for an environment.
//
// SECRET-SAFETY IS THE POINT OF THIS METHOD'S SHAPE. `forge secret list --json`
// cannot emit a secret value (its report type graph holds only names, booleans
// and coordinates, pinned by a reflection test), and the daemon withholds
// forge's stderr entirely on this path so a diagnostic line cannot become the
// leak channel. This layer is the last link, so it holds the same line:
//
//   - the response body is NEVER logged, here or anywhere on this path — not at
//     debug level, not in an error, not in telemetry;
//   - no field is added beyond meta + forge's document, so there is nowhere to
//     put a value even by accident
//     (TestListForgeSecretsResponseCannotCarrySecretValues pins that);
//   - dispatch errors are returned as-is from the shared mapper and are built
//     from transport text, not from the report body.
func (s *Service) ListSecrets(
	ctx context.Context,
	req *connect.Request[reliantv1.ListForgeSecretsRequest],
) (*connect.Response[reliantv1.ListForgeSecretsResponse], error) {
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

	// readsCluster=false: secret presence is resolved from the declaration and
	// the provider's metadata, not by reading cluster workloads, so a timeout
	// here is a real failure rather than a reachability verdict.
	reply, unreachable, err := s.forgeDispatch(ctx, userID, target, "forge.secret_list", payload, forgeSecretListTimeoutMs, false)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.ListForgeSecretsResponse{
		Meta:       forgeMeta(reply, false, unreachable),
		ReportJson: reply.reportJSON(),
	}), nil
}
