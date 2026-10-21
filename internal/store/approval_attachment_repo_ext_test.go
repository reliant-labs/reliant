// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestApprovalQueries ports approvals.sql, interleaved with the Repository.
func TestApprovalQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChatThread(t, env, "chat-approvals", "thread-approvals")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	pending := int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING)
	stepType := int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP)

	first := &core.Approval{
		ID: "ap-1", ChatID: "chat-approvals", ApprovalType: stepType, EntityID: "event-1", Status: pending,
		Title: "deploy?", Metadata: strp(`{"step":"deploy"}`), TemporalWorkflowID: "wf-1", CreatedAt: base, ThreadID: strp("thread-approvals"),
	}
	if err := store.CreateApprovalIfAbsent(ctx, env.ORM, first); err != nil {
		t.Fatalf("CreateApprovalIfAbsent: %v", err)
	}
	// A second approval for the same entity is a no-op, whoever writes it.
	if err := store.CreateApprovalIfAbsent(ctx, env.ORM, &core.Approval{ID: "ap-dup", ChatID: "chat-approvals", EntityID: "event-1", Status: pending, Title: "dup", CreatedAt: base}); err != nil {
		t.Fatalf("CreateApprovalIfAbsent(duplicate entity): %v", err)
	}
	if err := env.Repo.CreateApproval(ctx, &core.Approval{ID: "ap-dup2", ChatID: "chat-approvals", EntityID: "event-1", Status: pending, Title: "dup", CreatedAt: base}); err != nil {
		t.Fatalf("Repo.CreateApproval(duplicate entity): %v", err)
	}
	if err := env.Repo.CreateApproval(ctx, &core.Approval{
		ID: "ap-2", ChatID: "chat-approvals", ApprovalType: stepType, EntityID: "event-2", Status: pending, Title: "second", CreatedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatalf("Repo.CreateApproval: %v", err)
	}

	twin, err := env.Repo.GetApproval(ctx, "ap-1")
	if err != nil || twin == nil {
		t.Fatalf("Repo.GetApproval = %v, %v", twin, err)
	}
	if twin.Title != "deploy?" || twin.ApprovalType != stepType || *twin.Metadata != `{"step":"deploy"}` || twin.TemporalWorkflowID != "wf-1" || *twin.ThreadID != "thread-approvals" || !twin.CreatedAt.Equal(base) {
		t.Fatalf("Repo.GetApproval = %+v, want the store's write", twin)
	}
	got, err := store.GetApproval(ctx, env.ORM, "ap-2")
	if err != nil || got == nil || got.Title != "second" || got.ThreadID != nil || got.Metadata != nil {
		t.Fatalf("GetApproval(ap-2) = %+v, %v; want the Repository's write", got, err)
	}
	for _, id := range []string{"ap-dup", "ap-dup2", "missing"} {
		if got, err := store.GetApproval(ctx, env.ORM, id); got != nil || err != nil {
			t.Fatalf("GetApproval(%s) = %v, %v; want (nil, nil)", id, got, err)
		}
	}
	byEntity, err := store.GetApprovalByEntityID(ctx, env.ORM, "event-1")
	if err != nil || byEntity == nil || byEntity.ID != "ap-1" {
		t.Fatalf("GetApprovalByEntityID(event-1) = %+v, %v; want ap-1, the first writer", byEntity, err)
	}
	if got, err := store.GetApprovalByEntityID(ctx, env.ORM, "event-none"); got != nil || err != nil {
		t.Fatalf("GetApprovalByEntityID(none) = %v, %v; want (nil, nil)", got, err)
	}

	ids := func(as []*core.Approval) []string {
		out := make([]string, len(as))
		for i, a := range as {
			out[i] = a.ID
		}
		return out
	}

	// Resolve ap-1 with no metadata: the raised metadata is kept.
	resolved := base.Add(2 * time.Minute)
	approved := int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED)
	if err := store.UpdateApprovalStatus(ctx, env.ORM, "ap-1", approved, nil, strp("Deploy Now"), nil, &resolved); err != nil {
		t.Fatalf("UpdateApprovalStatus: %v", err)
	}
	twin, err = env.Repo.GetApproval(ctx, "ap-1")
	if err != nil {
		t.Fatalf("Repo.GetApproval after resolve: %v", err)
	}
	if twin.Status != approved || *twin.ActionTaken != "Deploy Now" || twin.DenialReason != nil || !twin.ResolvedAt.Equal(resolved) || *twin.Metadata != `{"step":"deploy"}` {
		t.Fatalf("after UpdateApprovalStatus Repo.GetApproval = %+v, want approved, action set, metadata kept", twin)
	}
	// With metadata: replaced.
	denied := int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED)
	if err := env.Repo.UpdateApprovalStatus(ctx, "ap-2", denied, strp("no"), nil, strp(`{"why":"risky"}`)); err != nil {
		t.Fatalf("Repo.UpdateApprovalStatus: %v", err)
	}
	got, err = store.GetApproval(ctx, env.ORM, "ap-2")
	if err != nil || got.Status != denied || *got.DenialReason != "no" || *got.Metadata != `{"why":"risky"}` || got.ResolvedAt == nil {
		t.Fatalf("GetApproval(ap-2) after deny = %+v, %v", got, err)
	}
	if err := store.UpdateApprovalStatus(ctx, env.ORM, "missing", approved, nil, nil, nil, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateApprovalStatus(missing) err = %v, want sql.ErrNoRows", err)
	}

	// Re-open ap-2 so both lists have something to order.
	if err := store.UpdateApprovalStatus(ctx, env.ORM, "ap-2", pending, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateApprovalStatus(re-open): %v", err)
	}
	pend, err := store.ListPendingApprovalsByChat(ctx, env.ORM, "chat-approvals")
	if err != nil {
		t.Fatalf("ListPendingApprovalsByChat: %v", err)
	}
	twinPend, err := env.Repo.ListPendingApprovalsByChat(ctx, "chat-approvals")
	if err != nil {
		t.Fatalf("Repo.ListPendingApprovalsByChat: %v", err)
	}
	if g, w := ids(pend), ids(twinPend); len(g) != 1 || g[0] != "ap-2" || len(w) != 1 || w[0] != "ap-2" {
		t.Fatalf("ListPendingApprovalsByChat = %v, twin %v; want [ap-2]", g, w)
	}
	all, err := store.ListApprovalsByChat(ctx, env.ORM, "chat-approvals")
	if err != nil {
		t.Fatalf("ListApprovalsByChat: %v", err)
	}
	twinAll, err := env.Repo.ListApprovalsByChat(ctx, "chat-approvals")
	if err != nil {
		t.Fatalf("Repo.ListApprovalsByChat: %v", err)
	}
	if g, w := ids(all), ids(twinAll); len(g) != 2 || g[0] != "ap-2" || g[1] != "ap-1" || len(w) != 2 || w[0] != "ap-2" {
		t.Fatalf("ListApprovalsByChat = %v, twin %v; want [ap-2 ap-1], newest first", g, w)
	}
}

