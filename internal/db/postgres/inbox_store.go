package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type inboxStore struct{ q pgdb.Querier }

// NewInboxStore creates the Postgres inbox store implementation.
func NewInboxStore(q pgdb.Querier) core.InboxStore { return &inboxStore{q: q} }

func (s *inboxStore) ListInboxPending(ctx context.Context, userID string) ([]*core.InboxPending, error) {
	rows, err := s.q.ListInboxPending(ctx, pgdb.ListInboxPendingParams{
		UserID: userID,
		// "Run finished" items follow the same rule as the unread write.
		UnattendedKinds: core.UnattendedEventKinds(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list inbox: %w", err)
	}
	out := make([]*core.InboxPending, 0, len(rows))
	for _, r := range rows {
		out = append(out, &core.InboxPending{
			Kind:         core.InboxPendingKind(r.Kind),
			ItemKey:      r.ItemKey,
			ChatID:       r.ChatID,
			RunID:        r.RunID,
			TriggerID:    r.TriggerID,
			ProjectID:    r.ProjectID,
			ProjectName:  r.ProjectName,
			WorkflowName: r.WorkflowName,
			ChatTitle:    r.ChatTitle,
			TriggerName:  r.TriggerName,
			WaitingSince: r.WaitingSince,
			Text1:        r.AText,
			Text2:        r.BText,
			Int1:         r.AInt,
		})
	}
	return out, nil
}

func (s *inboxStore) ListDismissedInboxItemIDs(ctx context.Context, userID string, itemIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(itemIDs))
	if len(itemIDs) == 0 {
		return out, nil
	}
	ids, err := s.q.ListDismissedInboxItemIDs(ctx, pgdb.ListDismissedInboxItemIDsParams{UserID: userID, ItemIds: itemIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to list dismissed inbox items: %w", err)
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (s *inboxStore) DismissInboxItems(ctx context.Context, userID string, itemIDs []string, at time.Time) error {
	if len(itemIDs) == 0 {
		return nil
	}
	return s.q.DismissInboxItems(ctx, pgdb.DismissInboxItemsParams{UserID: userID, ItemIds: itemIDs, DismissedAt: at})
}

func (s *inboxStore) RestoreInboxItems(ctx context.Context, userID string, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	return s.q.RestoreInboxItems(ctx, pgdb.RestoreInboxItemsParams{UserID: userID, ItemIds: itemIDs})
}
