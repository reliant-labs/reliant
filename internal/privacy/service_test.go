package privacy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/privacy"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func newService(t *testing.T, env *storetest.Env) privacy.Service {
	t.Helper()
	svc, err := privacy.New(privacy.Deps{Settings: store.NewSettingExtStore(env.ORM)})
	require.NoError(t, err)
	return svc
}

func choices(t *testing.T, svc privacy.Service, user string) (analytics, crash bool) {
	t.Helper()
	analytics, crash, err := svc.GetPrivacySettings(context.Background(), user)
	require.NoError(t, err)
	return analytics, crash
}

// Two users keep their own choices: updating one never reads, writes or
// overwrites the other's consent.
func TestPrivacy_ChoicesAreKeptPerUser(t *testing.T) {
	env := storetest.New(t)
	svc := newService(t, env)
	ctx := context.Background()

	require.NoError(t, svc.SetPrivacySettings(ctx, "alice", false, true))
	require.NoError(t, svc.SetPrivacySettings(ctx, "bob", true, false))

	a, c := choices(t, svc, "alice")
	require.False(t, a)
	require.True(t, c)
	a, c = choices(t, svc, "bob")
	require.True(t, a)
	require.False(t, c)

	// Changing alice's choice leaves bob's as it was.
	require.NoError(t, svc.SetPrivacySettings(ctx, "alice", true, true))
	a, c = choices(t, svc, "bob")
	require.True(t, a)
	require.False(t, c, "bob's opt-out survives another user's update")
}

// An opt-out is persisted: a service rebuilt over the same database still
// reads it, so a restart does not turn a user's analytics back on.
func TestPrivacy_OptOutSurvivesReconstruction(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	require.NoError(t, newService(t, env).SetPrivacySettings(ctx, "alice", false, false))

	again := newService(t, env)
	enabled, err := again.GetAnalyticsEnabled(ctx, "alice")
	require.NoError(t, err)
	require.False(t, enabled)
	a, c := choices(t, again, "alice")
	require.False(t, a)
	require.False(t, c)

	// Setting the same choice twice updates the one row, not a second one.
	require.NoError(t, again.SetPrivacySettings(ctx, "alice", false, false))
	a, _ = choices(t, again, "alice")
	require.False(t, a)
}

// A user who never chose is opted in (both choices default on).
func TestPrivacy_NoChoiceMeansOptedIn(t *testing.T) {
	env := storetest.New(t)
	svc := newService(t, env)
	a, c := choices(t, svc, "nobody-chose")
	require.True(t, a)
	require.True(t, c)
	enabled, err := svc.GetAnalyticsEnabled(context.Background(), "nobody-chose")
	require.NoError(t, err)
	require.True(t, enabled)
}

// An empty user id is an error on every read and write, never an answer.
func TestPrivacy_EmptyUserIsRejected(t *testing.T) {
	svc, err := privacy.New(privacy.Deps{Settings: &privacy.MockSettings{}})
	require.NoError(t, err)
	ctx := context.Background()

	_, err = svc.GetAnalyticsEnabled(ctx, "")
	require.Error(t, err)
	_, _, err = svc.GetPrivacySettings(ctx, "")
	require.Error(t, err)
	require.Error(t, svc.SetPrivacySettings(ctx, "", false, false))
}

// A failed read is an error, not "opted in": an outage must not enable what
// the user turned off. A malformed stored value is an error too.
func TestPrivacy_ReadFailuresAreErrorsNotDefaults(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")
	svc, err := privacy.New(privacy.Deps{Settings: &privacy.MockSettings{
		GetSettingFunc: func(context.Context, string, *string, string) (*core.Setting, error) { return nil, boom },
	}})
	require.NoError(t, err)
	_, err = svc.GetAnalyticsEnabled(ctx, "alice")
	require.ErrorIs(t, err, boom)

	malformed, err := privacy.New(privacy.Deps{Settings: &privacy.MockSettings{
		GetSettingFunc: func(context.Context, string, *string, string) (*core.Setting, error) {
			return &core.Setting{Value: "maybe"}, nil
		},
	}})
	require.NoError(t, err)
	_, err = malformed.GetAnalyticsEnabled(ctx, "alice")
	require.Error(t, err)

	missing, err := privacy.New(privacy.Deps{Settings: &privacy.MockSettings{
		GetSettingFunc: func(context.Context, string, *string, string) (*core.Setting, error) {
			return nil, svcerr.NotFound("setting")
		},
	}})
	require.NoError(t, err)
	enabled, err := missing.GetAnalyticsEnabled(ctx, "alice")
	require.NoError(t, err)
	require.True(t, enabled, "only a confirmed not-found means the default")
}
