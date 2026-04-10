// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skillTestEnv holds a pre-built in-memory skill slice and the tool under test.
//
// The test tree mirrors the old filesystem fixture:
//
//	go/                (Go development)
//	├── defer/             (Defer patterns)
//	└── error-handling/    (Error handling)
//	    └── wrap-errors/   (Wrap errors with %w)
//	database-migration/    (allowed-tools: bash view)
//	disabled-skill/        (disable-model-invocation: true)
type skillTestEnv struct {
	tool *skillTool
}

func newSkillTestEnv(_ *testing.T) *skillTestEnv {
	skills := []config.StoredSkill{
		{
			SkillPath:   "go",
			Name:        "go",
			Description: "Go development patterns and idioms",
			Scope:       "project",
			Body:        "# Go\n\nGuidance for writing idiomatic Go code.",
		},
		{
			SkillPath:   "go/defer",
			Name:        "defer",
			Description: "Defer patterns for resource cleanup in Go",
			Scope:       "project",
			Body:        "# Defer\n\nUse defer for cleanup. Prefer defer over manual close.",
		},
		{
			SkillPath:   "go/error-handling",
			Name:        "error-handling",
			Description: "Error handling conventions in Go",
			Scope:       "project",
			Body:        "# Error Handling\n\nReturn errors as the last return value.",
		},
		{
			SkillPath:   "go/error-handling/wrap-errors",
			Name:        "wrap-errors",
			Description: "Wrapping errors with context using fmt.Errorf and %w",
			Scope:       "project",
			Body:        "# Wrap Errors\n\nUse fmt.Errorf with %w to wrap errors.",
		},
		{
			SkillPath:    "database-migration",
			Name:         "database-migration",
			Description:  "How to create and run database migrations safely",
			Scope:        "project",
			Body:         "# Database Migration\n\nSteps for writing DB migrations.",
			AllowedTools: []string{"bash", "view"},
		},
		{
			SkillPath:              "disabled-skill",
			Name:                   "disabled-skill",
			Description:            "A skill hidden from model invocation",
			Scope:                  "project",
			Body:                   "# Disabled Skill\n\nThis skill exists but should not be auto-invoked.",
			DisableModelInvocation: true,
		},
	}

	return &skillTestEnv{
		tool: &skillTool{skills: skills},
	}
}

// execute invokes the skill tool and fails the test on unexpected transport errors.
func (e *skillTestEnv) execute(t *testing.T, params SkillParams) ToolResponse {
	t.Helper()
	resp, err := e.tool.Execute(nil, params)
	require.NoError(t, err, "skill tool should not return a transport error")
	return resp
}

// -----------------------------------------------------------------------------
// list
// -----------------------------------------------------------------------------

func TestSkillTool_List_EmptyPath_ReturnsTopLevelOnly(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "list"})
	require.False(t, resp.IsError, "list with empty path should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go", "should list top-level 'go' skill")
	assert.Contains(t, resp.Content, "database-migration", "should list top-level 'database-migration' skill")

	// Nested skills should NOT appear in the top-level listing.
	assert.NotContains(t, resp.Content, "go/defer", "should not list nested 'go/defer'")
	assert.NotContains(t, resp.Content, "go/error-handling", "should not list nested 'go/error-handling'")
	assert.NotContains(t, resp.Content, "wrap-errors", "should not list deeply nested skill")
}

func TestSkillTool_List_WithPath_ReturnsChildren(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "list", Path: "go"})
	require.False(t, resp.IsError, "list with path=go should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go/defer", "should list child 'go/defer'")
	assert.Contains(t, resp.Content, "go/error-handling", "should list child 'go/error-handling'")

	// 'error-handling' has children ('wrap-errors'), so the has-sub-skills hint should appear.
	assert.Contains(t, resp.Content, "has sub-skills", "children with sub-skills should be flagged")

	// The deeply-nested skill should NOT appear — only immediate children.
	assert.NotContains(t, resp.Content, "wrap-errors", "should not include grand-children")
}

