// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// MockToolExecutor intercepts tool calls (bash, file operations, etc.) for testing.
// It can be configured to return specific responses for specific tools and
// tracks all tool calls for assertions.
//
// Usage:
//
//	mock := NewMockToolExecutor()
//	mock.On("bash", MockToolResponse{Result: "hello world", Success: true})
//	mock.OnMatch("view", func(args map[string]any) bool {
//	    return args["file_path"] == "/etc/passwd"
//	}, MockToolResponse{Result: "root:x:0:0:...", Success: true})
type MockToolExecutor struct {
	mu sync.RWMutex

	// Responses maps tool names to their responses
	responses []mockToolPattern

	// Calls records all executed tool calls for assertions
	Calls []ToolExecutorCall

	// DefaultResponse is returned when no pattern matches
	DefaultResponse *MockToolResponse

	// Passthrough, if set, will be called for unmatched tools
	Passthrough toolexec.ToolExecutor

	// SequentialResponses returns responses in order, cycling through them
	// This takes precedence over pattern matching when set
	sequentialResponses []MockToolResponse
	sequentialIndex     int
}

// mockToolPattern holds a tool name pattern and its response
type mockToolPattern struct {
	toolName    string
	namePattern *regexp.Regexp // If set, matches tool name by regex
	argMatcher  func(map[string]interface{}) bool
	responses   []MockToolResponse
	callCount   int
}

// MockToolResponse represents the result of a mocked tool execution
type MockToolResponse struct {
	Result       string
	Success      bool
	IsError      bool
	Backgrounded bool
	Error        error
	Delay        time.Duration // Optional delay to simulate execution time
	Metadata     string        // Optional JSON metadata
}

// ToolExecutorCall records a tool execution for later assertions
// Note: Named differently from ToolExecutorCall in mock_llm.go to avoid conflict
type ToolExecutorCall struct {
	Name          string
	Input         string // JSON string
	Arguments     map[string]interface{}
	ToolCallID    string
	ChatID        string
	ProjectID     string
	WorktreeID    string
	WorktreePath  string
	Timestamp     time.Time
	MatchedBy     string // Which pattern matched this call
	ActualRequest *toolexec.ToolRequest
}

// NewMockToolExecutor creates a new mock tool executor
func NewMockToolExecutor() *MockToolExecutor {
	return &MockToolExecutor{
		Calls: make([]ToolExecutorCall, 0),
		DefaultResponse: &MockToolResponse{
			Result:  "mock tool executed",
			Success: true,
		},
	}
}

// On registers an exact tool name match with a single response
func (m *MockToolExecutor) On(toolName string, response MockToolResponse) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockToolPattern{
		toolName:  toolName,
		responses: []MockToolResponse{response},
	})
	return m
}

// OnSequence registers an exact tool name match with multiple responses
// Each call to the tool returns the next response in sequence
func (m *MockToolExecutor) OnSequence(toolName string, responses ...MockToolResponse) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockToolPattern{
		toolName:  toolName,
		responses: responses,
	})
	return m
}

// OnMatch registers a tool name with an argument matcher
func (m *MockToolExecutor) OnMatch(toolName string, argMatcher func(map[string]interface{}) bool, response MockToolResponse) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockToolPattern{
		toolName:   toolName,
		argMatcher: argMatcher,
		responses:  []MockToolResponse{response},
	})
	return m
}

// OnMatchSequence registers a tool name with an argument matcher and multiple responses
func (m *MockToolExecutor) OnMatchSequence(toolName string, argMatcher func(map[string]interface{}) bool, responses ...MockToolResponse) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockToolPattern{
		toolName:   toolName,
		argMatcher: argMatcher,
		responses:  responses,
	})
	return m
}

// OnPattern registers a regex pattern for tool names
func (m *MockToolExecutor) OnPattern(pattern string, response MockToolResponse) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	re := regexp.MustCompile(pattern)
	m.responses = append(m.responses, mockToolPattern{
		namePattern: re,
		responses:   []MockToolResponse{response},
	})
	return m
}

// SetSequentialResponses sets responses that will be returned in order
// regardless of the tool. This takes precedence over pattern matching.
func (m *MockToolExecutor) SetSequentialResponses(responses ...MockToolResponse) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sequentialResponses = responses
	m.sequentialIndex = 0
	return m
}

// SetDefault sets the default response for unmatched tools
func (m *MockToolExecutor) SetDefault(response MockToolResponse) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.DefaultResponse = &response
	return m
}

// SetPassthrough sets a passthrough executor for unmatched tools
func (m *MockToolExecutor) SetPassthrough(executor toolexec.ToolExecutor) *MockToolExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Passthrough = executor
	return m
}

