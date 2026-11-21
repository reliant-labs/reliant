package forge

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// rememberForgeProjectName persists the name forge's topology report gives the
// project, when it differs from the row's.
//
// projects.forge_project_name is the key the web joins a project to its
// control-plane environments on, and it must be readable with the daemon
// offline — so every time forge DOES answer, the row is brought up to date.
// This is what backfills projects created before the column existed, and what
// follows a rename in forge.yaml. The comparison against the row already in
// hand keeps the steady state write-free; the store's own guard keeps a race
// idempotent. Best effort: a failed write is logged, never the RPC's failure.
//
// This is the one field this layer reads out of forge's otherwise opaque
// report. Only `project` is decoded, so a newer forge's additions still pass
// through untouched.
func (s *Service) rememberForgeProjectName(ctx context.Context, project *db.Project, userID string, reply forgeReportReply) {
	if !reply.IsForgeProject || !reply.Supported || len(reply.Report) == 0 {
		return
	}
	var named struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(reply.Report, &named); err != nil {
		return
	}
	name := strings.TrimSpace(named.Project)
	if name == "" {
		return
	}
	if project.IsForge && project.ForgeProjectName != nil && *project.ForgeProjectName == name {
		return
	}
	if _, err := s.deps.Repo.SetProjectForgeName(ctx, project.ID, userID, name); err != nil {
		logging.WarnContext(ctx, "forge: failed to persist forge project name", "error", err, "project_id", project.ID)
	}
}

// forgeAppsRefreshEvery is how often a topology read re-reads the forge apps
// of the project's repos: the Forge tab reads topology on every focus, and
// forge.yaml files come and go far more rarely.
const forgeAppsRefreshEvery = 10 * time.Minute

// forgeAppsRefreshTimeout bounds one background refresh.
const forgeAppsRefreshTimeout = 45 * time.Second

// refreshForgeApps re-reads every forge app in the repos of the checkout that
// just answered a topology read (projectfacts.RefreshForgeApps), at most every
// forgeAppsRefreshEvery. It runs in the background, detached from the RPC:
// the read is a walk of the checkout on the daemon, and the topology answer
// must not wait on it. Best effort; nothing here reaches the caller.
func (s *Service) refreshForgeApps(ctx context.Context, userID string, answered projectcheckout.Target) {
	if answered.Project == nil || answered.DaemonID == "" || answered.Path == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgeAppsRefreshTimeout)
	go func() {
		defer cancel()
		projectfacts.RefreshForgeApps(ctx, s.deps.Router, s.deps.Repo,
			userID, answered.DaemonID, answered.Project.ID, answered.Path, forgeAppsRefreshEvery)
	}()
}

// =============================================================================
// RPCs
// =============================================================================

// GetTopology returns the release/environment topology.
func (s *Service) GetTopology(
	ctx context.Context,
	req *connect.Request[reliantv1.GetForgeTopologyRequest],
) (*connect.Response[reliantv1.GetForgeTopologyResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	target, err := s.forgeTarget(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	// --verify is what turns this from a local file read into a fleet of
	// cluster round trips, so it selects both the budget and whether a
	// timeout is a reachability verdict.
	verify := req.Msg.Verify
	timeout := int32(forgeTopologyTimeoutMs)
	if verify {
		timeout = forgeTopologyVerifyTimeoutMs
	}

	payload := func(projectPath string) any {
		return struct {
			ProjectPath string   `json:"project_path"`
			Verify      bool     `json:"verify,omitempty"`
			Envs        []string `json:"envs,omitempty"`
			OrgID       string   `json:"org_id,omitempty"`
			ProjectID   string   `json:"project_id,omitempty"`
		}{ProjectPath: projectPath, Verify: verify, Envs: req.Msg.Envs, OrgID: req.Msg.GetOrgId(), ProjectID: target.Project.ID}
	}

	reply, unreachable, answered, err := s.forgeReadDispatch(ctx, userID, target, "forge.topology", payload, timeout, verify)
	if err != nil {
		return nil, err
	}
	s.rememberForgeProjectName(ctx, answered.Project, userID, reply)
	s.refreshForgeApps(ctx, userID, answered)

	return connect.NewResponse(&reliantv1.GetForgeTopologyResponse{
		Meta:       forgeMeta(reply, verify, unreachable),
		ReportJson: reply.reportJSON(),
	}), nil
}
