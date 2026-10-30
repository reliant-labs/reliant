// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

type provenanceStore struct {
	listWorkflowsStore
	provenance []config.WorkflowProvenance
}

func (s provenanceStore) ProjectWorkflowProvenance(context.Context, string) ([]config.WorkflowProvenance, error) {
	return s.provenance, nil
}

// list_workflows says where each project workflow is defined: on which
// machine, at which file, at which branch and commit, and flags a copy on a
// feature branch or with uncommitted edits. Two machines can run two
// definitions of one name; this is where it shows.
func TestListWorkflowsShowsWhereProjectWorkflowsAreDefined(t *testing.T) {
	const path = "control-plane/.reliant/workflows/release.yaml"
	sha := "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	tool := &listWorkflowsTool{repo: provenanceStore{provenance: []config.WorkflowProvenance{
		{Name: "release", RelativePath: path, Machine: "cicd", Repo: &config.SyncedRepoState{Repo: "control-plane", Branch: "main", Commit: sha, DefaultBranch: "main"}},
		{Name: "release", RelativePath: path, Machine: "laptop", Repo: &config.SyncedRepoState{Repo: "control-plane", Branch: "feat/x", Commit: sha, Dirty: true, DefaultBranch: "main"}},
	}}}
	ctx := createTestContext(t, "chat")
	ctx.Project = &db.Project{ID: "p"}
	var sb strings.Builder
	tool.writeProvenance(ctx, &sb)
	out := sb.String()
	assert.Contains(t, out, "- `release`: machine cicd at "+path+" @ main 1a2b3c4, clean\n")
	assert.Contains(t, out, "- `release`: machine laptop at "+path+" @ feat/x 1a2b3c4, uncommitted changes\n")
	assert.Contains(t, out, "⚠ machine laptop has control-plane checked out on feat/x, not main")
	assert.Contains(t, out, "⚠ machine laptop has uncommitted changes under control-plane/.reliant")
	assert.NotContains(t, strings.Split(out, "laptop")[0], "⚠", "the clean copy on main carries no warning")
}
