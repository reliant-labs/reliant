package forge

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// PlanDeploy previews a deploy. APPLIES NOTHING.
//
// `forge env deploy <env> --dry-run` renders the env, evaluates the
// declared-cluster guard, runs the preflight against the live target and returns
// before the first kubectl apply. Read-only and idempotent, so it needs no
// confirmation token — there is nothing to confirm. It is what a UI calls to put
// the cluster name, the namespace, the image pinning and the resource list in
// front of a human before asking them to approve it.
//
// readsCluster=true, which is the difference from PlanPromote: the preflight
// really does read Secrets and image metadata off the live target, so a timeout
// here means live state is UNKNOWN rather than that something broke.
func (s *Service) PlanDeploy(
	ctx context.Context,
	req *connect.Request[reliantv1.PlanForgeDeployRequest],
) (*connect.Response[reliantv1.PlanForgeDeployResponse], error) {
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
		ProjectPath  string `json:"project_path"`
		Env          string `json:"env"`
		CheckoutPath string `json:"checkout_path,omitempty"`
		OrgID        string `json:"org_id,omitempty"`
		ProjectID    string `json:"project_id,omitempty"`
	}{ProjectPath: target.Path, Env: env, CheckoutPath: strings.TrimSpace(req.Msg.CheckoutPath), OrgID: req.Msg.GetOrgId(), ProjectID: target.Project.ID}

	reply, unreachable, err := s.forgeDispatch(
		ctx, userID, target, "forge.deploy_plan", payload, forgeDeployPlanTimeoutMs, true)
	if err != nil {
		return nil, err
	}
	// forge ran but could not authenticate to the control plane: that is
	// this rpc's error, with forge's reason (authz_refusal.go), not a report.
	if refusal := forgeAuthzRefusal("forge.deploy_plan", reply); refusal != nil {
		return nil, refusal
	}

	return connect.NewResponse(&reliantv1.PlanForgeDeployResponse{
		Meta:       forgeMeta(reply, true, unreachable),
		ReportJson: reply.reportJSON(),
	}), nil
}
