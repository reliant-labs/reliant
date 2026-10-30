// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// The update ledgers (chat_updates, user_updates) and the stream counters
// they allocate from: chat_update_repo_ext.go, user_update_repo_ext.go,
// update_stream_counter_repo_ext.go. Writes alternate between the Repository
// and the store on one database, so both share one allocator.

// TestUpdateSequenceAllocation_RequiresTransaction: a sequence allocated
// outside the transaction that writes its ledger row could be consumed with
// nothing durable behind it, so the allocator refuses.
func TestUpdateSequenceAllocation_RequiresTransaction(t *testing.T) {
	f := newMsgFixture(t, "chat-alloc")
	ctx := context.Background()
	for _, kind := range []string{store.UpdateStreamUser, store.UpdateStreamChat} {
		if _, err := store.AllocateUpdateSequence(ctx, f.env.ORM, kind, "unbound"); err == nil {
			t.Fatalf("%s allocation outside a transaction succeeded", kind)
		}
	}
	if _, err := store.AppendChatUpdate(ctx, f.env.ORM, f.chat, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_REFETCH, "e", "{}"); err == nil {
		t.Fatal("AppendChatUpdate outside a transaction succeeded")
	}
}

func TestChatUpdates_AcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-updates")
	ctx := context.Background()
	o := f.env.ORM
	status := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS

	for i := 0; i < 4; i++ {
		entity, data := fmt.Sprintf("e%d", i), fmt.Sprintf(`{"n":%d}`, i)
		if i%2 == 0 {
			if err := f.env.Repo.CreateChatUpdate(ctx, f.chat, status, entity, data); err != nil {
				t.Fatalf("Repo.CreateChatUpdate %d: %v", i, err)
			}
			continue
		}
		var got int64
		if err := o.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			u, err := store.AppendChatUpdate(ctx, o, f.chat, status, entity, data)
			got = u.SequenceNumber
			return err
		}); err != nil {
			t.Fatalf("AppendChatUpdate %d: %v", i, err)
		}
		if got != int64(i+1) {
			t.Fatalf("AppendChatUpdate %d took sequence %d, want %d", i, got, i+1)
		}
	}
	if seq, err := store.LatestChatUpdateSequence(ctx, o, f.chat); err != nil || seq != 4 {
		t.Fatalf("LatestChatUpdateSequence = %d, %v; want 4", seq, err)
	}
	if seq, err := f.env.Repo.GetLatestUpdateSequence(ctx, f.chat); err != nil || seq != 4 {
		t.Fatalf("Repo.GetLatestUpdateSequence = %d, %v; want 4", seq, err)
	}
	if seq, err := store.LatestChatUpdateSequence(ctx, o, "no-chat"); err != nil || seq != 0 {
		t.Fatalf("LatestChatUpdateSequence(empty) = %d, %v; want 0", seq, err)
	}

	page, err := store.ChatUpdatesSince(ctx, o, f.chat, 1, 2)
	if err != nil || len(page) != 2 || page[0].SequenceNumber != 2 || page[1].SequenceNumber != 3 ||
		page[0].ID != f.chat+"-2" || page[0].EntityID != "e1" || string(page[0].Data) != `{"n":1}` || page[0].UpdateType != status {
		t.Fatalf("ChatUpdatesSince(1, 2) = %+v, %v", page, err)
	}
	twin, err := f.env.Repo.GetUpdatesSince(ctx, f.chat, 1, 2)
	if err != nil || len(twin) != 2 {
		t.Fatalf("Repo.GetUpdatesSince = %+v, %v", twin, err)
	}
	for i := range twin {
		if twin[i].ID != page[i].ID || string(twin[i].Data) != string(page[i].Data) || !twin[i].CreatedAt.Equal(page[i].CreatedAt) {
			t.Fatalf("update %d: Repo %+v, store %+v", i, twin[i], page[i])
		}
	}
}

