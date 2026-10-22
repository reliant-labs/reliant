package settings_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
)

// memorySettings is the caller's settings rows, by key, behind a StubRepo
// for the scaffold tests that need no database. Refetch notifications are
// accepted and dropped.
type memorySettings map[string]*db.Setting

func (m memorySettings) repo() *settings.StubRepo {
	return &settings.StubRepo{
		GetSettingFunc: func(_ context.Context, _ string, _ *string, key string) (*db.Setting, error) {
			if s, ok := m[key]; ok {
				cp := *s
				return &cp, nil
			}
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("setting"), key) // the store's shape
		},
		ListSettingsFunc: func(context.Context, string, *string) ([]*db.Setting, error) {
			out := make([]*db.Setting, 0, len(m))
			for _, s := range m {
				out = append(out, s)
			}
			return out, nil
		},
		CreateSettingFunc: func(_ context.Context, s *db.Setting) error {
			m[s.Key] = s
			return nil
		},
		UpdateSettingFunc: func(_ context.Context, s *db.Setting) error {
			m[s.Key] = s
			return nil
		},
		DeleteSettingFunc: func(_ context.Context, id string) error {
			for k, s := range m {
				if s.ID == id {
					delete(m, k)
				}
			}
			return nil
		},
		EmitUserRefetchFunc: func(context.Context, string, db.RefetchType, db.RefetchOpts) error { return nil },
	}
}

// newSettingsService is the service over repo through the generated harness.
// The control plane and the code exchanges are stand-ins that fail loudly if a row
// reaches them. The Copilot poll is the one the real flow has: it fails on a cancelled
// context before anything leaves the process.
func newSettingsService(t *testing.T, repo settings.Repository) *settings.Service {
	t.Helper()
	return settings.NewTestSettings(t, settings.WithSettingsDeps(settings.Deps{
		Repo: repo,
		PollCopilotDevice: func(ctx context.Context, _ string) (*copilot.GitHubTokens, error) {
			return nil, ctx.Err()
		},
	}), settings.WithPlaceholderDeps())
}
