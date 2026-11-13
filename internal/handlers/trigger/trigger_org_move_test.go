// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
)

// movedTrigger is a trigger a project move paused, now in org.
func movedTrigger(t *testing.T, org, installation string) *core.Trigger {
	t.Helper()
	cfg, err := json.Marshal(core.IntegrationConfig{Integration: "github", Events: []string{"issues.opened"}, Installation: installation})
	require.NoError(t, err)
	return &core.Trigger{ID: "t-1", UserID: "alice", ProjectID: "p-1", OrgID: &org, Kind: core.TriggerKindIntegration,
		Config: cfg, PausedReason: core.TriggerPausedOrgMove}
}

// A trigger a move paused is turned back on, as its owner, only when it
// checks out in its new org; otherwise it stays paused and says why.
func TestResumeAfterOrgMove(t *testing.T) {
	type outcome struct {
		resumed bool
		asked   bool // ResumeTriggerPausedBy was called
	}
	for name, tc := range map[string]struct {
		trigger    *core.Trigger
		projectErr error
		apps       OrgGitHubApps
		pauseHeld  bool
		want       outcome
	}{
		"it checks out": {trigger: movedTrigger(t, "acme", ""), pauseHeld: true, want: outcome{resumed: true, asked: true}},
		"bound to an installation the org has linked": {trigger: movedTrigger(t, "acme", "4242"), apps: linkedApps(), pauseHeld: true, want: outcome{resumed: true, asked: true}},
		"bound to an installation the org has not":    {trigger: movedTrigger(t, "globex", "4242"), apps: linkedApps()},
		"bound to a suspended installation":           {trigger: movedTrigger(t, "acme", "7"), apps: linkedApps()},
		"bound to an installation nobody can check":   {trigger: movedTrigger(t, "acme", "4242")},
		"its owner may not work in the new org":       {trigger: movedTrigger(t, "acme", ""), projectErr: core.ErrProjectNotFound},
		"a person changed its pause meanwhile":        {trigger: movedTrigger(t, "acme", ""), pauseHeld: false, want: outcome{asked: true}},
		"paused by a person, not the move": {trigger: func() *core.Trigger {
			tr := movedTrigger(t, "acme", "")
			tr.PausedReason = ""
			return tr
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			var asked bool
			repo := &MockRepository{
				GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
					if tc.projectErr != nil {
						return nil, tc.projectErr
					}
					require.Equal(t, "alice", userID, "decided as the trigger's owner")
					return &db.Project{ID: id, UserID: userID}, nil
				},
				ResumeTriggerPausedByFunc: func(_ context.Context, id, reason string) (bool, error) {
					asked = true
					assert.Equal(t, core.TriggerPausedOrgMove, reason)
					return tc.pauseHeld, nil
				},
			}
			svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo, OrgGitHubApps: tc.apps}))
			resumed, why, err := svc.ResumeAfterOrgMove(context.Background(), tc.trigger)
			require.NoError(t, err)
			assert.Equal(t, tc.want, outcome{resumed: resumed, asked: asked}, "why: %s", why)
			if !resumed {
				assert.NotEmpty(t, why)
			}
		})
	}

	// An outage is an error, never a verdict: the move retries.
	repo := &MockRepository{GetProjectWithUserCheckFunc: func(context.Context, string, string) (*db.Project, error) {
		return &db.Project{ID: "p-1", UserID: "alice"}, nil
	}}
	down := &StubOrgGitHubApps{ListOrgGitHubInstallationsFunc: func(context.Context, string) ([]gitcredentialclient.OrgInstallation, error) {
		return nil, errors.New("control-plane is down")
	}}
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo, OrgGitHubApps: down}))
	_, _, err := svc.ResumeAfterOrgMove(context.Background(), movedTrigger(t, "acme", "4242"))
	require.Error(t, err)
}
