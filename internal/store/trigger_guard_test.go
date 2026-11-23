// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The empty-id and empty-user guards of the two facade methods wave 25 deleted
// (SetTriggerWebhookSecret, RecentTriggerFirings) live in the store functions:
// the handler reaches them through the generated ext stores, so a blank id
// must fail before any SQL, never write or read "no trigger".
func TestWebhookSecretAndFiringsReadsFailClosedOnABlankID(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	if err := store.NewTriggerExtStore(env.ORM).SetTriggerWebhookSecret(ctx, "", []byte("sealed")); err == nil {
		t.Fatal("SetTriggerWebhookSecret(\"\") = nil, want an error")
	}
	if _, err := store.NewTriggerEventExtStore(env.ORM).RecentTriggerFirings(ctx, "", []string{"t"}, 5); err == nil {
		t.Fatal("RecentTriggerFirings(no user) = nil error, want one")
	}
}
