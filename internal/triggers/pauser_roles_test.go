// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// AutoPause pauses through the generated writes store: the repository facade
// keeps a PauseTrigger for the activity that has not moved yet, and must not
// be mistaken for the pauser, whose method is SystemPauseTrigger.
func TestTriggerPauserIsTheWritesStore(t *testing.T) {
	var _ TriggerPauser = store.TriggerWritesExtStore(nil)
	var facade any = (*db.Repo)(nil)
	if _, ok := facade.(TriggerPauser); ok {
		t.Error("*db.Repo satisfies TriggerPauser")
	}
}