// ExecuteTool executes a tool and returns the mocked response
// This implements the toolexec.ToolExecutor interface
func (m *MockToolExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	m.mu.Lock()

	// Parse arguments from JSON input for matching
	var args map[string]interface{}
	// Note: We store the raw input, but for argument matching we'd need to parse
	// For now, we'll just use the tool name for matching

	// Record the call
	call := ToolExecutorCall{
		Name:          req.ToolName,
		Input:         req.ToolInput,
		Arguments:     args,
		ToolCallID:    req.ToolCallID,
		ChatID:        req.ChatID,
		ProjectID:     req.ProjectID,
		WorktreeID:    req.WorktreeID,
		WorktreePath:  req.WorktreePath,
		Timestamp:     time.Now(),
		ActualRequest: req,
	}

	// Check for sequential responses first
	if len(m.sequentialResponses) > 0 {
		resp := m.sequentialResponses[m.sequentialIndex%len(m.sequentialResponses)]
		m.sequentialIndex++
		call.MatchedBy = "sequential"
		m.Calls = append(m.Calls, call)
		m.mu.Unlock()

		if resp.Delay > 0 {
			time.Sleep(resp.Delay)
		}
		return m.toToolResult(resp), resp.Error
	}

	// Find matching pattern
	for i := range m.responses {
		pattern := &m.responses[i]
		if m.matchesTool(pattern, req.ToolName, args) {
			resp := pattern.responses[pattern.callCount%len(pattern.responses)]
			pattern.callCount++
			call.MatchedBy = pattern.toolName
			if pattern.namePattern != nil {
				call.MatchedBy = "pattern:" + pattern.namePattern.String()
			}
			m.Calls = append(m.Calls, call)
			m.mu.Unlock()

			if resp.Delay > 0 {
				time.Sleep(resp.Delay)
			}
			return m.toToolResult(resp), resp.Error
		}
	}

	// Check for passthrough
	if m.Passthrough != nil {
		call.MatchedBy = "passthrough"
		m.Calls = append(m.Calls, call)
		m.mu.Unlock()
		return m.Passthrough.ExecuteTool(ctx, req)
	}

	// Return default response
	call.MatchedBy = "default"
	m.Calls = append(m.Calls, call)
	resp := m.DefaultResponse
	m.mu.Unlock()

	if resp.Delay > 0 {
		time.Sleep(resp.Delay)
	}
	return m.toToolResult(*resp), resp.Error
}

// Close implements the toolexec.ToolExecutor interface
func (m *MockToolExecutor) Close() error {
	return nil
}

// toToolResult converts a MockToolResponse to a toolexec.ToolResult
func (m *MockToolExecutor) toToolResult(resp MockToolResponse) *toolexec.ToolResult {
	result := &toolexec.ToolResult{
		Success:      resp.Success,
		IsError:      resp.IsError,
		Backgrounded: resp.Backgrounded,
		Content:      resp.Result,
		Metadata:     resp.Metadata,
		StartTime:    time.Now(),
		EndTime:      time.Now(),
	}
	if resp.Error != nil {
		result.ErrorMessage = resp.Error.Error()
	}
	return result
}

// matchesTool checks if a tool call matches a pattern
func (m *MockToolExecutor) matchesTool(pattern *mockToolPattern, toolName string, args map[string]interface{}) bool {
	// Check name pattern first
	if pattern.namePattern != nil {
		if !pattern.namePattern.MatchString(toolName) {
			return false
		}
	} else if pattern.toolName != "" {
		if !strings.EqualFold(pattern.toolName, toolName) {
			return false
		}
	}

	// Check argument matcher if present
	if pattern.argMatcher != nil {
		return pattern.argMatcher(args)
	}

	return true
}

// Reset clears all recorded calls and responses
func (m *MockToolExecutor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Calls = make([]ToolExecutorCall, 0)
	m.responses = nil
	m.sequentialResponses = nil
	m.sequentialIndex = 0
}

// CallCount returns the number of times ExecuteTool was called
func (m *MockToolExecutor) CallCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.Calls)
}

// GetCalls returns a copy of all recorded calls
func (m *MockToolExecutor) GetCalls() []ToolExecutorCall {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]ToolExecutorCall, len(m.Calls))
	copy(result, m.Calls)
	return result
}

// WasCalled checks if a tool with the given name was called
func (m *MockToolExecutor) WasCalled(toolName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, call := range m.Calls {
		if strings.EqualFold(call.Name, toolName) {
			return true
		}
	}
	return false
}

// CallCountFor returns the number of times a specific tool was called
func (m *MockToolExecutor) CallCountFor(toolName string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, call := range m.Calls {
		if strings.EqualFold(call.Name, toolName) {
			count++
		}
	}
	return count
}

// GetCallsFor returns all calls for a specific tool
func (m *MockToolExecutor) GetCallsFor(toolName string) []ToolExecutorCall {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []ToolExecutorCall
	for _, call := range m.Calls {
		if strings.EqualFold(call.Name, toolName) {
			result = append(result, call)
		}
	}
	return result
}

// AssertCalled is a test helper that fails if the tool was not called
func (m *MockToolExecutor) AssertCalled(t interface {
	Helper()
	Fatalf(string, ...interface{})
}, toolName string) {
	t.Helper()
	if !m.WasCalled(toolName) {
		t.Fatalf("expected tool %q to be called, but it was not. Called tools: %v", toolName, m.getCalledToolNames())
	}
}

// AssertNotCalled is a test helper that fails if the tool was called
func (m *MockToolExecutor) AssertNotCalled(t interface {
	Helper()
	Fatalf(string, ...interface{})
}, toolName string) {
	t.Helper()
	if m.WasCalled(toolName) {
		t.Fatalf("expected tool %q to NOT be called, but it was called %d times", toolName, m.CallCountFor(toolName))
	}
}

// AssertCallCount is a test helper that fails if the call count doesn't match
func (m *MockToolExecutor) AssertCallCount(t interface {
	Helper()
	Fatalf(string, ...interface{})
}, toolName string, expected int) {
	t.Helper()
	actual := m.CallCountFor(toolName)
	if actual != expected {
		t.Fatalf("expected tool %q to be called %d times, but it was called %d times", toolName, expected, actual)
	}
}

// getCalledToolNames returns a list of unique tool names that were called
func (m *MockToolExecutor) getCalledToolNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, call := range m.Calls {
		if !seen[call.Name] {
			seen[call.Name] = true
			names = append(names, call.Name)
		}
	}
	return names
}
