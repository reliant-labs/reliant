package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A message saved with DISPLAY_STYLE_HIDDEN is LLM-context only: the transcript
// filters it in ListMessages (proto_converters.go) and in the timeline
// (InterleavedTimeline skips DisplayStyle.HIDDEN). Both of those filters read
// display_style, so a live update that omits the field arrives as
// displayStyle=undefined and renders as a normal message — the hidden message
// shows up until the next reload, when the filtered read path finally hides it.
//
// That is what made the "Some of your params have changed…" system message
// visible even though chat_send.go writes it hidden.

func TestSaveMessageToThread_UpdateCarriesDisplayStyle(t *testing.T) {
	repo, rawDB, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-display-style-payload"
	createActivityTestChat(t, repo, chatID)

	hidden := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	msg, err := repo.SaveMessageToThread(ctx, chatID, chatID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		"Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.",
		nil, nil, &hidden)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if msg.DisplayStyle == nil || *msg.DisplayStyle != reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
		t.Fatalf("message row display_style = %v, want HIDDEN", msg.DisplayStyle)
	}

	got := latestMessageUpdatePayload(t, rawDB, chatID, msg.ID)
	assertHiddenDisplayStyle(t, got, "SaveMessageToThread")
}

func TestEnrichMessageUpdate_CarriesDisplayStyle(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-display-style-enrich"
	createActivityTestChat(t, repo, chatID)

	hidden := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	msg, err := repo.SaveMessageToThread(ctx, chatID, chatID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "hidden context", nil, nil, &hidden)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"id": msg.ID})
	enriched, err := repo.EnrichMessageUpdate(ctx, ChatUpdate{
		Data: raw, EntityID: msg.ID, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(enriched, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertHiddenDisplayStyle(t, got, "EnrichMessageUpdate")
}

// A visible style must still round-trip — the fix must not blanket-stamp every
// update as hidden.
func TestSaveMessageToThread_UpdateCarriesVisibleDisplayStyle(t *testing.T) {
	repo, rawDB, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-display-style-info"
	createActivityTestChat(t, repo, chatID)

	info := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_INFO)
	msg, err := repo.SaveMessageToThread(ctx, chatID, chatID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "max turns reached", nil, nil, &info)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got := latestMessageUpdatePayload(t, rawDB, chatID, msg.ID)
	style, ok := got["display_style"]
	if !ok {
		t.Fatal("live message update dropped display_style for an INFO message")
	}
	if int32(style.(float64)) != int32(reliantv1.DisplayStyle_DISPLAY_STYLE_INFO) {
		t.Fatalf("display_style = %v, want INFO (%d)", style, reliantv1.DisplayStyle_DISPLAY_STYLE_INFO)
	}
}

// A message with no display style must not gain one — omitted stays omitted so
// the client renders it as an ordinary message.
func TestSaveMessageToThread_UpdateOmitsAbsentDisplayStyle(t *testing.T) {
	repo, rawDB, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-display-style-absent"
	createActivityTestChat(t, repo, chatID)

	msg, err := repo.SaveMessageToThread(ctx, chatID, chatID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "plain message", nil, nil, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got := latestMessageUpdatePayload(t, rawDB, chatID, msg.ID)
	if _, ok := got["display_style"]; ok {
		t.Fatalf("live update added display_style=%v to a message that has none", got["display_style"])
	}
}

func assertHiddenDisplayStyle(t *testing.T, payload map[string]any, writer string) {
	t.Helper()
	style, ok := payload["display_style"]
	if !ok {
		t.Fatalf("%s emitted a live update with no display_style — a HIDDEN message "+
			"arrives as displayStyle=undefined and renders in the transcript", writer)
	}
	if int32(style.(float64)) != int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN) {
		t.Fatalf("%s: display_style = %v, want HIDDEN (%d)", writer, style,
			reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	}
}

// latestMessageUpdatePayload reads back the chat_updates row emitted for a
// message, which is the exact JSON the streaming service sends to the client.
func latestMessageUpdatePayload(t *testing.T, rawDB *sql.DB, chatID, messageID string) map[string]any {
	t.Helper()
	row := rawDB.QueryRow(
		`SELECT data FROM chat_updates WHERE chat_id = $1 AND entity_id = $2 AND update_type = $3 ORDER BY sequence_number DESC LIMIT 1`,
		chatID, messageID, UpdateTypeMessage)
	var data string
	if err := row.Scan(&data); err != nil {
		t.Fatalf("read chat_update for message %s: %v", messageID, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("unmarshal chat_update data: %v", err)
	}
	return payload
}
