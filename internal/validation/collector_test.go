// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sync"
	"testing"
)

func TestLocalCollector_Basic(t *testing.T) {
	c := NewLocalCollector()

	if c.HasAny() {
		t.Error("new collector should be empty")
	}

	err := NewError(CategoryWorkflow, "test error").Build()
	c.Add(err)

	if !c.HasAny() {
		t.Error("collector should have errors after Add")
	}
	if c.Count() != 1 {
		t.Errorf("Count() = %d, want 1", c.Count())
	}
}

func TestLocalCollector_AddNil(t *testing.T) {
	c := NewLocalCollector()
	c.Add(nil)

	if c.HasAny() {
		t.Error("adding nil should not add an error")
	}
}

func TestLocalCollector_AddAll(t *testing.T) {
	c := NewLocalCollector()
	errs := []*Error{
		NewError(CategoryWorkflow, "error 1").Build(),
		NewError(CategoryConfig, "error 2").Build(),
		nil, // should be skipped
	}
	c.AddAll(errs)

	if c.Count() != 2 {
		t.Errorf("Count() = %d, want 2", c.Count())
	}
}

func TestLocalCollector_HasErrors(t *testing.T) {
	c := NewLocalCollector()

	// Add warning only
	c.Add(NewWarning(CategoryWorkflow, "warning").Build())
	if c.HasErrors() {
		t.Error("HasErrors() should be false with only warnings")
	}
	if !c.HasAny() {
		t.Error("HasAny() should be true with warnings")
	}

	// Add error
	c.Add(NewError(CategoryWorkflow, "error").Build())
	if !c.HasErrors() {
		t.Error("HasErrors() should be true with errors")
	}
}

func TestLocalCollector_ByCategory(t *testing.T) {
	c := NewLocalCollector()
	c.Add(NewError(CategoryWorkflow, "wf error 1").Build())
	c.Add(NewError(CategoryConfig, "cfg error").Build())
	c.Add(NewError(CategoryWorkflow, "wf error 2").Build())

	wfErrors := c.ByCategory(CategoryWorkflow)
	if len(wfErrors) != 2 {
		t.Errorf("ByCategory(workflow) = %d errors, want 2", len(wfErrors))
	}

	cfgErrors := c.ByCategory(CategoryConfig)
	if len(cfgErrors) != 1 {
		t.Errorf("ByCategory(config) = %d errors, want 1", len(cfgErrors))
	}

	mcpErrors := c.ByCategory(CategoryMCP)
	if len(mcpErrors) != 0 {
		t.Errorf("ByCategory(mcp) = %d errors, want 0", len(mcpErrors))
	}
}

func TestLocalCollector_BySeverity(t *testing.T) {
	c := NewLocalCollector()
	c.Add(NewError(CategoryWorkflow, "error 1").Build())
	c.Add(NewWarning(CategoryWorkflow, "warning 1").Build())
	c.Add(NewError(CategoryWorkflow, "error 2").Build())

	errors := c.BySeverity(SeverityError)
	if len(errors) != 2 {
		t.Errorf("BySeverity(error) = %d, want 2", len(errors))
	}

	warnings := c.BySeverity(SeverityWarning)
	if len(warnings) != 1 {
		t.Errorf("BySeverity(warning) = %d, want 1", len(warnings))
	}
}

func TestLocalCollector_Clear(t *testing.T) {
	c := NewLocalCollector()
	c.Add(NewError(CategoryWorkflow, "error").Build())
	c.Clear()

	if c.HasAny() {
		t.Error("collector should be empty after Clear()")
	}
}

func TestLocalCollector_Counts(t *testing.T) {
	c := NewLocalCollector()
	c.Add(NewError(CategoryWorkflow, "error 1").Build())
	c.Add(NewWarning(CategoryWorkflow, "warning 1").Build())
	c.Add(NewError(CategoryWorkflow, "error 2").Build())
	c.Add(NewWarning(CategoryWorkflow, "warning 2").Build())

	if c.Count() != 4 {
		t.Errorf("Count() = %d, want 4", c.Count())
	}
	if c.ErrorCount() != 2 {
		t.Errorf("ErrorCount() = %d, want 2", c.ErrorCount())
	}
	if c.WarningCount() != 2 {
		t.Errorf("WarningCount() = %d, want 2", c.WarningCount())
	}
}

func TestLocalCollector_Errors_ReturnsCopy(t *testing.T) {
	c := NewLocalCollector()
	c.Add(NewError(CategoryWorkflow, "error").Build())

	errs1 := c.Errors()
	errs2 := c.Errors()

	// Modify the first slice
	errs1[0] = nil

	// Second slice should be unaffected
	if errs2[0] == nil {
		t.Error("Errors() should return a copy, not the original slice")
	}
}

