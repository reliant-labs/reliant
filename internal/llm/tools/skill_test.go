// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"sort"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "list", Path: "go/error-handling"})
	require.False(t, resp.IsError, "list with deep path should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go/error-handling/wrap-errors", "should list deeply nested child")
}

func TestSkillTool_List_NonexistentPath_ReturnsError(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "list", Path: "nonexistent"})
	assert.True(t, resp.IsError, "non-existent path should surface as an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "nonexistent", "error should mention the queried path")
}

func TestSkillTool_List_LeafSkill_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	// 'go/defer' has no sub-skills.
	resp := env.execute(t, SkillParams{Action: "list", Path: "go/defer"})
	assert.True(t, resp.IsError, "leaf skill listing should produce a no-children error response")
	assert.Contains(t, resp.Content, "go/defer", "error message should reference the queried leaf path")
}

func TestSkillTool_List_IncludesDescription(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "# Go", "loaded skill should include its markdown body")
	assert.Contains(t, resp.Content, "idiomatic Go code", "loaded skill body content should be present")
}

func TestSkillTool_Load_NestedSkill_ReturnsBody(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go/error-handling"})
	require.False(t, resp.IsError, "nested load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "# Error Handling")
	assert.Contains(t, resp.Content, "last return value")
}

func TestSkillTool_Load_WithSubSkills_AppendsSubSkillsSection(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "Sub-skills available",
		"loading a skill with children should append a sub-skills section")
	assert.Contains(t, resp.Content, "go/defer", "sub-skills list should include 'defer'")
	assert.Contains(t, resp.Content, "go/error-handling", "sub-skills list should include 'error-handling'")
}

func TestSkillTool_Load_WithAllowedTools_AppendsToolsSection(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "database-migration"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "suggests loading these tools",
		"allowed-tools should produce a suggestion section")
	assert.Contains(t, resp.Content, "bash", "suggested tools should include bash")
	assert.Contains(t, resp.Content, "view", "suggested tools should include view")
}

func TestSkillTool_Load_LeafSkill_NoSubSkillsSection(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go/defer"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "# Defer")
	assert.NotContains(t, resp.Content, "Sub-skills available",
		"leaf skill should not include a sub-skills section")
}

func TestSkillTool_Load_NonexistentSkill_ReturnsError(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "nonexistent"})
	assert.True(t, resp.IsError, "loading a non-existent skill should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "not found")
}

func TestSkillTool_Load_PathNormalization(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "migration"})
	require.False(t, resp.IsError, "search should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "database-migration",
		"search 'migration' should match 'database-migration' via name/path")
}

func TestSkillTool_Search_FindsByDescription(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "error"})
	require.False(t, resp.IsError, "search should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go/error-handling",
		"search should find 'go/error-handling' by name/path")
	assert.Contains(t, resp.Content, "go/error-handling/wrap-errors",
		"search should find nested 'wrap-errors' by its description containing 'error'")
}

func TestSkillTool_Search_FindsNestedSkills(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "defer"})
	require.False(t, resp.IsError, "search should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "go/defer",
		"search must traverse nested skills, not only top-level")
}

func TestSkillTool_Search_NoMatches_ReturnsEmptyResults(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "zebra"})
	require.False(t, resp.IsError, "empty results should not be an error: %s", resp.Content)
	assert.Contains(t, strings.ToLower(resp.Content), "no skills found",
		"should tell the user nothing matched")
	assert.Contains(t, resp.Content, "zebra", "should echo the query back")
}

func TestSkillTool_Search_ShowsFullPath(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: "wrap"})
	require.False(t, resp.IsError)

	assert.Contains(t, resp.Content, "go/error-handling/wrap-errors",
		"results should include the full skill path")
}

func TestSkillTool_ListAndSearch_RenderNormalizedStoredSkillPaths(t *testing.T) {
	t.Parallel()
	skills := config.NormalizeStoredSkills([]config.StoredSkill{
		{
			Name:        "testing-methodology",
			Description: "Testing methodology",
			Scope:       "builtin",
			Body:        "Testing guidance",
		},
	})
	env := &skillTestEnv{tool: &skillTool{skills: skills}}

	listResp := env.execute(t, SkillParams{Action: "list"})
	require.False(t, listResp.IsError)
	assert.Contains(t, listResp.Content, "- testing-methodology: Testing methodology")
	assert.NotContains(t, listResp.Content, "- :")

	searchResp := env.execute(t, SkillParams{Action: "search", Query: "testing"})
	require.False(t, searchResp.IsError)
	assert.Contains(t, searchResp.Content, "- testing-methodology: Testing methodology")
	assert.NotContains(t, searchResp.Content, "- :")
}

