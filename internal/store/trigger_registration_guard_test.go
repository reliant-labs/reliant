// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The guard the repository facade used to carry now lives in the store
// function: a registration with no trigger id, or no registration at all, is
// refused before any SQL, so the generated store cannot write an orphan row.
func TestUpsertTriggerRegistrationFailsClosedOnAMissingTriggerID(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	if err := store.UpsertTriggerRegistration(ctx, env.ORM, nil); err == nil {
		t.Fatal("UpsertTriggerRegistration(nil) = nil, want an error")
	}
	if err := store.UpsertTriggerRegistration(ctx, env.ORM, &core.TriggerRegistration{Provider: "gmail"}); err == nil {
		t.Fatal("UpsertTriggerRegistration(no trigger id) = nil, want an error")
	}
}