func TestSkillTool_List_WithDeepPath_ReturnsChildren(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "list", Path: "go/error-handling"})
	require.False(t, resp.IsError, "list with deep path should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go/error-handling/wrap-errors", "should list deeply nested child")
}

func TestSkillTool_List_NonexistentPath_ReturnsError(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "list", Path: "nonexistent"})
	assert.True(t, resp.IsError, "non-existent path should surface as an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "nonexistent", "error should mention the queried path")
}

func TestSkillTool_List_LeafSkill_ReturnsEmpty(t *testing.T) {
	env := newSkillTestEnv(t)

	// 'go/defer' has no sub-skills.
	resp := env.execute(t, SkillParams{Action: "list", Path: "go/defer"})
	assert.True(t, resp.IsError, "leaf skill listing should produce a no-children error response")
	assert.Contains(t, resp.Content, "go/defer", "error message should reference the queried leaf path")
}

func TestSkillTool_List_IncludesDescription(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "list"})
	require.False(t, resp.IsError)

	assert.Contains(t, resp.Content, "Go development patterns and idioms",
		"top-level listing should include the 'go' skill description")
	assert.Contains(t, resp.Content, "How to create and run database migrations safely",
		"top-level listing should include the 'database-migration' skill description")
}

// -----------------------------------------------------------------------------
// load
// -----------------------------------------------------------------------------

func TestSkillTool_Load_TopLevelSkill_ReturnsBody(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "# Go", "loaded skill should include its markdown body")
	assert.Contains(t, resp.Content, "idiomatic Go code", "loaded skill body content should be present")
}

func TestSkillTool_Load_NestedSkill_ReturnsBody(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go/error-handling"})
	require.False(t, resp.IsError, "nested load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "# Error Handling")
	assert.Contains(t, resp.Content, "last return value")
}

func TestSkillTool_Load_WithSubSkills_AppendsSubSkillsSection(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "Sub-skills available",
		"loading a skill with children should append a sub-skills section")
	assert.Contains(t, resp.Content, "go/defer", "sub-skills list should include 'defer'")
	assert.Contains(t, resp.Content, "go/error-handling", "sub-skills list should include 'error-handling'")
}

func TestSkillTool_Load_WithAllowedTools_AppendsToolsSection(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "database-migration"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "suggests loading these tools",
		"allowed-tools should produce a suggestion section")
	assert.Contains(t, resp.Content, "bash", "suggested tools should include bash")
	assert.Contains(t, resp.Content, "view", "suggested tools should include view")
}

func TestSkillTool_Load_LeafSkill_NoSubSkillsSection(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go/defer"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "# Defer")
	assert.NotContains(t, resp.Content, "Sub-skills available",
		"leaf skill should not include a sub-skills section")
}

func TestSkillTool_Load_NonexistentSkill_ReturnsError(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "nonexistent"})
	assert.True(t, resp.IsError, "loading a non-existent skill should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "not found")
}

func TestSkillTool_Load_PathNormalization(t *testing.T) {
	env := newSkillTestEnv(t)

	// Case-insensitive lookup is explicitly implemented via strings.ToLower.
	t.Run("uppercase", func(t *testing.T) {
		resp := env.execute(t, SkillParams{Action: "load", Path: "GO"})
		require.False(t, resp.IsError, "uppercase lookup should succeed: %s", resp.Content)
		assert.Contains(t, resp.Content, "# Go")
	})

	// Whitespace trimming is also explicitly handled.
	t.Run("leading-whitespace", func(t *testing.T) {
		resp := env.execute(t, SkillParams{Action: "load", Path: "  go"})
		require.False(t, resp.IsError, "whitespace should be trimmed: %s", resp.Content)
		assert.Contains(t, resp.Content, "# Go")
	})

	// NOTE: trailing slash is NOT currently normalized — "go/" will not match "go".
	t.Run("trailing-slash-not-normalized", func(t *testing.T) {
		resp := env.execute(t, SkillParams{Action: "load", Path: "go/"})
		assert.True(t, resp.IsError,
			"current behavior: trailing slash is not normalized; update this test if that changes")
	})
}