func TestSkillTool_Search_EmptyQuery_ReturnsError(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search", Query: ""})
	assert.True(t, resp.IsError, "empty query should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "query is required")
}

// -----------------------------------------------------------------------------
// errors / edge cases
// -----------------------------------------------------------------------------

func TestSkillTool_InvalidAction_ReturnsError(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load"})
	assert.True(t, resp.IsError, "load without path should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "path is required")
}

func TestSkillTool_Search_MissingQuery_ReturnsError(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "search"})
	assert.True(t, resp.IsError, "search without query should return an error response")
	assert.Contains(t, strings.ToLower(resp.Content), "query is required")
}

// -----------------------------------------------------------------------------
// SkillsAnnouncement
// -----------------------------------------------------------------------------

func TestSkillsAnnouncement_OnlyTopLevel(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	got := SkillsAnnouncement(nil)
	assert.Empty(t, got)
}

// -----------------------------------------------------------------------------
// forge namespace resolution
// -----------------------------------------------------------------------------

// newForgeNamespaceEnv mirrors how reliant actually surfaces a forge project's
// skills: every forge skill is re-keyed under a synthetic "forge/" namespace
// (internal/skills/catalog/forge.go), and in a multi-repo project under a
// further "<repo>/" prefix. forge's own CLI and forge's own skill bodies use
// the bare path, so these are the spellings an agent will type.
func newForgeNamespaceEnv() *skillTestEnv {
	return &skillTestEnv{tool: &skillTool{skills: []config.StoredSkill{
		{
			SkillPath:   "forge",
			Name:        "forge",
			Description: "Forge skills surfaced from this project's sibling forge module",
			Body:        "# Forge skills",
		},
		{
			SkillPath:   "forge/frontend",
			Name:        "frontend",
			Description: "Write Next.js frontends",
			Body:        "# Frontend",
		},
		{
			SkillPath:   "forge/frontend/design",
			Name:        "frontend-design",
			Description: "Visual-design discipline for forge frontends",
			Body:        "# Frontend design\n\nSee the frontend/state skill.",
		},
		{
			SkillPath:   "forge/frontend/state",
			Name:        "frontend-state",
			Description: "Frontend state management",
			Body:        "# Frontend state",
		},
		{
			SkillPath:   "forge/db",
			Name:        "db",
			Description: "Database work",
			Body:        "# DB",
		},
	}}}
}

// The headline friction from real-workflow run 1: six of seven fan-out units
// independently called `skill load frontend/design` — the path `forge skill
// list` prints and `forge skill load` accepts — and each got a 404.
func TestSkillTool_Load_AcceptsPathForgePrints(t *testing.T) {
	t.Parallel()
	env := newForgeNamespaceEnv()

	for _, path := range []string{"frontend/design", "forge/frontend/design", "FRONTEND/DESIGN", "  frontend/design  "} {
		resp := env.execute(t, SkillParams{Action: "load", Path: path})
		require.False(t, resp.IsError, "path %q should resolve: %s", path, resp.Content)
		assert.Contains(t, resp.Content, "# Frontend design", "path %q loaded the wrong skill", path)
	}
}

// forge's skill bodies cross-reference each other unprefixed, so following a
// skill's own advice has to work.
func TestSkillTool_Load_AcceptsUnprefixedCrossReference(t *testing.T) {
	t.Parallel()
	env := newForgeNamespaceEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "frontend/state"})
	require.False(t, resp.IsError, "unprefixed cross-reference should resolve: %s", resp.Content)
	assert.Contains(t, resp.Content, "# Frontend state")

	resp = env.execute(t, SkillParams{Action: "load", Path: "db"})
	require.False(t, resp.IsError, "unprefixed top-level forge skill should resolve: %s", resp.Content)
	assert.Contains(t, resp.Content, "# DB")
}

// Listing a group by the name forge prints must list that group's children,
// and must echo the prefixed path those children carry.
func TestSkillTool_List_AcceptsPathForgePrints(t *testing.T) {
	t.Parallel()
	env := newForgeNamespaceEnv()

	resp := env.execute(t, SkillParams{Action: "list", Path: "frontend"})
	require.False(t, resp.IsError, "unprefixed group listing should succeed: %s", resp.Content)
	assert.Contains(t, resp.Content, "Sub-skills of forge/frontend",
		"header should teach the canonical prefixed path")
	assert.Contains(t, resp.Content, "forge/frontend/design")
	assert.Contains(t, resp.Content, "forge/frontend/state")
}

