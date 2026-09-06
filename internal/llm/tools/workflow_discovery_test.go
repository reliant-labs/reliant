// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listWorkflowsRow pulls one workflow's row out of the rendered markdown table.
// Reading the real output is the point: the round trip this file pins is the
// agent copying a handle out of that text and passing it to get_workflow.
func listWorkflowsRow(t *testing.T, listing, workflowName string) []string {
	t.Helper()
	for _, line := range strings.Split(listing, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i, cell := range cells {
			cells[i] = strings.TrimSpace(cell)
		}
		if len(cells) > 0 && cells[0] == "`"+workflowName+"`" {
			return cells
		}
	}
	t.Fatalf("workflow %q not found in listing:\n%s", workflowName, listing)
	return nil
}

func runListWorkflows(t *testing.T, repo db.Repository, chatID string) string {
	t.Helper()
	resp, err := NewListWorkflowsTool(repo).Run(createTestContext(t, chatID), ToolCall{
		ID: "list", Name: ListWorkflowsToolName, Input: "{}",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "%s", resp.Content)
	return resp.Content
}

func runGetWorkflow(t *testing.T, repo db.Repository, chatID, id string) ToolResponse {
	t.Helper()
	inputJSON, err := json.Marshal(GetWorkflowParams{ID: id})
	require.NoError(t, err)
	resp, err := NewGetWorkflowTool(repo).Run(createTestContext(t, chatID), ToolCall{
		ID: "get", Name: GetWorkflowToolName, Input: string(inputJSON),
	})
	require.NoError(t, err)
	return resp
}

// TestListWorkflowsToGetWorkflowRoundTrip is the chain that was broken: every
// handle list_workflows prints was previously unusable, because the only
// documented call was a `name=` parameter that does not exist and get_workflow
// wanted a UUID it never showed.
func TestListWorkflowsToGetWorkflowRoundTrip(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createValidPersistDraft(t, repo)
	listing := runListWorkflows(t, repo, "")

	cells := listWorkflowsRow(t, listing, draft.Slug)
	require.Len(t, cells, 5, "expected Workflow | ID | Source | Valid | Description")
	assert.Equal(t, "user", cells[2])
	assert.Equal(t, "✓", cells[3])

	t.Run("the slug from the listing fetches the workflow", func(t *testing.T) {
		resp := runGetWorkflow(t, repo, "", draft.Slug)
		assert.False(t, resp.IsError, "%s", resp.Content)
		assert.Contains(t, resp.Content, draft.ID)
		assert.Contains(t, resp.Content, draft.Definition)
	})

	t.Run("the UUID from the listing fetches the workflow", func(t *testing.T) {
		id := strings.Trim(cells[1], "`")
		assert.Equal(t, draft.ID, id, "listing should expose the draft UUID as a second handle")

		resp := runGetWorkflow(t, repo, "", id)
		assert.False(t, resp.IsError, "%s", resp.Content)
		assert.Contains(t, resp.Content, draft.Definition)
	})

	t.Run("the footer advertises only signatures that exist", func(t *testing.T) {
		// `get_workflow(name="...")` was the old footer, and there is no such
		// parameter — following it produced an unrecoverable dead end.
		assert.NotContains(t, listing, `name="`)
		assert.Contains(t, listing, "get_workflow")
		assert.Contains(t, listing, "`id`")
	})
}

// TestGetWorkflowResolvesBuiltinByName covers the other half of the listing:
// builtins have no draft row, so before the fallback every builtin name in the
// table resolved to "workflow not found".
func TestGetWorkflowResolvesBuiltinByName(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	listing := runListWorkflows(t, repo, "")
	cells := listWorkflowsRow(t, listing, "agent")
	assert.Equal(t, "builtin", cells[2])
	assert.Equal(t, "—", cells[1], "builtins have no draft UUID")

	resp := runGetWorkflow(t, repo, "", "agent")
	assert.False(t, resp.IsError, "a builtin name from list_workflows must be fetchable: %s", resp.Content)
	assert.Contains(t, resp.Content, "read-only")
	assert.Contains(t, resp.Content, "```yaml")
	assert.Contains(t, resp.Content, "create_workflow",
		"a read-only workflow must name the action that does work")
}

// TestGetWorkflowPrintsVersion closes the loop on expected_version: four tools
// take it and get_workflow is where it has to come from.
func TestGetWorkflowPrintsVersion(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createValidPersistDraft(t, repo)
	stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
	require.NoError(t, err)

	resp := runGetWorkflow(t, repo, "", draft.ID)
	require.False(t, resp.IsError, "%s", resp.Content)

	versionLine := regexp.MustCompile(`\*\*Version:\*\* ` + "`" + `(\d+)` + "`")
	match := versionLine.FindStringSubmatch(resp.Content)
	require.NotNil(t, match, "get_workflow must print the version it tells you to pass:\n%s", resp.Content)

	// The printed value has to be the one the conflict check compares against,
	// so feed it straight back and require that it is accepted.
	printedVersion := match[1]
	assert.Equal(t, stored.Version, mustParseInt64(t, printedVersion))

	expected := stored.Version
	inputJSON, err := json.Marshal(EditWorkflowParams{
		ID:              draft.ID,
		OldString:       "model: mock",
		NewString:       "model: mock-2",
		ExpectedVersion: &expected,
	})
	require.NoError(t, err)

	editResp, err := NewEditWorkflowTool(repo).Run(createTestContext(t, ""), ToolCall{
		ID: "edit", Name: EditWorkflowToolName, Input: string(inputJSON),
	})
	require.NoError(t, err)
	assert.False(t, editResp.IsError,
		"the version get_workflow printed must satisfy expected_version: %s", editResp.Content)
}

func mustParseInt64(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, r := range s {
		require.True(t, r >= '0' && r <= '9', "not a number: %q", s)
		n = n*10 + int64(r-'0')
	}
	return n
}

// TestListWorkflowsMarksBrokenDraftInvalid ties (a) to the discovery surface:
// once an edit persists is_valid=false the workflow drops out of the usable
// list entirely, rather than continuing to render a ✓.
func TestListWorkflowsMarksBrokenDraftInvalid(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createValidPersistDraft(t, repo)
	require.Contains(t, runListWorkflows(t, repo, ""), draft.Slug)

	inputJSON, err := json.Marshal(EditWorkflowParams{
		ID:        draft.ID,
		OldString: "entry: [agent]",
		NewString: "entry: [no-such-node]",
	})
	require.NoError(t, err)
	_, err = NewEditWorkflowTool(repo).Run(createTestContext(t, ""), ToolCall{
		ID: "break", Name: EditWorkflowToolName, Input: string(inputJSON),
	})
	require.NoError(t, err)

	assert.NotContains(t, runListWorkflows(t, repo, ""), draft.Slug,
		"list_workflows reads ListUsableWorkflowsByUser, which filters is_valid = 1")
}
