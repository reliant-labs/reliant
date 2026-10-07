package worktreesweep

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestNotRemovableMessage_NamesTheHeldReasonAndPath(t *testing.T) {
	m := &core.CleanupMetadata{HeldReason: "quarantined", HeldDetail: "1 checkout(s) are parked at /r/.reclaim/x/root-1"}
	got := notRemovableMessage(m)
	if !strings.Contains(got, "quarantined") || !strings.Contains(got, "/r/.reclaim/x/root-1") {
		t.Errorf("message should carry reason and parked path, got %q", got)
	}
	if strings.Contains(got, "contains data") {
		t.Errorf("generic text must be gone: %q", got)
	}
}