func TestLocalCollector_FormatErrors(t *testing.T) {
	c := NewLocalCollector()

	// Empty collector
	if c.FormatErrors() != "" {
		t.Error("FormatErrors() should return empty string for empty collector")
	}

	c.Add(WorkflowError("test.yaml", "error 1").Build())
	c.Add(ConfigError("config.yaml", "error 2").Build())

	formatted := c.FormatErrors()
	if formatted == "" {
		t.Error("FormatErrors() should return non-empty string")
	}
	if !contains(formatted, "error 1") || !contains(formatted, "error 2") {
		t.Errorf("FormatErrors() missing expected content: %s", formatted)
	}
}

func TestGlobalCollector_Singleton(t *testing.T) {
	g1 := Global()
	g2 := Global()

	if g1 != g2 {
		t.Error("Global() should return the same instance")
	}
}

func TestGlobalCollector_Deduplication(t *testing.T) {
	// Create a fresh collector for this test to avoid interference
	c := &GlobalCollector{
		errors: make([]*Error, 0),
		seen:   make(map[string]bool),
	}

	err := NewError(CategoryWorkflow, "duplicate error").
		Source("test.yaml").
		Build()

	c.Add(err)
	c.Add(err) // Same error again

	if c.Count() != 1 {
		t.Errorf("Count() = %d, want 1 (duplicates should be ignored)", c.Count())
	}
}

func TestGlobalCollector_DifferentErrorsNotDeduplicated(t *testing.T) {
	c := &GlobalCollector{
		errors: make([]*Error, 0),
		seen:   make(map[string]bool),
	}

	c.Add(NewError(CategoryWorkflow, "error 1").Source("test.yaml").Build())
	c.Add(NewError(CategoryWorkflow, "error 2").Source("test.yaml").Build())

	if c.Count() != 2 {
		t.Errorf("Count() = %d, want 2 (different errors should not be deduplicated)", c.Count())
	}
}

func TestGlobalCollector_ThreadSafety(t *testing.T) {
	c := &GlobalCollector{
		errors: make([]*Error, 0),
		seen:   make(map[string]bool),
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine adds a unique error (different message)
			c.Add(NewError(CategoryWorkflow, fmt.Sprintf("concurrent error %d", i)).
				Source("test.yaml").
				Build())
		}(i)
	}
	wg.Wait()

	// All should be added since they have different messages
	if c.Count() != 100 {
		t.Errorf("Count() = %d, want 100", c.Count())
	}
}

func TestGlobalCollector_ProjectContext(t *testing.T) {
	c := &GlobalCollector{
		errors: make([]*Error, 0),
		seen:   make(map[string]bool),
	}

	c.SetProjectContext("project-123")
	if c.GetProjectContext() != "project-123" {
		t.Errorf("GetProjectContext() = %s, want 'project-123'", c.GetProjectContext())
	}
}

func TestGlobalCollector_GetCounts(t *testing.T) {
	c := &GlobalCollector{
		errors: make([]*Error, 0),
		seen:   make(map[string]bool),
	}

	c.Add(NewError(CategoryWorkflow, "error").Build())
	c.Add(NewWarning(CategoryWorkflow, "warning").Build())

	errCount, warnCount := c.GetCounts()
	if errCount != 1 {
		t.Errorf("error count = %d, want 1", errCount)
	}
	if warnCount != 1 {
		t.Errorf("warning count = %d, want 1", warnCount)
	}
}

func TestGlobalCollector_ThreadSafety_Dedup(t *testing.T) {
	c := &GlobalCollector{
		errors: make([]*Error, 0),
		seen:   make(map[string]bool),
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// All goroutines add the same error
			c.Add(NewError(CategoryWorkflow, "same error").
				Source("test.yaml").
				Build())
		}()
	}
	wg.Wait()

	// Only one should be added due to deduplication
	if c.Count() != 1 {
		t.Errorf("Count() = %d, want 1 (duplicates should be deduplicated)", c.Count())
	}
}

func TestGlobalCollector_Clear(t *testing.T) {
	c := &GlobalCollector{
		errors: make([]*Error, 0),
		seen:   make(map[string]bool),
	}

	c.Add(NewError(CategoryWorkflow, "error").Build())
	c.Clear()

	if c.HasAny() {
		t.Error("collector should be empty after Clear()")
	}

	// Can add the same error again after clear
	c.Add(NewError(CategoryWorkflow, "error").Build())
	if c.Count() != 1 {
		t.Error("should be able to add error after Clear()")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
