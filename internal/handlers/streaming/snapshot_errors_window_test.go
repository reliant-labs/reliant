package streaming

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

func TestErrorsInWindow_DropsOnlyErrorsOlderThanTheWindow(t *testing.T) {
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }
	update := func(id string, kind reliantv1.ChatUpdateType, d time.Duration) *reliantv1.ChatUpdateData {
		return &reliantv1.ChatUpdateData{EntityId: id, UpdateType: kind, CreatedAt: at(d)}
	}
	window := []*db.Message{{CreatedAt: base.Add(10 * time.Minute)}, {CreatedAt: base.Add(5 * time.Minute)}}
	in := []*reliantv1.ChatUpdateData{
		update("old-error", reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR, time.Minute),
		update("old-approval", reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL, time.Minute),
		update("edge-error", reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR, 5*time.Minute),
		update("new-error", reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR, 7*time.Minute),
	}

	var ids []string
	for _, u := range errorsInWindow(in, window) {
		ids = append(ids, u.EntityId)
	}
	require.Equal(t, []string{"old-approval", "edge-error", "new-error"}, ids)
	require.Len(t, in, 4, "the input slice is not rewritten")
}

func TestErrorsInWindow_EmptyWindowKeepsEverything(t *testing.T) {
	in := []*reliantv1.ChatUpdateData{{UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR, CreatedAt: "2026-11-04T12:00:00Z"}}
	require.Equal(t, in, errorsInWindow(in, nil))
}
