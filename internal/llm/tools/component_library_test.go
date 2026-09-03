// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/components"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

func TestComponentLibraryRegistry(t *testing.T) {
	t.Parallel()
	lib := components.NewLibrary()
	reg := lib.Registry()

	if len(reg) == 0 {
		t.Fatal("registry is empty")
	}
	if len(lib.ByName()) == 0 {
		t.Fatal("byName is empty")
	}

	for _, entry := range reg {
		if entry.Name == "" {
			t.Error("component with empty name")
		}
		if entry.Category == "" {
			t.Errorf("component %q has empty category", entry.Name)
		}
		if entry.Description == "" {
			t.Errorf("component %q has empty description", entry.Name)
		}
		if len(entry.Tags) == 0 {
			t.Errorf("component %q has no tags", entry.Name)
		}
	}

	// No duplicate names
	seen := make(map[string]bool)
	for _, entry := range reg {
		if seen[entry.Name] {
			t.Errorf("duplicate component name: %q", entry.Name)
		}
		seen[entry.Name] = true
	}
}

func TestComponentLibraryCategories(t *testing.T) {
	t.Parallel()
	lib := components.NewLibrary()
	counts := make(map[components.Category]int)
	for _, entry := range lib.Registry() {
		counts[entry.Category]++
	}

	// Each canonical category must be non-empty. Exact counts drift as forge
	// adds components — assert presence, not magic numbers.
	for _, cat := range []components.Category{
		components.CategoryLayouts,
		components.CategoryCharts,
		components.CategoryDiagrams,
		components.CategoryDeck,
		components.CategoryUI,
	} {
		if counts[cat] == 0 {
			t.Errorf("category %q has no components", cat)
		}
	}
}