// A multi-repo project stacks "<repo>/" on top of "forge/". The bare path is
// still what forge prints, so it still has to resolve.
func TestSkillTool_Load_AcceptsPathForgePrints_NestedRepo(t *testing.T) {
	t.Parallel()
	env := &skillTestEnv{tool: &skillTool{skills: []config.StoredSkill{
		{SkillPath: "api/forge/frontend/design", Name: "frontend-design", Body: "# Nested design", Source: "api"},
	}}}

	resp := env.execute(t, SkillParams{Action: "load", Path: "frontend/design"})
	require.False(t, resp.IsError, "nested-repo forge skill should resolve unprefixed: %s", resp.Content)
	assert.Contains(t, resp.Content, "# Nested design")
}

// An exact match must never be shadowed by a suffix match, or a project skill
// could be silently replaced by a forge skill of the same name.
func TestSkillTool_Load_ExactMatchBeatsSuffixMatch(t *testing.T) {
	t.Parallel()
	env := &skillTestEnv{tool: &skillTool{skills: []config.StoredSkill{
		{SkillPath: "deploy", Name: "deploy", Body: "# Project deploy"},
		{SkillPath: "forge/deploy", Name: "deploy", Body: "# Forge deploy"},
	}}}

	resp := env.execute(t, SkillParams{Action: "load", Path: "deploy"})
	require.False(t, resp.IsError, "exact match should resolve: %s", resp.Content)
	assert.Contains(t, resp.Content, "# Project deploy")
	assert.NotContains(t, resp.Content, "# Forge deploy")
}

// Two equally-good suffix candidates must not be guessed between — the caller
// gets the "did you mean" list instead.
func TestSkillTool_Load_AmbiguousSuffix_ReturnsCandidates(t *testing.T) {
	t.Parallel()
	env := &skillTestEnv{tool: &skillTool{skills: []config.StoredSkill{
		{SkillPath: "api/forge/db", Name: "db", Body: "# API db"},
		{SkillPath: "web/forge/db", Name: "db", Body: "# Web db"},
	}}}

	resp := env.execute(t, SkillParams{Action: "load", Path: "db"})
	require.True(t, resp.IsError, "ambiguous path must not silently pick one: %s", resp.Content)
	assert.Contains(t, resp.Content, "api/forge/db")
	assert.Contains(t, resp.Content, "web/forge/db")
}

// The suffix match is component-aligned — a bare word must not match the tail
// of a longer path component.
func TestSkillTool_Load_SuffixMatchIsComponentAligned(t *testing.T) {
	t.Parallel()
	env := &skillTestEnv{tool: &skillTool{skills: []config.StoredSkill{
		{SkillPath: "forge/frontend-design", Name: "frontend-design", Body: "# Hyphenated"},
	}}}

	resp := env.execute(t, SkillParams{Action: "load", Path: "design"})
	assert.True(t, resp.IsError, "'design' must not match the tail of 'frontend-design': %s", resp.Content)
}

// -----------------------------------------------------------------------------
// list/load namespace agreement
// -----------------------------------------------------------------------------

// newForgeProjectSkillsEnv mirrors the skill set a real forge project hands the
// tool. Two independent producers contribute:
//
//   - internal/skills/catalog/forge.go re-keys forge's nested skill tree under
//     the synthetic "forge/" namespace ("frontend/design" -> "forge/frontend/design").
//   - `forge generate` writes a FLAT, hyphenated copy of the same skills into
//     .claude/skills ("frontend/", "frontend-design/"), which the runtime
//     discovers as top-level skills of their own.
//
// So "frontend" exists as a childless top-level skill while its actual children
// live under "forge/frontend/". That collision is what an agent hits.
func newForgeProjectSkillsEnv() *skillTestEnv {
	return &skillTestEnv{tool: &skillTool{skills: []config.StoredSkill{
		// Flat .claude/skills copies.
		{SkillPath: "frontend", Name: "frontend", Description: "Write Next.js frontends", Scope: "claude", Body: "# Frontend"},
		{SkillPath: "frontend-design", Name: "frontend-design", Description: "Visual-design discipline", Scope: "claude", Body: "# Frontend design (flat)"},
		{SkillPath: "frontend-state", Name: "frontend-state", Description: "Frontend state management", Scope: "claude", Body: "# Frontend state (flat)"},
		{SkillPath: "testing", Name: "testing", Description: "Testing methodology", Scope: "claude", Body: "# Testing"},
		{SkillPath: "testing-unit", Name: "testing-unit", Description: "Unit tests", Scope: "claude", Body: "# Testing unit (flat)"},
		{SkillPath: "db", Name: "db", Description: "Database work", Scope: "claude", Body: "# DB (flat)"},

		// Nested forge tree.
		{SkillPath: "forge", Name: "forge", Description: "Forge skills", Scope: "forge", Body: "# Forge skills"},
		{SkillPath: "forge/frontend", Name: "frontend", Description: "Write Next.js frontends", Scope: "forge", Body: "# Frontend"},
		{SkillPath: "forge/frontend/design", Name: "frontend-design", Description: "Visual-design discipline", Scope: "forge", Body: "# Frontend design"},
		{SkillPath: "forge/frontend/state", Name: "frontend-state", Description: "Frontend state management", Scope: "forge", Body: "# Frontend state"},
		{SkillPath: "forge/testing", Name: "testing", Description: "Testing methodology", Scope: "forge", Body: "# Testing"},
		{SkillPath: "forge/testing/unit", Name: "testing-unit", Description: "Unit tests", Scope: "forge", Body: "# Testing unit"},
		{SkillPath: "forge/db", Name: "db", Description: "Database work", Scope: "forge", Body: "# DB"},
	}}}
}

