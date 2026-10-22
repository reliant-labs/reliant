package settings_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestListSettings_Generated(t *testing.T) {
	t.Parallel()

	t.Run("lists the caller's settings", func(t *testing.T) {
		var askedFor string
		repo := memorySettings{
			"theme": {ID: "s-1", UserID: "test-user", Key: "theme", Value: "dark", ValueType: "string"},
			"font":  {ID: "s-2", UserID: "test-user", Key: "font", Value: "mono", ValueType: "string"},
		}.repo()
		list := repo.ListSettingsFunc
		repo.ListSettingsFunc = func(ctx context.Context, userID string, projectID *string) ([]*db.Setting, error) {
			askedFor = userID
			return list(ctx, userID, projectID)
		}
		svc := newSettingsService(t, repo)

		resp, err := svc.ListSettings(settings.AuthedContext(t), connect.NewRequest(&pb.ListSettingsRequest{}))
		require.NoError(t, err)
		require.Equal(t, "test-user", askedFor)
		require.EqualValues(t, 2, resp.Msg.GetTotal())
		require.Len(t, resp.Msg.GetSettings(), 2)
	})

	t.Run("a failed read is internal", func(t *testing.T) {
		repo := memorySettings{}.repo()
		repo.ListSettingsFunc = func(context.Context, string, *string) ([]*db.Setting, error) { return nil, errors.New("db down") }
		svc := newSettingsService(t, repo)

		_, err := svc.ListSettings(settings.AuthedContext(t), connect.NewRequest(&pb.ListSettingsRequest{}))
		require.Equal(t, connect.CodeInternal, svcerr.Code(err))
	})
}
