// Copyright (c) 2026 Reliant Labs
package db

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// An approval's feed update names the workflow and thread that raised it, so
// a resolved approval (gone from the pending-only list) is still attributable.
func TestAttributeApprovalUpdate(t *testing.T) {
	metadata := `{"workflow_id":"wf-1","run_id":"r-1"}`
	thread := "t-1"
	update := map[string]any{"id": "a-1"}
	AttributeApprovalUpdate(update, &core.Approval{ID: "a-1", Metadata: &metadata, ThreadID: &thread})
	assert.Equal(t, map[string]any{"id": "a-1", "workflow_id": "wf-1", "thread_id": "t-1"}, update)

	// Neither known: the update stays as it was.
	update = map[string]any{"id": "a-2"}
	AttributeApprovalUpdate(update, &core.Approval{ID: "a-2"})
	assert.Equal(t, map[string]any{"id": "a-2"}, update)
}