// TestAttachmentQueries ports attachments.sql, interleaved with the
// Repository.
func TestAttachmentQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	before := time.Now().UTC().Add(-time.Second)

	img := &core.Attachment{
		ID: "att-img", UserID: storetest.SeedUserID, Filename: "a.png", Size: 3, MimeType: "image/png",
		FileHash: strp("sha256:abc"), FilePath: "u/a.png", AttachmentType: "image", Content: []byte{1, 2, 3},
	}
	if err := store.InsertAttachment(ctx, env.ORM, img); err != nil {
		t.Fatalf("InsertAttachment: %v", err)
	}
	if img.CreatedAt.Before(before) || !img.UpdatedAt.Equal(img.CreatedAt) {
		t.Fatalf("InsertAttachment stamps = {%v, %v}, want both the write time", img.CreatedAt, img.UpdatedAt)
	}
	if err := env.Repo.CreateAttachment(ctx, &core.Attachment{
		ID: "att-ref", UserID: storetest.SeedUserID, Filename: "b.go", Size: 10, MimeType: "text/plain", FilePath: "u/b.go", AttachmentType: "file_reference",
	}); err != nil {
		t.Fatalf("Repo.CreateAttachment: %v", err)
	}

	twin, err := env.Repo.GetAttachment(ctx, "att-img")
	if err != nil || twin == nil {
		t.Fatalf("Repo.GetAttachment = %v, %v", twin, err)
	}
	if twin.Filename != "a.png" || twin.Size != 3 || *twin.FileHash != "sha256:abc" || string(twin.Content) != "\x01\x02\x03" || twin.AttachmentType != "image" {
		t.Fatalf("Repo.GetAttachment = %+v, want the store's write", twin)
	}
	ref, err := store.GetAttachment(ctx, env.ORM, "att-ref")
	if err != nil || ref == nil || ref.FileHash != nil || ref.Content != nil || ref.CreatedAt.Before(before) {
		t.Fatalf("GetAttachment(att-ref) = %+v, %v; want the Repository's write with nil hash and content", ref, err)
	}
	if got, err := store.GetAttachment(ctx, env.ORM, "missing"); got != nil || err != nil {
		t.Fatalf("GetAttachment(missing) = %v, %v; want (nil, nil)", got, err)
	}

	byIDs, err := store.GetAttachmentsByIDs(ctx, env.ORM, []string{"att-ref", "att-img", "missing"})
	if err != nil {
		t.Fatalf("GetAttachmentsByIDs: %v", err)
	}
	twinByIDs, err := env.Repo.GetAttachmentsByIDs(ctx, []string{"att-ref", "att-img", "missing"})
	if err != nil {
		t.Fatalf("Repo.GetAttachmentsByIDs: %v", err)
	}
	sorted := func(as []*core.Attachment) []string {
		out := make([]string, len(as))
		for i, a := range as {
			out[i] = a.ID
		}
		sort.Strings(out)
		return out
	}
	if g, w := sorted(byIDs), sorted(twinByIDs); len(g) != 2 || g[0] != "att-img" || g[1] != "att-ref" || len(w) != 2 {
		t.Fatalf("GetAttachmentsByIDs = %v, twin %v; want [att-img att-ref]", g, w)
	}
	if one, err := store.GetAttachmentsByIDs(ctx, env.ORM, []string{"att-ref"}); err != nil || len(one) != 1 || one[0].ID != "att-ref" {
		t.Fatalf("GetAttachmentsByIDs(one id) = %v, %v; want [att-ref]", sorted(one), err)
	}
	if empty, err := store.GetAttachmentsByIDs(ctx, env.ORM, nil); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("GetAttachmentsByIDs(nil) = %v, %v; want an empty, non-nil slice", empty, err)
	}

	if err := store.RemoveAttachment(ctx, env.ORM, "att-img"); err != nil {
		t.Fatalf("RemoveAttachment: %v", err)
	}
	if err := store.RemoveAttachment(ctx, env.ORM, "att-img"); err != nil {
		t.Fatalf("RemoveAttachment(again) = %v, want nil", err)
	}
	if got, err := env.Repo.GetAttachment(ctx, "att-img"); got != nil || err != nil {
		t.Fatalf("Repo.GetAttachment after RemoveAttachment = %v, %v; want (nil, nil)", got, err)
	}
}