func TestSkillTool_Load_SiblingsSection(t *testing.T) {
	env := newSkillTestEnv(t)

	// go/defer has sibling go/error-handling
	resp := env.execute(t, SkillParams{Action: "load", Path: "go/defer"})
	require.False(t, resp.IsError)

	assert.Contains(t, resp.Content, "Related skills available",
		"nested skills should list siblings")
	assert.Contains(t, resp.Content, "go/error-handling",
		"sibling section should include error-handling")
}

// -----------------------------------------------------------------------------
// search
// -----------------------------------------------------------------------------

func TestSkillTool_Search_FindsByName(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "migration"})
	require.False(t, resp.IsError, "search should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "database-migration",
		"search 'migration' should match 'database-migration' via name/path")
}

func TestSkillTool_Search_FindsByDescription(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "error"})
	require.False(t, resp.IsError, "search should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go/error-handling",
		"search should find 'go/error-handling' by name/path")
	assert.Contains(t, resp.Content, "go/error-handling/wrap-errors",
		"search should find nested 'wrap-errors' by its description containing 'error'")
}

func TestSkillTool_Search_FindsNestedSkills(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "defer"})
	require.False(t, resp.IsError, "search should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go/defer",
		"search must traverse nested skills, not only top-level")
}

func TestSkillTool_Search_NoMatches_ReturnsEmptyResults(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "zebra"})
	require.False(t, resp.IsError, "empty results should not be an error: %s", resp.Content)
	assert.Contains(t, strings.ToLower(resp.Content), "no skills found",
		"should tell the user nothing matched")
	assert.Contains(t, resp.Content, "zebra", "should echo the query back")
}

func TestSkillTool_Search_ShowsFullPath(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "wrap"})
	require.False(t, resp.IsError)

	assert.Contains(t, resp.Content, "go/error-handling/wrap-errors",
		"results should include the full skill path")
}

func TestSkillTool_Search_EmptyQuery_ReturnsError(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: ""})
	assert.True(t, resp.IsError, "empty query should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "query is required")
}

// -----------------------------------------------------------------------------
// errors / edge cases
// -----------------------------------------------------------------------------

func TestSkillTool_InvalidAction_ReturnsError(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "delete"})
	assert.True(t, resp.IsError, "unknown action should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "unknown action")
	for _, action := range []string{"load", "list", "search"} {
		assert.Contains(t, resp.Content, action,
			fmt.Sprintf("error should mention supported action %q", action))
	}
}

func TestSkillTool_Load_MissingPath_ReturnsError(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load"})
	assert.True(t, resp.IsError, "load without path should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "path is required")
}

func TestSkillTool_Search_MissingQuery_ReturnsError(t *testing.T) {
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search"})
	assert.True(t, resp.IsError, "search without query should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "query is required")
}

// -----------------------------------------------------------------------------
// SkillsAnnouncement
// -----------------------------------------------------------------------------

func TestSkillsAnnouncement_OnlyTopLevel(t *testing.T) {
	env := newSkillTestEnv(t)
	got := SkillsAnnouncement(env.tool.skills)

	require.NotEmpty(t, got)
	assert.Contains(t, got, "go", "announcement should include top-level 'go'")
	assert.Contains(t, got, "database-migration", "announcement should include top-level 'database-migration'")
	assert.Contains(t, got, "has sub-skills", "top-level 'go' should be flagged as having sub-skills")

	// Nested skills should NOT appear.
	assert.NotContains(t, got, "go/defer", "nested skills must be omitted")
	assert.NotContains(t, got, "wrap-errors", "deeply nested skills must be omitted")
}

func TestSkillsAnnouncement_EmptyReturnsEmpty(t *testing.T) {
	got := SkillsAnnouncement(nil)
	assert.Empty(t, got)
}
