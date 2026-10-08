// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Ownership of a daemon row is immutable: a second user upserting the same id
// gets the sentinel and the row is untouched.
func TestUpsertDaemonNeverChangesOwner(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id := uuid.NewString()
	host := "owner-host"

	if err := repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A", Hostname: &host}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same owner may refresh.
	if err := repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A", Hostname: &host}); err != nil {
		t.Fatalf("owner re-upsert: %v", err)
	}

	evil := "evil-host"
	err := repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-B", Hostname: &evil})
	if !errors.Is(err, ErrDaemonOwnedByAnotherUser) {
		t.Fatalf("foreign upsert err = %v, want ErrDaemonOwnedByAnotherUser", err)
	}

	got, err := repo.GetDaemon(ctx, id)
	if err != nil {
		t.Fatalf("GetDaemon: %v", err)
	}
	if got.UserID != "user-A" {
		t.Errorf("owner = %q, want user-A", got.UserID)
	}
	if got.Hostname == nil || *got.Hostname != host {
		t.Errorf("hostname changed by refused upsert: %v", got.Hostname)
	}
}

// A foreign user cannot take over another user's attachment lease either.
func TestUpsertDaemonAttachmentRefusesForeignOwner(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id := uuid.NewString()

	if err := repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{DaemonID: id, UserID: "user-A", Source: DaemonAttachmentSourceInbound}); err != nil {
		t.Fatalf("owner attach: %v", err)
	}
	err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{DaemonID: id, UserID: "user-B", Source: DaemonAttachmentSourceInbound})
	if !errors.Is(err, ErrDaemonOwnedByAnotherUser) {
		t.Fatalf("foreign attach err = %v, want ErrDaemonOwnedByAnotherUser", err)
	}
}