// TestSnapshotHeads_AgreeWithRepository: one row per entity, the newest; the
// re-keyed types collapse on their own key; messages, tool calls, finalize
// markers and drains are not snapshot heads at all.
func TestSnapshotHeads_AgreeWithRepository(t *testing.T) {
	f := newMsgFixture(t, "chat-heads")
	ctx := context.Background()
	write := func(typ reliantv1.ChatUpdateType, entity, data string) {
		t.Helper()
		if err := f.env.Repo.CreateChatUpdate(ctx, f.chat, typ, entity, data); err != nil {
			t.Fatal(err)
		}
	}
	ap := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL
	write(ap, "ap-1", `{"v":1}`)                                                               // 1
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, "m-1", `{}`)                      // 2: excluded
	write(ap, "ap-1", `{"v":2}`)                                                               // 3: ap-1's head
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD, "wf-1", `{"thread":"th-a"}`)       // 4: th-a's head
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD, "wf-1", `{"thread":"th-b"}`)       // 5: th-b's head
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION, "question-q-1-100", `{"s":"p"}`) // 6
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION, "question-q-1-200", `{"s":"r"}`) // 7: q-1's head
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL, "tool-1", `{}`)                 // 8: excluded
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED, "m-1", `{}`)             // 9: excluded
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED, "drain-1", `{}`)   // 10: excluded
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION, "wf-1:node-1", `{"n":1}`)  // 11: excluded (workflow state)
	write(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS, "wf-1", `{"v":3}`)        // 12: excluded (workflow state)

	got, err := store.LatestNonMessageUpdatesPerEntity(ctx, f.env.ORM, f.chat)
	if err != nil {
		t.Fatal(err)
	}
	seqs := make([]int64, len(got))
	for i, u := range got {
		seqs[i] = u.SequenceNumber
	}
	if fmt.Sprint(seqs) != "[3 4 5 7]" {
		t.Fatalf("snapshot heads = %v, want [3 4 5 7]", seqs)
	}
	var payload map[string]any
	if err := json.Unmarshal(got[0].Data, &payload); err != nil || payload["v"] != float64(2) {
		t.Fatalf("ap-1's head carries %s, want the newest", got[0].Data)
	}
	twin, err := f.env.Repo.GetLatestNonMessageUpdatesPerEntity(ctx, f.chat)
	if err != nil || len(twin) != len(got) {
		t.Fatalf("Repo heads = %+v, %v", twin, err)
	}
	for i := range twin {
		if twin[i].ID != got[i].ID || twin[i].UpdateType != got[i].UpdateType || string(twin[i].Data) != string(got[i].Data) {
			t.Fatalf("head %d: Repo %+v, store %+v", i, twin[i], got[i])
		}
	}
}

func TestUserUpdates_AcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-user-updates")
	ctx := context.Background()
	o := f.env.ORM
	user := "user-ledger"
	chat := f.chat

	for i := 0; i < 4; i++ {
		u := &db.UserUpdate{
			UserID: user, ChatID: &chat, EntityID: fmt.Sprintf("e%d", i), Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)),
			UpdateType: reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_TITLE_CHANGED, EntityType: reliantv1.EntityType_ENTITY_TYPE_CHAT,
		}
		if i == 3 {
			u.ID = "chosen-id"
		}
		if i%2 == 0 {
			if err := f.env.Repo.CreateUserUpdate(ctx, u); err != nil {
				t.Fatalf("Repo.CreateUserUpdate %d: %v", i, err)
			}
		} else if err := o.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			return store.AppendUserUpdate(ctx, o, u, u.ID != "")
		}); err != nil {
			t.Fatalf("AppendUserUpdate %d: %v", i, err)
		}
		want := fmt.Sprintf("%s-%d", user, i+1)
		if i == 3 {
			want = "chosen-id"
		}
		if u.SequenceNumber != int64(i+1) || u.ID != want {
			t.Fatalf("update %d: sequence %d id %q, want %d %q", i, u.SequenceNumber, u.ID, i+1, want)
		}
	}
	if seq, err := store.LatestUserUpdateSequence(ctx, o, user); err != nil || seq != 4 {
		t.Fatalf("LatestUserUpdateSequence = %d, %v; want 4", seq, err)
	}
	if seq, err := f.env.Repo.GetLatestUserUpdateSequence(ctx, "nobody"); err != nil || seq != 0 {
		t.Fatalf("Repo.GetLatestUserUpdateSequence(nobody) = %d, %v; want 0", seq, err)
	}
	page, err := store.UserUpdatesSince(ctx, o, user, 2, 10)
	if err != nil || len(page) != 2 || page[0].ID != user+"-3" || page[1].ID != "chosen-id" ||
		page[0].ChatID == nil || *page[0].ChatID != chat || page[0].ProjectID != nil ||
		page[0].UpdateType != reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_TITLE_CHANGED || page[0].EntityType != reliantv1.EntityType_ENTITY_TYPE_CHAT ||
		string(page[1].Data) != `{"n":3}` || page[0].CreatedAt.IsZero() {
		t.Fatalf("UserUpdatesSince(2) = %+v, %v", page, err)
	}
	twin, err := f.env.Repo.GetUserUpdatesSince(ctx, user, 2, 0)
	if err != nil || len(twin) != 2 || twin[1].ID != "chosen-id" || !twin[0].CreatedAt.Equal(page[0].CreatedAt) {
		t.Fatalf("Repo.GetUserUpdatesSince = %+v, %v", twin, err)
	}
	c, err := store.GetUpdateStreamCounterByPK(ctx, o, store.UpdateStreamUser, user)
	if err != nil || c.LastAssignedSeq != 4 {
		t.Fatalf("user counter = %+v, %v; want 4", c, err)
	}
}
