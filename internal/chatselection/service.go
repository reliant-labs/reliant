// Copyright (c) 2025 Reliant Labs
package chatselection

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

type service struct {
	store Store
}

func (s *service) Get(ctx context.Context, chatID string) (*Selection, error) {
	if chatID == "" {
		return nil, nil
	}
	return s.store.GetChatSelection(ctx, chatID)
}

func (s *service) Merge(ctx context.Context, chatID, userID, workflowName string, patch map[string]any, expectedVersion int64) (*Selection, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat id is required")
	}
	return s.store.MutateChatSelection(ctx, chatID, func(cur *Selection) (*Selection, error) {
		if err := checkVersion(cur, expectedVersion); err != nil {
			return nil, err
		}
		if cur == nil && len(patch) == 0 {
			return nil, nil
		}
		next := base(cur, chatID, userID, workflowName)
		if cur != nil && cur.WorkflowName != workflowName {
			next.Params = map[string]any{}
		}
		for key, value := range patch {
			if value == nil {
				delete(next.Params, key)
				continue
			}
			next.Params[key] = value
		}
		if cur != nil && cur.WorkflowName == next.WorkflowName && sameParams(cur.Params, next.Params) {
			return cur, nil
		}
		return next, nil
	})
}

func (s *service) SetPolicy(ctx context.Context, chatID, userID, workflowName string, policy ProviderPolicy, expectedVersion int64) (*Selection, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat id is required")
	}
	return s.store.MutateChatSelection(ctx, chatID, func(cur *Selection) (*Selection, error) {
		if err := checkVersion(cur, expectedVersion); err != nil {
			return nil, err
		}
		if cur != nil && cur.Policy == policy {
			return cur, nil
		}
		if cur == nil && policy == (ProviderPolicy{}) {
			return nil, nil
		}
		next := base(cur, chatID, userID, workflowName)
		next.Policy = policy
		return next, nil
	})
}

func (s *service) Copy(ctx context.Context, fromChatID, toChatID, userID string) error {
	if fromChatID == "" || toChatID == "" {
		return fmt.Errorf("both chat ids are required")
	}
	return s.store.CopyChatSelection(ctx, fromChatID, toChatID, userID)
}

// checkVersion enforces expectedVersion against the current record. A chat with
// no record is at version 0, so a client that read "none" (version 0) may
// create the first one, and a client that read version 3 of a record that is
// gone is told it is stale.
func checkVersion(cur *Selection, expected int64) error {
	if expected <= 0 {
		return nil
	}
	var have int64
	if cur != nil {
		have = cur.Version
	}
	if have != expected {
		return ErrVersionConflict
	}
	return nil
}

// base is a copy of cur to edit, or a new record when there is none. The
// version is the store's to advance.
func base(cur *Selection, chatID, userID, workflowName string) *Selection {
	next := &Selection{ChatID: chatID, UserID: userID, WorkflowName: workflowName, Params: map[string]any{}}
	if cur == nil {
		return next
	}
	next.UserID = cur.UserID
	next.Policy = cur.Policy
	next.Version = cur.Version
	for key, value := range cur.Params {
		next.Params[key] = value
	}
	return next
}

// sameParams compares two param maps as the database will round-trip them
// (JSON), so a float and the integer it was decoded from do not differ.
func sameParams(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return reflect.DeepEqual(a, b)
	}
	return string(ja) == string(jb)
}
