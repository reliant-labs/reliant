package settings_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
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
			return nil, sql.ErrNoRows
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
// The control plane and the Claude code exchange are stand-ins that fail
// loudly if a row reaches them.
func newSettingsService(t *testing.T, repo db.Repository) *settings.Service {
	t.Helper()
	return settings.NewTestSettings(t, settings.WithSettingsDeps(settings.Deps{Repo: repo}), settings.WithPlaceholderDeps())
}
