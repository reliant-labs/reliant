// Copyright (c) 2025 Reliant Labs
package launch

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// The trigger-event ledger is its own role: the repository facade no longer
// carries the writes a launch makes on it, so it cannot stand in, and a
// launcher given a store without one is refused at construction rather than
// failing its first launch.
func TestFacadeIsNotTheTriggerEventLedger(t *testing.T) {
	var facade any = (*db.Repo)(nil)
	if _, ok := facade.(TriggerEventStore); ok {
		t.Error("*db.Repo satisfies launch.TriggerEventStore")
	}
}

func TestNewRefusesAStoreWithoutTheLedger(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New(Deps{Store: s}) without TriggerEvents did not panic")
		}
	}()
	New(Deps{Store: &MockStore{}, Installs: nil})
}
