package forge

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// PlanPromote previews binding an environment to a release. WRITES NOTHING.
//
// `forge env promote --plan` computes the whole change set and stops before
// forge's only write, so this is safe and idempotent and needs no confirmation
// token — there is nothing to confirm. It is what a UI calls to render the diff
// before asking a human to approve it.
//
// readsCluster=false: the plan is local ledger files plus a git range walk. A
// timeout here is therefore a real failure, not a reachability verdict —
// claiming "cluster unknown" for a failed local read would be a diagnosis of
// the wrong thing.
func (s *Service) PlanPromote(
	ctx context.Context,
	req *connect.Request[reliantv1.PlanForgePromoteRequest],
) (*connect.Response[reliantv1.PlanForgePromoteResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	release, err := forgeReleaseArg(req.Msg.Release)
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
		Release     string `json:"release"`
		OrgID       string `json:"org_id,omitempty"`
		ProjectID   string `json:"project_id,omitempty"`
	}{ProjectPath: target.Path, Env: env, Release: release, OrgID: req.Msg.GetOrgId(), ProjectID: target.Project.ID}

	reply, unreachable, err := s.forgeDispatch(
		ctx, userID, target, "forge.promote_plan", payload, forgePromotePlanTimeoutMs, false)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.PlanForgePromoteResponse{
		Meta:       forgeMeta(reply, false, unreachable),
		ReportJson: reply.reportJSON(),
	}), nil
}
