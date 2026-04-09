// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"
)

func TestComponentLibraryEmbedding(t *testing.T) {
	// Verify all registered components can be read from the embedded FS
	for _, entry := range componentRegistry {
		content, err := componentsFS.ReadFile(entry.FilePath)
		if err != nil {
			t.Errorf("component %q (path %q): embed read failed: %v", entry.Name, entry.FilePath, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("component %q: embedded content is empty", entry.Name)
		}
	}
}

func TestComponentLibraryRegistry(t *testing.T) {
	// Verify registry is populated
	if len(componentRegistry) == 0 {
		t.Fatal("componentRegistry is empty")
	}
	if len(componentsByName) == 0 {
		t.Fatal("componentsByName is empty")
	}

	// Verify every entry has required fields
	for _, entry := range componentRegistry {
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
		if entry.FilePath == "" {
			t.Errorf("component %q has empty file path", entry.Name)
		}
	}

	// Verify no duplicate names
	seen := make(map[string]bool)
	for _, entry := range componentRegistry {
		if seen[entry.Name] {
			t.Errorf("duplicate component name: %q", entry.Name)
		}
		seen[entry.Name] = true
	}
}

func TestComponentLibraryCategories(t *testing.T) {
	counts := make(map[ComponentCategory]int)
	for _, entry := range componentRegistry {
		counts[entry.Category]++
	}

	expected := map[ComponentCategory]int{
		CategoryLayouts:  10,
		CategoryCharts:   6,
		CategoryDiagrams: 5,
		CategoryDeck:     7,
		CategoryUI:       8,
	}

	for cat, want := range expected {
		got := counts[cat]
		if got != want {
			t.Errorf("category %q: expected %d components, got %d", cat, want, got)
		}
	}
}

func TestComponentLibraryGet(t *testing.T) {
	tool := &componentLibraryTool{}

	// Get existing component
	resp, err := tool.get("quadrant_chart")
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
	resp, err = tool.get("nonexistent")
	if err != nil {
		t.Fatalf("get nonexistent: %v", err)
	}
	if !resp.IsError {
		t.Error("get nonexistent should return error")
	}
}

func TestComponentLibrarySearch(t *testing.T) {
	tool := &componentLibraryTool{}

	// Search by tag
	resp, err := tool.search("", "deck", "")
	if err != nil {
		t.Fatalf("search tag=deck: %v", err)
	}
	if !strings.Contains(resp.Content, "slide_title") {
		t.Error("search tag=deck should find slide_title")
	}

	// Search by category
	resp, err = tool.search("", "", "charts")
	if err != nil {
		t.Fatalf("search category=charts: %v", err)
	}
	if !strings.Contains(resp.Content, "quadrant_chart") {
		t.Error("search category=charts should find quadrant_chart")
	}

	// Search by query
	resp, err = tool.search("funnel", "", "")
	if err != nil {
		t.Fatalf("search query=funnel: %v", err)
	}
	if !strings.Contains(resp.Content, "funnel_chart") {
		t.Error("search query=funnel should find funnel_chart")
	}

	// Search by tag + category
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
	tool := &componentLibraryTool{}

	// List all
	resp, err := tool.list("", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if !strings.Contains(resp.Content, "36 components") {
		t.Errorf("list all should show 36 components, got: %s", resp.Content[:100])
	}

	// List filtered by category
	resp, err = tool.list("", "deck")
	if err != nil {
		t.Fatalf("list category=deck: %v", err)
	}
	if !strings.Contains(resp.Content, "7 components") {
		t.Errorf("list category=deck should show 7 components")
	}
}

func TestComponentLibraryToolInterface(t *testing.T) {
	tool := NewComponentLibraryTool()
	if tool.Name() != "component_library" {
		t.Errorf("tool name = %q, want %q", tool.Name(), "component_library")
	}
	if tool.Description() == "" {
		t.Error("tool description should not be empty")
	}
}
