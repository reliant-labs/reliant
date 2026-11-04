// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/store"
)

// ErrorUpdatesInRange returns one update per error (its newest), only errors,
// and with an open lower bound everything before the upper one. No thread
// message at or after the cursor leaves the range open above.
func TestErrorUpdatesInRange(t *testing.T) {
	f := newMsgFixture(t, "chat-error-range")
	ctx := context.Background()
	errType := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR
	for _, u := range []struct{ entity, data string }{
		{"err-a", `{"id":"err-a","attempt_number":1}`},
		{"err-a", `{"id":"err-a","attempt_number":2}`},
		{"err-b", `{"id":"err-b"}`},
	} {
		if err := f.env.Repo.CreateChatUpdate(ctx, f.chat, errType, u.entity, u.data); err != nil {
			t.Fatalf("CreateChatUpdate: %v", err)
		}
	}
	if err := f.env.Repo.CreateChatUpdate(ctx, f.chat, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_INFO, "info", `{}`); err != nil {
		t.Fatalf("CreateChatUpdate: %v", err)
	}

	all, err := store.ErrorUpdatesInRange(ctx, f.env.ORM, f.chat, nil, "no-such-thread", 1)
	if err != nil {
		t.Fatalf("ErrorUpdatesInRange: %v", err)
	}
	if len(all) != 2 || all[0].EntityID != "err-a" || all[1].EntityID != "err-b" {
		t.Fatalf("got %+v, want err-a then err-b", all)
	}
	if string(all[0].Data) != `{"id": "err-a", "attempt_number": 2}` && string(all[0].Data) != `{"id":"err-a","attempt_number":2}` {
		t.Fatalf("err-a is not its newest attempt: %s", all[0].Data)
	}

	future := time.Now().Add(time.Hour)
	none, err := store.ErrorUpdatesInRange(ctx, f.env.ORM, f.chat, &future, "no-such-thread", 1)
	if err != nil {
		t.Fatalf("ErrorUpdatesInRange: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("errors before the lower bound were returned: %+v", none)
	}
}