// addressableNamespaces derives, from the skill slice alone, every namespace an
// agent can legitimately type, mapped to the skills that live directly under it.
//
// The rule is the one findSkillByPath already implements for load: a path
// resolves by exact match OR by unique component-aligned suffix. So for a skill
// "forge/frontend/design", `load` accepts "forge/frontend/design" and
// "frontend/design" and "design" — which makes both "forge/frontend" and
// "frontend" namespaces that must list it.
//
// Deriving instead of hard-coding means a renamed or re-homed skill changes the
// expectation with the fixture rather than silently dropping an assertion; the
// caller must still assert the derived set is non-empty.
func addressableNamespaces(skills []config.StoredSkill) map[string][]string {
	out := map[string][]string{}
	for _, s := range skills {
		idx := strings.LastIndex(s.SkillPath, "/")
		if idx < 0 {
			continue // top-level: no parent namespace
		}
		for ns := s.SkillPath[:idx]; ns != ""; {
			out[ns] = append(out[ns], s.SkillPath)
			cut := strings.Index(ns, "/")
			if cut < 0 {
				break
			}
			ns = ns[cut+1:]
		}
	}
	return out
}

// listedSkillPaths pulls the skill paths back out of a list response body.
func listedSkillPaths(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		path, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), ":")
		if !ok {
			continue
		}
		out = append(out, strings.TrimSpace(path))
	}
	return out
}

// list and load must agree on what a namespace contains. The measured defect:
// `list frontend` answered "no sub-skills found under: frontend" while
// `load frontend/design` happily loaded one of its members.
func TestSkillTool_List_NamespaceAgreesWithLoad(t *testing.T) {
	t.Parallel()
	env := newForgeProjectSkillsEnv()

	want := addressableNamespaces(env.tool.skills)
	require.NotEmpty(t, want, "fixture yields no namespaces — this test would assert nothing")

	namespaces := make([]string, 0, len(want))
	for ns := range want {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	for _, ns := range namespaces {
		children := want[ns]
		require.NotEmpty(t, children, "namespace %q derived with no members", ns)

		listResp := env.execute(t, SkillParams{Action: "list", Path: ns})
		require.False(t, listResp.IsError,
			"list %q errored but load can address %v under it: %s", ns, children, listResp.Content)

		assert.ElementsMatch(t, children, listedSkillPaths(listResp.Content),
			"list %q must return exactly the skills load can address under it", ns)

		for _, child := range children {
			byFullPath := env.execute(t, SkillParams{Action: "load", Path: child})
			require.False(t, byFullPath.IsError, "list %q offered %q but load rejected it: %s", ns, child, byFullPath.Content)

			short := ns + "/" + child[strings.LastIndex(child, "/")+1:]
			byNamespace := env.execute(t, SkillParams{Action: "load", Path: short})
			require.False(t, byNamespace.IsError, "load %q must resolve because list %q offers it: %s", short, ns, byNamespace.Content)
		}
	}
}

// A path with genuinely nothing under it must say so actionably — naming the
// namespaces that do exist, so the agent can retry instead of giving up.
func TestSkillTool_List_EmptyNamespace_NamesExistingNamespaces(t *testing.T) {
	t.Parallel()
	env := newForgeProjectSkillsEnv()

	resp := env.execute(t, SkillParams{Action: "list", Path: "nonexistent"})
	require.True(t, resp.IsError, "unknown namespace should error: %s", resp.Content)
	assert.Contains(t, resp.Content, "nonexistent", "error should echo the queried path")

	// Every canonical namespace — the full-path parent of some skill — must be
	// named, so the reply is a menu rather than a dead end.
	canonical := map[string]struct{}{}
	for _, s := range env.tool.skills {
		if idx := strings.LastIndex(s.SkillPath, "/"); idx > 0 {
			canonical[s.SkillPath[:idx]] = struct{}{}
		}
	}
	require.NotEmpty(t, canonical, "fixture yields no namespaces — this test would assert nothing")
	for ns := range canonical {
		assert.Contains(t, resp.Content, ns, "error should name existing namespace %q", ns)
	}
}