func TestComponentLibraryGet(t *testing.T) {
	t.Parallel()
	tool := &componentLibraryTool{}

	// Get existing component
	resp, err := tool.get("quadrant_chart", false)
	if err != nil {
		t.Fatalf("get quadrant_chart: %v", err)
	}
	if resp.IsError {
		t.Fatalf("get quadrant_chart returned error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "QuadrantChart") {
		t.Error("quadrant_chart content should contain 'QuadrantChart'")
	}
	if !strings.Contains(resp.Content, "interface") {
		t.Error("quadrant_chart content should contain TypeScript interface")
	}

	// Get non-existing component
	resp, err = tool.get("nonexistent", false)
	if err != nil {
		t.Fatalf("get nonexistent: %v", err)
	}
	if !resp.IsError {
		t.Error("get nonexistent should return error")
	}
}

func TestComponentLibrarySearch(t *testing.T) {
	t.Parallel()
	tool := &componentLibraryTool{}

	// Search by tag keyword
	resp, err := tool.search("", "deck", "")
	if err != nil {
		t.Fatalf("search tag=deck: %v", err)
	}
	if !strings.Contains(resp.Content, "slide_title") {
		t.Error("search tag=deck should find slide_title")
	}

	// Search by category keyword
	resp, err = tool.search("", "", "charts")
	if err != nil {
		t.Fatalf("search category=charts: %v", err)
	}
	if !strings.Contains(resp.Content, "quadrant_chart") {
		t.Error("search category=charts should find quadrant_chart")
	}

	// Search by query keyword
	resp, err = tool.search("funnel", "", "")
	if err != nil {
		t.Fatalf("search query=funnel: %v", err)
	}
	if !strings.Contains(resp.Content, "funnel_chart") {
		t.Error("search query=funnel should find funnel_chart")
	}

	// Unified multi-word search
	resp, err = tool.search("crud admin table", "", "")
	if err != nil {
		t.Fatalf("search 'crud admin table': %v", err)
	}
	if !strings.Contains(resp.Content, "data_table") {
		t.Error("search 'crud admin table' should find data_table")
	}

	// tag + category builds unified string
	resp, err = tool.search("", "competitive", "charts")
	if err != nil {
		t.Fatalf("search tag=competitive category=charts: %v", err)
	}
	if !strings.Contains(resp.Content, "quadrant_chart") {
		t.Error("search tag=competitive category=charts should find quadrant_chart")
	}

	// Search with no results
	resp, err = tool.search("xyznonexistent123", "", "")
	if err != nil {
		t.Fatalf("search no results: %v", err)
	}
	if !strings.Contains(resp.Content, "No components found") {
		t.Error("search with no results should say so")
	}
}

func TestComponentLibraryList(t *testing.T) {
	t.Parallel()
	tool := &componentLibraryTool{}
	lib := components.NewLibrary()

	// list("") should report the registry's actual total. Derived from the
	// library at test time so additions don't break the assertion.
	total := len(lib.Registry())
	resp, err := tool.list("", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	wantAll := fmt.Sprintf("%d components", total)
	if !strings.Contains(resp.Content, wantAll) {
		t.Errorf("list all should report %q, got: %s", wantAll, resp.Content[:min(len(resp.Content), 200)])
	}

	// list("", category) should match the count of components with that
	// category in the registry — again derived, not hardcoded.
	var deckCount int
	for _, e := range lib.Registry() {
		if e.Category == components.CategoryDeck {
			deckCount++
		}
	}
	resp, err = tool.list("", "deck")
	if err != nil {
		t.Fatalf("list category=deck: %v", err)
	}
	wantDeck := fmt.Sprintf("%d components", deckCount)
	if !strings.Contains(resp.Content, wantDeck) {
		t.Errorf("list category=deck should report %q, got: %s", wantDeck, resp.Content[:min(len(resp.Content), 200)])
	}
}

func TestComponentLibraryInstall(t *testing.T) {
	t.Parallel()
	tool := &componentLibraryTool{}

	t.Run("install writes file to disk", func(t *testing.T) {
		tempDir := t.TempDir()
		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).
			WithDaemon(daemon.NewLocalClient())

		destPath := filepath.Join("components", "layouts", "sidebar_left.tsx")
		resp, err := tool.install(ctx, "sidebar_left", destPath, "")
		if err != nil {
			t.Fatalf("install sidebar_left: %v", err)
		}
		if resp.IsError {
			t.Fatalf("install sidebar_left returned error: %s", resp.Content)
		}
		if !strings.Contains(resp.Content, "installed to") {
			t.Errorf("expected success message, got: %s", resp.Content)
		}

		// Verify file was written
		writtenPath := filepath.Join(tempDir, destPath)
		data, err := os.ReadFile(writtenPath)
		if err != nil {
			t.Fatalf("failed to read written file: %v", err)
		}
		if len(data) == 0 {
			t.Error("written file is empty")
		}

		// Verify content matches the library source
		lib := components.NewLibrary()
		expected, _ := lib.Get("sidebar_left")
		if string(data) != expected {
			t.Error("written file content does not match library source")
		}
	})

	t.Run("install with missing name errors", func(t *testing.T) {
		tempDir := t.TempDir()
		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).
			WithDaemon(daemon.NewLocalClient())

		resp, err := tool.Execute(ctx, ComponentLibraryParams{Action: "install", Path: "out.tsx"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.IsError {
			t.Error("expected error for missing name")
		}
		if !strings.Contains(resp.Content, "'name' is required") {
			t.Errorf("unexpected error message: %s", resp.Content)
		}
	})

	t.Run("install with missing path errors", func(t *testing.T) {
		tempDir := t.TempDir()
		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).
			WithDaemon(daemon.NewLocalClient())

		resp, err := tool.Execute(ctx, ComponentLibraryParams{Action: "install", Name: "sidebar_left"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.IsError {
			t.Error("expected error for missing path")
		}
		if !strings.Contains(resp.Content, "'path' is required") {
			t.Errorf("unexpected error message: %s", resp.Content)
		}
	})

	t.Run("install with nonexistent component errors", func(t *testing.T) {
		tempDir := t.TempDir()
		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).
			WithDaemon(daemon.NewLocalClient())

		resp, err := tool.install(ctx, "nonexistent_component", "out.tsx", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.IsError {
			t.Error("expected error for nonexistent component")
		}
		if !strings.Contains(resp.Content, "not found") {
			t.Errorf("unexpected error message: %s", resp.Content)
		}
	})
}

func TestComponentLibraryRequiresPermission(t *testing.T) {
	t.Parallel()
	tool := &componentLibraryTool{}

	// Read-only actions don't require permission
	for _, action := range []string{"search", "get", "list"} {
		req, err := tool.RequiresPermission(ComponentLibraryParams{Action: action})
		if err != nil {
			t.Fatalf("RequiresPermission(%s): %v", action, err)
		}
		if req {
			t.Errorf("action %q should not require permission", action)
		}
	}

	// Install requires permission
	req, err := tool.RequiresPermission(ComponentLibraryParams{Action: "install"})
	if err != nil {
		t.Fatalf("RequiresPermission(install): %v", err)
	}
	if !req {
		t.Error("action 'install' should require permission")
	}
}

func TestComponentLibraryToolInterface(t *testing.T) {
	t.Parallel()
	tool := NewComponentLibraryTool()
	if tool.Name() != "component_library" {
		t.Errorf("tool name = %q, want %q", tool.Name(), "component_library")
	}
	if tool.Description() == "" {
		t.Error("tool description should not be empty")
	}
}
