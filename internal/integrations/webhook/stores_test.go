// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// A bare repository facade no longer owns the delivery inbox or the
// integration routes, so it must not satisfy the receivers' store roles: a
// receiver wired over it would compile and then lose deliveries.
func TestRepoAloneIsNotAWebhookStore(t *testing.T) {
	var repo any = (*db.Repo)(nil)
	if _, ok := repo.(Store); ok {
		t.Error("*db.Repo satisfies webhook.Store")
	}
	if _, ok := repo.(RelayStore); ok {
		t.Error("*db.Repo satisfies webhook.RelayStore")
	}
	if _, ok := repo.(ReceiptStore); ok {
		t.Error("*db.Repo satisfies webhook.ReceiptStore")
	}
}

func TestNewStoresRequiresARepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewStores(nil) did not panic")
		}
	}()
	NewStores(nil)
}
