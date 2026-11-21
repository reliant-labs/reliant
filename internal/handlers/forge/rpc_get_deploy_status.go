package forge

import (
	"context"
	"encoding/json"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// forgeDeployStatusReply is forge.deploy_status's daemon reply.
type forgeDeployStatusReply struct {
	forgeReportReply

	Handle          string `json:"handle"`
	Env             string `json:"env"`
	JobStatus       string `json:"job_status"`
	JobStatusDetail string `json:"job_status_detail"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`

	// PlanOnly marks a job whose report is a PLAN, not a deploy.
	PlanOnly bool `json:"plan_only"`

	// Refused is forge's own refusal, lifted out of the report by the
	// daemon: plan_stale carries the recomputed plan, plan_unacknowledged
	// the codes still needing a decision.
	Refused *struct {
		Reason         string          `json:"reason"`
		Detail         string          `json:"detail"`
		CurrentPlan    json.RawMessage `json:"current_plan"`
		Unacknowledged []string        `json:"unacknowledged"`
	} `json:"deploy_refused"`
}

// GetDeployStatus polls a running or finished deploy by handle.
//
// readsCluster=false, and that is deliberate despite the deploy itself very much
// touching a cluster: this call reads one entry from the daemon's in-memory job
// registry and nothing else. Claiming "cluster unreachable" because a poll timed
// out would diagnose the wrong thing entirely — the cluster is not what failed to
// answer, the daemon is.
//
// A HANDLE THE DAEMON DOES NOT RECOGNISE IS NOT AN ERROR. The registry is
// in-memory, so a daemon that restarted mid-apply has lost the handle for a
// deploy that may well have landed. The daemon answers that poll with job_status
// UNKNOWN and a detail saying so, and this layer passes it through as a
// successful RPC. Failing the RPC instead would render "something broke" over a
// cluster that is converging; reporting FAILED would invite a retry.
func (s *Service) GetDeployStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetForgeDeployStatusRequest],
) (*connect.Response[reliantv1.GetForgeDeployStatusResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	handle := strings.TrimSpace(req.Msg.Handle)
	if handle == "" {
		return nil, svcerr.InvalidArgument("handle is required")
	}
	// The project is resolved even though the daemon keys jobs by handle alone:
	// it is what enforces that the caller owns the project whose daemon is being
	// asked, and it is how the request reaches the right daemon at all — the
	// job registry is in memory on the machine that started the deploy, which
	// is the machine forgeTarget pinned the start to.
	target, err := s.forgeTarget(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		Handle string `json:"handle"`
	}{Handle: handle}

	var reply forgeDeployStatusReply
	if err := s.daemon(target).Dispatch(ctx, userID, "forge.deploy_status", payload, &reply, forgeDeployStatusTimeoutMs); err != nil {
		return nil, s.dispatchError(ctx, target, "forge.deploy_status", err)
	}

	// Forge's refusal, when it raised one. Carried as a FIELD rather than as
	// an error because the poll succeeded and the job has a terminal state to
	// report — see the proto comment. job_status is FAILED alongside it.
	var refusal *reliantv1.ForgeDeployRefusal
	if reply.Refused != nil {
		refusal = &reliantv1.ForgeDeployRefusal{
			Reason:          forgeDeployRefusalReason(reply.Refused.Reason),
			Detail:          reply.Refused.Detail,
			CurrentPlanJson: string(reply.Refused.CurrentPlan),
			Unacknowledged:  reply.Refused.Unacknowledged,
		}
	}

	return connect.NewResponse(&reliantv1.GetForgeDeployStatusResponse{
		Meta:            forgeMeta(reply.forgeReportReply, false, ""),
		Handle:          reply.Handle,
		Env:             reply.Env,
		JobStatus:       forgeDeployJobStatus(reply.JobStatus),
		JobStatusDetail: reply.JobStatusDetail,
		StartedAt:       reply.StartedAt,
		FinishedAt:      reply.FinishedAt,
		ReportJson:      reply.reportJSON(),
		PlanOnly:        reply.PlanOnly,
		Refusal:         refusal,
	}), nil
}
