package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

type unavailableInboxPending struct{ Items }

func (unavailableInboxPending) ListInboxPending(context.Context, string) ([]*core.InboxPending, error) {
	return nil, errors.New("pending unavailable")
}

type unavailableInboxTriggers struct{ Repository }

func (unavailableInboxTriggers) ListTriggers(context.Context, core.TriggerFilters) ([]*core.Trigger, error) {
	return nil, errors.New("automation health unavailable")
}

func TestListInbox_HousekeepingDoesNotReadUnrelatedSections(t *testing.T) {
	for _, section := range []string{"pending", "automation"} {
		t.Run(section, func(t *testing.T) {
			f := newInboxFixture(t)
			daemonID := "d-" + uuid.NewString()
			require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID}))
			putCacheVolume(t, f, daemonID, 1<<30, true, 0)
			if section == "pending" {
				f.inbox.deps.Items = unavailableInboxPending{f.inbox.deps.Items}
			} else {
				f.inbox.deps.Repo = unavailableInboxTriggers{f.inbox.deps.Repo}
			}
			_, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
			require.Error(t, err, "the unrelated section really is unavailable")

			resp := f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING)
			items := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
			require.Len(t, items, 1)
			assert.Equal(t, daemonID, items[0].GetStorage().GetDaemonId())
		})
	}
}

func TestListInbox_PausedRunAfterSuccessIsNews(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-paused-news")
	base := time.Date(2026, time.January, 2, 9, 0, 0, 0, time.UTC)
	f.runFiring(t, "paused-news-failure", "trg-paused-news", base, true)
	f.runFiring(t, "paused-news-success", "trg-paused-news", base.Add(time.Hour), false)
	f.launched(t, "paused-news-launch", "chat-paused-news", "trg-paused-news", base.Add(2*time.Hour), db.Paused())
	require.NoError(t, f.repo.CreateStepExecution(f.ctx, &db.StepExecution{
		ID: "paused-news-step", WorkflowID: "chat-paused-news", StepID: "gate", ActivityName: "ExecuteRunStep",
		ErrorMessage: ptr.Of("new failure"), CreatedAt: base.Add(2*time.Hour + time.Minute),
	}))

	resp := f.list(t, nil)
	items := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_RUN_PAUSED)
	require.Len(t, items, 1)
	assert.Equal(t, "chat-paused-news", items[0].ChatId)
	assert.False(t, items[0].Habitual)
	assert.Zero(t, resp.HabitualCount)
}
