// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/chatselection"
)

// GetChatSelection returns the chat's selection, nil with no error when it has
// none.
func (r *Repo) GetChatSelection(ctx context.Context, chatID string) (*chatselection.Selection, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return r.readChatSelection(ctx, chatID, false)
}

func (r *Repo) readChatSelection(ctx context.Context, chatID string, forUpdate bool) (*chatselection.Selection, error) {
	query := `SELECT chat_id, user_id, workflow_name, params, provider_policy, version, updated_at
FROM chat_selections WHERE chat_id = ?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var (
		sel               chatselection.Selection
		params, policyRaw []byte
	)
	err := r.DB.DB(ctx).QueryRowContext(ctx, r.bindQuery(query), chatID).Scan(
		&sel.ChatID, &sel.UserID, &sel.WorkflowName, &params, &policyRaw, &sel.Version, &sel.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read chat selection: %w", err)
	}
	if err := json.Unmarshal(params, &sel.Params); err != nil {
		return nil, fmt.Errorf("failed to decode chat selection params: %w", err)
	}
	if sel.Params == nil {
		sel.Params = map[string]any{}
	}
	if err := json.Unmarshal(policyRaw, &sel.Policy); err != nil {
		return nil, fmt.Errorf("failed to decode chat selection policy: %w", err)
	}
	return &sel, nil
}

// MutateChatSelection is chatselection.Store.MutateChatSelection: one writer of
// a chat's record at a time, so a read-modify-write cannot lose an edit. The
// advisory lock covers the first write too, when there is no row to lock.
func (r *Repo) MutateChatSelection(ctx context.Context, chatID string, mutate func(current *chatselection.Selection) (*chatselection.Selection, error)) (*chatselection.Selection, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	var out *chatselection.Selection
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		if _, err := r.DB.DB(txCtx).ExecContext(txCtx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "chat-selection:"+chatID); err != nil {
			return fmt.Errorf("failed to lock chat selection: %w", err)
		}
		cur, err := r.readChatSelection(txCtx, chatID, true)
		if err != nil {
			return err
		}
		next, err := mutate(cur)
		if err != nil {
			return err
		}
		if next == nil || next == cur {
			out = cur
			return nil
		}
		params, err := json.Marshal(next.Params)
		if err != nil {
			return fmt.Errorf("failed to encode chat selection params: %w", err)
		}
		policy, err := json.Marshal(next.Policy)
		if err != nil {
			return fmt.Errorf("failed to encode chat selection policy: %w", err)
		}
		now := time.Now().UTC()
		var version int64
		err = r.DB.DB(txCtx).QueryRowContext(txCtx, r.bindQuery(`
INSERT INTO chat_selections (chat_id, user_id, workflow_name, params, provider_policy, version, updated_at)
VALUES (?, ?, ?, ?::jsonb, ?::jsonb, 1, ?)
ON CONFLICT (chat_id) DO UPDATE SET
    workflow_name = EXCLUDED.workflow_name,
    params = EXCLUDED.params,
    provider_policy = EXCLUDED.provider_policy,
    version = chat_selections.version + 1,
    updated_at = EXCLUDED.updated_at
RETURNING version`),
			chatID, next.UserID, next.WorkflowName, string(params), string(policy), now).Scan(&version)
		if err != nil {
			return fmt.Errorf("failed to write chat selection: %w", err)
		}
		stored := *next
		stored.ChatID = chatID
		stored.Version = version
		stored.UpdatedAt = now
		out = &stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CopyChatSelection is chatselection.Store.CopyChatSelection.
func (r *Repo) CopyChatSelection(ctx context.Context, fromChatID, toChatID, userID string) error {
	if fromChatID == "" || toChatID == "" {
		return fmt.Errorf("chat IDs cannot be empty")
	}
	_, err := r.DB.DB(ctx).ExecContext(ctx, r.bindQuery(`
INSERT INTO chat_selections (chat_id, user_id, workflow_name, params, provider_policy, version, updated_at)
SELECT ?, ?, workflow_name, params, provider_policy, 1, ?
FROM chat_selections WHERE chat_id = ?
ON CONFLICT (chat_id) DO NOTHING`), toChatID, userID, time.Now().UTC(), fromChatID)
	if err != nil {
		return fmt.Errorf("failed to copy chat selection: %w", err)
	}
	return nil
}
