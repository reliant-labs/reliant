package forge

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// StartDeploy begins a real deploy as a background job and returns its handle.
//
// The confirmation token is expected_declared_context plus either
// expected_current_release or expect_unbound. The daemon re-plans, compares, and
// starts nothing unless every claim still holds. Both halves are validated here
// BEFORE the project lookup so an unauthorised request costs nothing — and the
// validation is duplicated on the daemon rather than trusted from here, because
// the daemon's command must not have an unguarded spelling.
//
// A REFUSAL FAILS THE RPC with FailedPrecondition carrying a ForgeDeployRefusal
// detail. The daemon reports it as data on a successful response; this is where
// that becomes an error, because a refusal returned as a 200 with a nullable
// field is safe only if every client remembers to check it — and a client that
// forgets shows a progress spinner for a deploy that was never started.
//
// THIS RPC RETURNING OK MEANS A DEPLOY IS IN FLIGHT. It emphatically does not
// mean one succeeded; nothing is known about the outcome yet. Poll
// GetDeployStatus.
func (s *Service) StartDeploy(
	ctx context.Context,
	req *connect.Request[reliantv1.StartForgeDeployRequest],
) (*connect.Response[reliantv1.StartForgeDeployResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}

	// THE CLUSTER CLAIM IS MANDATORY, and there is no spelling of this request
	// that means "I did not populate it". A deploy whose target cluster the
	// operator never saw is the accident this whole path is built around.
	expectedContext := strings.TrimSpace(req.Msg.ExpectedDeclaredContext)
	if expectedContext == "" {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("expected_declared_context is required: deploying to %q applies manifests to a "+
			"live cluster, and forge applies to the context the environment DECLARES — which can "+
			"change between a preview and this confirmation — so the request must state the "+
			"cluster the operator saw named", env))
	}

	// And the release claim, in the same three-way shape promote uses: an unset
	// expected_current_release must never be readable as "I saw no binding".
	expectedRelease := strings.TrimSpace(req.Msg.ExpectedCurrentRelease)
	switch {
	case req.Msg.ExpectUnbound && expectedRelease != "":
		return nil, svcerr.InvalidArgument(fmt.Sprintf("expect_unbound and expected_current_release %q are contradictory: assert "+
			"either that the environment had no release binding or which release it was bound to",
			expectedRelease))
	case !req.Msg.ExpectUnbound && expectedRelease == "":
		return nil, svcerr.InvalidArgument(fmt.Sprintf("expected_current_release is required (or set expect_unbound for an "+
			"environment with no binding): the token states the binding the operator "+
			"reviewed for %q, so the request must name it", env))
	}

	// THE CONTENT CLAIM, which is what the target token above could never
	// cover. A deploy builds from the checkout and cuts a new release, so
	// without a digest the request approves whatever forge computes when it
	// runs rather than the change set a human read.
	approveDigest := strings.TrimSpace(req.Msg.ApproveDigest)
	if approveDigest == "" {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("approve_digest is required: deploying %q ships a change set, and the "+
			"request must name the one a human reviewed. Compute it with StartDeployPlan "+
			"and send that plan's digest", env))
	}
	releaseVersion := strings.TrimSpace(req.Msg.ReleaseVersion)
	if releaseVersion == "" {
		return nil, svcerr.InvalidArgument("release_version is required: it is the release the plan was computed " +
			"for, and deploying it by name is what ships those artifacts. Without it the " +
			"deploy would build and cut a second release, whose plan the approved digest " +
			"could not match")
	}
	for _, code := range req.Msg.AcknowledgedFindings {
		if strings.TrimSpace(code) == "" {
			return nil, svcerr.InvalidArgument("acknowledged_findings must not contain an empty value: each entry " +
				"names one destructive change from the approved plan")
		}
	}

	target, err := s.forgeTarget(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath             string   `json:"project_path"`
		Env                     string   `json:"env"`
		ExpectedDeclaredContext string   `json:"expected_declared_context"`
		ExpectedCurrentRelease  string   `json:"expected_current_release,omitempty"`
		ExpectUnbound           bool     `json:"expect_unbound,omitempty"`
		ApproveDigest           string   `json:"approve_digest"`
		AcknowledgedFindings    []string `json:"acknowledged_findings,omitempty"`
		ReleaseVersion          string   `json:"release_version"`
		CheckoutPath            string   `json:"checkout_path,omitempty"`
		OrgID                   string   `json:"org_id,omitempty"`
		ProjectID               string   `json:"project_id,omitempty"`
	}{
		ProjectPath:             target.Path,
		Env:                     env,
		ExpectedDeclaredContext: expectedContext,
		ExpectedCurrentRelease:  expectedRelease,
		ExpectUnbound:           req.Msg.ExpectUnbound,
		ApproveDigest:           approveDigest,
		AcknowledgedFindings:    req.Msg.AcknowledgedFindings,
		ReleaseVersion:          releaseVersion,
		CheckoutPath:            strings.TrimSpace(req.Msg.CheckoutPath),
		OrgID:                   req.Msg.GetOrgId(),
		ProjectID:               target.Project.ID,
	}

	// Dispatched directly rather than through forgeDispatch: the reply carries
	// fields the shared envelope does not, and — more importantly — the
	// reachability escape hatch must NOT apply here. A start that timed out may
	// or may not have got a deploy underway, and reporting that as
	// "cluster unknown" would file a possibly-live deploy under a diagnosis of
	// the network.
	var reply forgeDeployStartReply
	if err := s.daemon(target).Dispatch(ctx, userID, "forge.deploy_start", payload, &reply, forgeDeployStartTimeoutMs); err != nil {
		return nil, s.dispatchError(ctx, target, "forge.deploy_start", err)
	}

	// THE GUARD REFUSED: nothing was applied and no job exists. Fail closed.
	if reply.Refused != nil {
		return nil, forgeDeployRefusedError(reply.Refused)
	}

	// A reply with neither a refusal nor a handle means the daemon answered
	// something this layer cannot act on — most plausibly a forge too old for
	// `env deploy --json`, which arrives as supported:false with no job. Report
	// it as the structured not-supported answer rather than handing back an empty
	// handle a client would poll forever.
	if strings.TrimSpace(reply.Handle) == "" {
		return connect.NewResponse(&reliantv1.StartForgeDeployResponse{
			Meta:       forgeMeta(reply.forgeReportReply, false, ""),
			Env:        env,
			JobStatus:  reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNSPECIFIED,
			ReportJson: reply.reportJSON(),
		}), nil
	}

	// Started. report_json is the GUARD PLAN this deploy was authorised against
	// — the read-only document, mode "dry_run" — not an apply report. No apply
	// report exists yet.
	return connect.NewResponse(&reliantv1.StartForgeDeployResponse{
		Meta:       forgeMeta(reply.forgeReportReply, false, ""),
		Handle:     reply.Handle,
		Env:        env,
		JobStatus:  forgeDeployJobStatus(reply.JobStatus),
		StartedAt:  reply.StartedAt,
		ReportJson: reply.reportJSON(),
	}), nil
}
