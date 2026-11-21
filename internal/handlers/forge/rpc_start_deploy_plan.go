package forge

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// StartDeployPlan begins the APPROVABLE plan as a background job.
//
// NO APPROVAL TOKEN, deliberately: this writes no promotion and applies
// nothing, so there is nothing to approve yet — computing the thing to approve
// IS the job. It does build and push, so the daemon refuses it while a deploy
// of the same environment is in flight, and that refusal arrives here exactly
// as StartDeploy's does.
//
// It is asynchronous for the same reason StartDeploy is, and more acutely: a
// cold multi-image build is minutes, far past any synchronous budget.
func (s *Service) StartDeployPlan(
	ctx context.Context,
	req *connect.Request[reliantv1.StartForgeDeployPlanRequest],
) (*connect.Response[reliantv1.StartForgeDeployResponse], error) {
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

	// Dispatched directly, and WITHOUT the reachability escape hatch, for the
	// same reason StartDeploy is: a start that timed out may or may not have
	// got a build underway, and reporting that as "cluster unknown" would file
	// a possibly-running job under a diagnosis of the network.
	var reply forgeDeployStartReply
	if err := s.daemon(target).Dispatch(ctx, userID, "forge.deploy_plan_start", payload, &reply,
		forgeDeployStartTimeoutMs); err != nil {
		return nil, s.dispatchError(ctx, target, "forge.deploy_plan_start", err)
	}

	if reply.Refused != nil {
		return nil, forgeDeployRefusedError(reply.Refused)
	}

	// Neither a refusal nor a handle: most plausibly a forge too old for the
	// command. Reported as the structured not-supported answer rather than an
	// empty handle a client would poll forever.
	if strings.TrimSpace(reply.Handle) == "" {
		return connect.NewResponse(&reliantv1.StartForgeDeployResponse{
			Meta:      forgeMeta(reply.forgeReportReply, false, ""),
			Env:       env,
			JobStatus: reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNSPECIFIED,
		}), nil
	}

	// NO report on this response: the plan does not exist yet. A plan here
	// would invite a client to render and approve something before the build
	// that produces it has run.
	return connect.NewResponse(&reliantv1.StartForgeDeployResponse{
		Meta:      forgeMeta(reply.forgeReportReply, false, ""),
		Handle:    reply.Handle,
		Env:       env,
		JobStatus: forgeDeployJobStatus(reply.JobStatus),
		StartedAt: reply.StartedAt,
	}), nil
}
