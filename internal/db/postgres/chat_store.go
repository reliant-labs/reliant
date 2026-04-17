package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type chatStore struct {
	q  pgdb.Querier
	db pgdb.DBTX
}

// NewChatStore creates the Postgres chat store implementation.
func NewChatStore(q pgdb.Querier, db pgdb.DBTX) core.ChatStore {
	return &chatStore{q: q, db: db}
}

func (s *chatStore) CreateChat(ctx context.Context, chat *core.Chat) error {
	return s.q.CreateChat(ctx, chatToCreateParams(chat))
}

func (s *chatStore) GetChat(ctx context.Context, id string) (*core.Chat, error) {
	row, err := s.q.GetChat(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("chat not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}
	return chatFromRow(row), nil
}

func (s *chatStore) GetChatWithUserCheck(ctx context.Context, id string, userID string) (*core.Chat, error) {
	row, err := s.q.GetChatWithUserCheck(ctx, pgdb.GetChatWithUserCheckParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("chat not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}
	return chatFromRow(row), nil
}

func (s *chatStore) ListChats(ctx context.Context, filters core.ChatFilters) ([]*core.Chat, error) {
	stateNull := sql.NullInt32{}
	if filters.State != nil {
		stateNull = sql.NullInt32{Int32: int32(*filters.State), Valid: true}
	}

	rows, err := s.q.ListChats(ctx, pgdb.ListChatsParams{
		UserID:          filters.UserID,
		ProjectID:       chatPtrToNullString(filters.ProjectID),
		State:           stateNull,
		ExcludeArchived: filters.ExcludeArchived,
		Limit:           int32(filters.Limit),
		Offset:          int32(filters.Offset),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list chats: %w", err)
	}
	return chatsFromRows(rows), nil
}

func (s *chatStore) SearchChats(ctx context.Context, filters core.ChatSearchFilters) ([]*core.Chat, error) {
	searchQuery := strings.TrimSpace(filters.SearchQuery)
	if searchQuery == "" {
		return []*core.Chat{}, nil
	}

	var stateCheck interface{}
	var stateNullInt sql.NullInt32
	if filters.State != nil {
		stateCheck = int32(*filters.State)
		stateNullInt = sql.NullInt32{Int32: int32(*filters.State), Valid: true}
	}

	searchPattern := "%" + searchQuery + "%"
	rows, err := s.q.SearchChats(ctx, pgdb.SearchChatsParams{
		UserID:    filters.UserID,
		ProjectID: filters.ProjectID,
		Column3:   stateCheck,
		State:     stateNullInt,
		Title:     searchPattern,
		Content:   chatPtrToNullString(&searchPattern),
		Limit:     int32(filters.Limit),
		Offset:    int32(filters.Offset),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search chats: %w", err)
	}
	return chatsFromRows(rows), nil
}

func (s *chatStore) UpdateChat(ctx context.Context, chat *core.Chat) error {
	return s.q.UpdateChat(ctx, chatToUpdateParams(chat))
}

func (s *chatStore) UpdateChatActiveDaemon(ctx context.Context, chatID string, daemonID *string) error {
	return s.q.UpdateChatActiveDaemon(ctx, pgdb.UpdateChatActiveDaemonParams{
		ActiveDaemonID: chatPtrToNullString(daemonID),
		ID:             chatID,
	})
}

func (s *chatStore) DeleteChat(ctx context.Context, id string) error {
	return s.q.DeleteChat(ctx, id)
}

func (s *chatStore) ListArchivedChats(ctx context.Context, userID string) ([]*core.ArchivedChatInfo, error) {
	rows, err := s.q.ListArchivedChats(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list archived chats: %w", err)
	}
	return archivedChatInfosFromRows(rows), nil
}

func (s *chatStore) CreateChatUpdate(ctx context.Context, update core.ChatUpdate) error {
	return s.q.CreateChatUpdate(ctx, pgdb.CreateChatUpdateParams{
		ID:             update.ID,
		ChatID:         update.ChatID,
		SequenceNumber: update.SequenceNumber,
		UpdateType:     int32(update.UpdateType),
		EntityID:       update.EntityID,
		Data:           string(update.Data),
		CreatedAt:      update.CreatedAt,
	})
}

func chatNullInt32ToChatState(ni sql.NullInt32) core.ChatState {
	if ni.Valid {
		return core.ChatState(ni.Int32)
	}
	return core.ChatStateUnspecified
}

func chatStateToNullInt32(state core.ChatState) sql.NullInt32 {
	if state == 0 {
		return sql.NullInt32{Valid: false}
	}
	return sql.NullInt32{Int32: int32(state), Valid: true}
}

func chatFromRow(row pgdb.ChatsWithActivity) *core.Chat {
	activity := int(row.Activity)
	return &core.Chat{
		ID:              row.ID,
		Title:           row.Title,
		ProjectID:       row.ProjectID,
		WorktreeID:      chatNullStringToPtr(row.WorktreeID),
		UserID:          row.UserID,
		WorkflowName:    chatNullStringToPtr(row.WorkflowName),
		State:           chatNullInt32ToChatState(row.State),
		WorkflowID:      chatNullStringToPtr(row.WorkflowID),
		RunID:           chatNullStringToPtr(row.RunID),
		SelectedPresets: chatNullStringToStringMap(row.SelectedPresets),
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		LastActive:      row.LastActive,
		LastMessageAt:   chatInterfaceToTimePtr(row.LastMessageAt),
		Activity:        &activity,
		Unread:          row.Unread != 0,
		ActiveDaemonID:  chatNullStringToPtr(row.ActiveDaemonID),
	}
}

func chatsFromRows(rows []pgdb.ChatsWithActivity) []*core.Chat {
	chats := make([]*core.Chat, len(rows))
	for i, row := range rows {
		chats[i] = chatFromRow(row)
	}
	return chats
}

func chatToCreateParams(chat *core.Chat) pgdb.CreateChatParams {
	state := chat.State
	if state == 0 {
		state = core.ChatStateIdle
	}

	return pgdb.CreateChatParams{
		ID:              chat.ID,
		UserID:          chat.UserID,
		Title:           chat.Title,
		ProjectID:       chat.ProjectID,
		WorktreeID:      chatPtrToNullString(chat.WorktreeID),
		WorkflowName:    chatPtrToNullString(chat.WorkflowName),
		State:           chatStateToNullInt32(state),
		WorkflowID:      chatPtrToNullString(chat.WorkflowID),
		RunID:           chatPtrToNullString(chat.RunID),
		SelectedPresets: chatStringMapToNullString(chat.SelectedPresets),
		CreatedAt:       chat.CreatedAt,
		UpdatedAt:       chat.UpdatedAt,
		LastActive:      chat.LastActive,
	}
}

func chatToUpdateParams(chat *core.Chat) pgdb.UpdateChatParams {
	state := chat.State
	if state == 0 {
		state = core.ChatStateIdle
	}

	return pgdb.UpdateChatParams{
		ID:              chat.ID,
		Title:           chat.Title,
		ProjectID:       chat.ProjectID,
		WorktreeID:      chatPtrToNullString(chat.WorktreeID),
		WorkflowName:    chatPtrToNullString(chat.WorkflowName),
		State:           chatStateToNullInt32(state),
		WorkflowID:      chatPtrToNullString(chat.WorkflowID),
		RunID:           chatPtrToNullString(chat.RunID),
		SelectedPresets: chatStringMapToNullString(chat.SelectedPresets),
		LastActive:      chat.LastActive,
	}
}

func archivedChatInfosFromRows(rows []pgdb.ListArchivedChatsRow) []*core.ArchivedChatInfo {
	items := make([]*core.ArchivedChatInfo, len(rows))
	for i, row := range rows {
		worktreeName := row.WorktreeName
		items[i] = &core.ArchivedChatInfo{
			Chat: core.Chat{
				ID:              row.ID,
				Title:           row.Title,
				ProjectID:       row.ProjectID,
				WorktreeID:      chatNullStringToPtr(row.WorktreeID),
				UserID:          row.UserID,
				WorkflowName:    chatNullStringToPtr(row.WorkflowName),
				State:           chatNullInt32ToChatState(row.State),
				WorkflowID:      chatNullStringToPtr(row.WorkflowID),
				RunID:           chatNullStringToPtr(row.RunID),
				SelectedPresets: chatNullStringToStringMap(row.SelectedPresets),
				CreatedAt:       row.CreatedAt,
				UpdatedAt:       row.UpdatedAt,
				LastActive:      row.LastActive,
				LastMessageAt:   chatInterfaceToTimePtr(row.LastMessageAt),
			},
			WorktreeName:      &worktreeName,
			WorktreeDeletedAt: chatNullTimeToPtr(row.WorktreeDeletedAt),
		}
	}
	return items
}

func chatPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func chatNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func chatNullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
	}
	return nil
}

func chatNullStringToStringMap(ns sql.NullString) map[string]string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(ns.String), &result); err != nil {
		return nil
	}
	return result
}

func chatStringMapToNullString(m map[string]string) sql.NullString {
	if len(m) == 0 {
		return sql.NullString{Valid: false}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: string(data), Valid: true}
}

func chatInterfaceToTimePtr(v interface{}) *time.Time {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case time.Time:
		return &t
	case *time.Time:
		return t
	case string:
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			parsed, err = time.Parse("2006-01-02 15:04:05", t)
			if err != nil {
				return nil
			}
		}
		return &parsed
	default:
		return nil
	}
}
