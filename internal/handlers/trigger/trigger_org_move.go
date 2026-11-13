// Copyright (c) 2025 Reliant Labs

package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ResumeAfterOrgMove turns a trigger a project move paused
// (core.TriggerPausedOrgMove) back on in its new org, when it checks out
// there as the owner turning it on would (control-plane
// docs/design/org-project-move.md §3.3): the owner may write its project in
// that org, its setup is complete, its integration access holds, and an org
// GitHub App installation it listens through is linked to that org and not
// suspended. It reports why not otherwise, and leaves the trigger paused with
// the move's reason, where the inbox's "needs setup" item shows it.
//
// It acts as the trigger's owner, never as whoever runs the move's saga, and
// turns the trigger on only while the move's pause still stands: a person
// who resumed, paused or edited it meanwhile decided.
func (s *Service) ResumeAfterOrgMove(ctx context.Context, t *core.Trigger) (resumed bool, why string, err error) {
	if t == nil || t.Enabled || t.PausedReason != core.TriggerPausedOrgMove {
		return false, "not paused by the move", nil
	}
	ctx = auth.WithUser(ctx, t.UserID, "", "")
	// The same read SetTriggerEnabled's requireProjectWritable makes: the
	// owner, a live member of the project's (new) org.
	if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, t.ProjectID, t.UserID); err != nil {
		if errors.Is(err, authz.ErrMembershipUnavailable) {
			return false, "", fmt.Errorf("check the owner's organization: %w", err)
		}
		if errors.Is(err, core.ErrProjectNotFound) {
			return false, "its owner may not run work in the project's organization", nil
		}
		return false, "", fmt.Errorf("read the trigger's project: %w", err)
	}
	if err := s.requireSetupComplete(ctx, t); err != nil {
		return false, err.Error(), nil
	}
	if ok, why, err := s.installationLinked(ctx, t); err != nil || !ok {
		return false, why, err
	}
	enabling := *t
	enabling.Enabled = true
	if err := s.refreshTriggerAccess(ctx, &enabling); err != nil {
		return false, err.Error(), nil
	}
	resumed, err = s.deps.Repo.ResumeTriggerPausedBy(ctx, t.ID, core.TriggerPausedOrgMove)
	if err != nil || !resumed {
		return false, "its pause changed while the move ran", err
	}
	// A polled or scheduled trigger converges its schedule from the row.
	if err := s.sync(ctx, t.Kind, t.ID); err != nil {
		return true, "", fmt.Errorf("resume the trigger's schedule: %w", err)
	}
	return true, "", nil
}

// installationLinked reports whether an org GitHub App installation the
// trigger listens through is linked to the trigger's org and live. A trigger
// bound to none passes. Without a way to ask (self-hosted, or a process with
// no control plane) it fails closed.
func (s *Service) installationLinked(ctx context.Context, t *core.Trigger) (bool, string, error) {
	if t.Kind != core.TriggerKindIntegration {
		return true, "", nil
	}
	var cfg core.IntegrationConfig
	if json.Unmarshal(t.Config, &cfg) != nil || cfg.Installation == "" {
		return true, "", nil
	}
	id, err := strconv.ParseInt(cfg.Installation, 10, 64)
	if err != nil || t.OrgID == nil || *t.OrgID == "" || s.deps.OrgGitHubApps == nil {
		return false, "it listens through a GitHub App installation this organization cannot check", nil
	}
	insts, err := s.deps.OrgGitHubApps.ListOrgGitHubInstallations(ctx, *t.OrgID)
	if err != nil {
		return false, "", fmt.Errorf("read the organization's GitHub App installations: %w", err)
	}
	for _, in := range insts {
		if in.InstallationID == id && !in.Suspended {
			return true, "", nil
		}
	}
	return false, "it listens through a GitHub App installation that is not linked to this organization", nil
}
