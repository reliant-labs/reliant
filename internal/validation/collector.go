// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Collector is the interface for collecting validation errors.
// There are two implementations:
//   - LocalCollector: For validation passes, no deduplication, not thread-safe
//   - GlobalCollector: Singleton for UI display, with deduplication, thread-safe
type Collector interface {
	// Add adds a single error to the collector.
	Add(err *Error)

	// AddAll adds multiple errors to the collector.
	AddAll(errs []*Error)

	// Errors returns all collected errors.
	Errors() []*Error

	// HasErrors returns true if there are any errors (not just warnings).
	HasErrors() bool

	// HasAny returns true if there are any errors or warnings.
	HasAny() bool

	// ByCategory returns errors filtered by category.
	ByCategory(cat Category) []*Error

	// BySeverity returns errors filtered by severity.
	BySeverity(sev Severity) []*Error

	// Clear removes all collected errors.
	Clear()

	// Count returns the total number of errors and warnings.
	Count() int

	// ErrorCount returns the count of errors (not warnings).
	ErrorCount() int

	// WarningCount returns the count of warnings (not errors).
	WarningCount() int
}

// LocalCollector is a simple collector for validation passes.
// It is NOT thread-safe and does NOT deduplicate errors.
// Use this for single validation operations that don't need concurrency.
type LocalCollector struct {
	errors []*Error
}

// NewLocalCollector creates a new local collector.
func NewLocalCollector() *LocalCollector {
	return &LocalCollector{
		errors: make([]*Error, 0),
	}
}

func (c *LocalCollector) Add(err *Error) {
	if err == nil {
		return
	}
	c.errors = append(c.errors, err)
}

func (c *LocalCollector) AddAll(errs []*Error) {
	for _, err := range errs {
		c.Add(err)
	}
}

func (c *LocalCollector) Errors() []*Error {
	result := make([]*Error, len(c.errors))
	copy(result, c.errors)
	return result
}

func (c *LocalCollector) HasErrors() bool {
	for _, err := range c.errors {
		if err.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (c *LocalCollector) HasAny() bool {
	return len(c.errors) > 0
}

func (c *LocalCollector) ByCategory(cat Category) []*Error {
	result := make([]*Error, 0)
	for _, err := range c.errors {
		if err.Category == cat {
			result = append(result, err)
		}
	}
	return result
}

func (c *LocalCollector) BySeverity(sev Severity) []*Error {
	result := make([]*Error, 0)
	for _, err := range c.errors {
		if err.Severity == sev {
			result = append(result, err)
		}
	}
	return result
}

func (c *LocalCollector) Clear() {
	c.errors = make([]*Error, 0)
}

func (c *LocalCollector) Count() int {
	return len(c.errors)
}

func (c *LocalCollector) ErrorCount() int {
	count := 0
	for _, err := range c.errors {
		if err.Severity == SeverityError {
			count++
		}
	}
	return count
}

func (c *LocalCollector) WarningCount() int {
	count := 0
	for _, err := range c.errors {
		if err.Severity == SeverityWarning {
			count++
		}
	}
	return count
}

// FormatErrors returns a formatted string of all errors, suitable for error messages.
func (c *LocalCollector) FormatErrors() string {
	if !c.HasAny() {
		return ""
	}
	var sb strings.Builder
	for i, err := range c.errors {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("  ")
		sb.WriteString(err.Error())
	}
	return sb.String()
}

// GlobalCollector is a thread-safe collector with deduplication.
// It is intended to be used as a singleton for surfacing errors to the UI.
// Errors are logged when added.
type GlobalCollector struct {
	mu        sync.RWMutex
	errors    []*Error
	seen      map[string]bool // Deduplication by error key
	projectID string          // Current project context for filtering
}

// globalInstance is the singleton GlobalCollector instance.
var (
	globalInstance     *GlobalCollector
	globalInstanceOnce sync.Once
)

// Global returns the global collector singleton.
// This replaces the old HealthCollector singleton.
func Global() *GlobalCollector {
	globalInstanceOnce.Do(func() {
		globalInstance = &GlobalCollector{
			errors: make([]*Error, 0),
			seen:   make(map[string]bool),
		}
	})
	return globalInstance
}

// SetProjectContext sets the current project context for filtering.
func (c *GlobalCollector) SetProjectContext(projectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.projectID = projectID
}

// GetProjectContext returns the current project context.
func (c *GlobalCollector) GetProjectContext() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.projectID
}

func (c *GlobalCollector) Add(err *Error) {
	if err == nil {
		return
	}

	key := err.Key()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Deduplicate
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.errors = append(c.errors, err)

	// Log the error for visibility
	if err.IsError() {
		logging.Error("Validation error",
			"category", err.Category,
			"source", err.Source,
			"message", err.Message,
		)
	} else {
		logging.Warn("Validation warning",
			"category", err.Category,
			"source", err.Source,
			"message", err.Message,
		)
	}
}

func (c *GlobalCollector) AddAll(errs []*Error) {
	for _, err := range errs {
		c.Add(err)
	}
}

func (c *GlobalCollector) Errors() []*Error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]*Error, len(c.errors))
	copy(result, c.errors)
	return result
}

func (c *GlobalCollector) HasErrors() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, err := range c.errors {
		if err.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (c *GlobalCollector) HasAny() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.errors) > 0
}

func (c *GlobalCollector) ByCategory(cat Category) []*Error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]*Error, 0)
	for _, err := range c.errors {
		if err.Category == cat {
			result = append(result, err)
		}
	}
	return result
}

func (c *GlobalCollector) BySeverity(sev Severity) []*Error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]*Error, 0)
	for _, err := range c.errors {
		if err.Severity == sev {
			result = append(result, err)
		}
	}
	return result
}

func (c *GlobalCollector) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors = make([]*Error, 0)
	c.seen = make(map[string]bool)
}

func (c *GlobalCollector) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.errors)
}

func (c *GlobalCollector) ErrorCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	count := 0
	for _, err := range c.errors {
		if err.Severity == SeverityError {
			count++
		}
	}
	return count
}

func (c *GlobalCollector) WarningCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	count := 0
	for _, err := range c.errors {
		if err.Severity == SeverityWarning {
			count++
		}
	}
	return count
}

// GetCounts returns the count of errors and warnings (for backwards compatibility).
func (c *GlobalCollector) GetCounts() (errorCount, warningCount int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, err := range c.errors {
		if err.Severity == SeverityError {
			errorCount++
		} else {
			warningCount++
		}
	}
	return
}

// Verify interface compliance at compile time
var (
	_ Collector = (*LocalCollector)(nil)
	_ Collector = (*GlobalCollector)(nil)
)
