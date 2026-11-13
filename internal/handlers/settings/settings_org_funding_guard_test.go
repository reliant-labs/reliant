package settings

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgfunding"
)

// The person's org funding choice is consent to spend their own AI accounts in
// an org (org-provider-accounts §10.4): only a person signed in to the app (or
// a self-host's shared key, auth.RequirePerson's rule) may set or clear it, on
// every settings write path. An agent's or a script's credential is refused
// before anything is written; other keys are not affected.
func TestOrgFundingSettingIsPersonOnly(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	svc := newTestService(t, Deps{Repo: repo})
	const user = "funding-guard"
	as := func(c auth.Credential) context.Context {
		return auth.WithCredential(context.WithValue(context.Background(), auth.UserIDContextKey, user), c)
	}
	usePersonal := orgfunding.Settings{}.With("org-acme", orgfunding.Preference{OnOrgLimit: orgfunding.OnOrgLimitUsePersonal}, nil).Value()
	stored := func() string {
		t.Helper()
		s, err := repo.GetSetting(context.Background(), user, nil, orgfunding.SettingKey)
		if err != nil {
			return ""
		}
		return s.Value
	}
	writes := map[string]func(ctx context.Context) error{
		"CreateSetting": func(ctx context.Context) error {
			_, err := svc.CreateSetting(ctx, connect.NewRequest(&reliantv1.CreateSettingRequest{Key: orgfunding.SettingKey, Value: usePersonal}))
			return err
		},
		"UpdateSetting": func(ctx context.Context) error {
			_, err := svc.UpdateSetting(ctx, connect.NewRequest(&reliantv1.UpdateSettingRequest{Key: orgfunding.SettingKey, Value: &usePersonal}))
			return err
		},
		"BatchUpsertSettings": func(ctx context.Context) error {
			_, err := svc.BatchUpsertSettings(ctx, connect.NewRequest(&reliantv1.BatchUpsertSettingsRequest{
				Settings: []*reliantv1.SettingWrite{{Key: "ui.theme", Value: "dark"}, {Key: orgfunding.SettingKey, Value: usePersonal}},
			}))
			return err
		},
		"DeleteSetting": func(ctx context.Context) error {
			_, err := svc.DeleteSetting(ctx, connect.NewRequest(&reliantv1.DeleteSettingRequest{Key: orgfunding.SettingKey}))
			return err
		},
	}
	for _, c := range []auth.Credential{auth.CredentialAccessToken, auth.CredentialOAuthClient, auth.CredentialUnknown} {
		for name, write := range writes {
			err := write(as(c))
			require.Error(t, err, "%s with %s", name, c)
			assert.True(t, svcerr.IsPermissionDenied(err), "%s with %s: %v", name, c, err)
		}
	}
	assert.Empty(t, stored(), "nothing was written")
	_, err := repo.GetSetting(context.Background(), user, nil, "ui.theme")
	assert.Error(t, err, "a refused batch writes none of its keys")

	person := as(auth.CredentialSession)
	require.NoError(t, writes["CreateSetting"](person))
	assert.Equal(t, usePersonal, stored())
	require.NoError(t, writes["DeleteSetting"](person))
	assert.Empty(t, stored())

	// Any other key is as free-form as before.
	_, err = svc.CreateSetting(as(auth.CredentialAccessToken), connect.NewRequest(&reliantv1.CreateSettingRequest{Key: "ui.theme", Value: "dark"}))
	require.NoError(t, err)
}
