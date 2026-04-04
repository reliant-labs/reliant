// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"regexp"
	"sync"
	"time"
)

// MockRunExecutor intercepts shell command execution for testing.
// It supports pattern matching (glob-style, exact, and regex) to return
// predefined responses for commands.
//
// Usage:
//
//	mock := NewMockRunExecutor()
//	mock.On("go test ./...", MockRunResponse{Stdout: "PASS", ExitCode: 0})
//	mock.OnPattern("npm run *", MockRunResponse{Stdout: "Done", ExitCode: 0})
//	mock.OnRegex(`git (add|commit).*`, MockRunResponse{ExitCode: 0})
type MockRunExecutor struct {
	mu sync.RWMutex

	// Responses maps command patterns to their responses
	// Patterns are checked in order: exact match, then glob, then regex
	responses []mockRunPattern

	// Calls records all executed commands for assertions
	Calls []MockRunCall

	// DefaultResponse is returned when no pattern matches
	DefaultResponse *MockRunResponse

	// SequentialResponses returns responses in order, cycling through them
	// This takes precedence over pattern matching when set
	sequentialResponses []MockRunResponse
	sequentialIndex     int
}

// mockRunPattern holds a pattern and its response
type mockRunPattern struct {
	pattern     string
	patternType patternType
	regex       *regexp.Regexp
	responses   []MockRunResponse // Multiple responses for sequential matching
	callCount   int               // Tracks which response to return
}

type patternType int

const (
	patternExact patternType = iota
	patternGlob
	patternRegex
)

// MockRunResponse represents the result of a mocked command execution
type MockRunResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Error    error
	Delay    time.Duration // Optional delay to simulate execution time
}

// MockRunCall records a command execution for later assertions
type MockRunCall struct {
	Command        string
	Args           []string
	Dir            string
	Env            map[string]string
	Timestamp      time.Time
	EndTimestamp   time.Time
	Completed      bool
	ExecutionDelay time.Duration
}

// NewMockRunExecutor creates a new mock run executor
func NewMockRunExecutor() *MockRunExecutor {
	return &MockRunExecutor{
		Calls: make([]MockRunCall, 0),
		DefaultResponse: &MockRunResponse{
			Stdout:   "",
			Stderr:   "mock: command not found",
			ExitCode: 127,
		},
	}
}

// On registers an exact match pattern with a single response
func (m *MockRunExecutor) On(command string, response MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockRunPattern{
		pattern:     command,
		patternType: patternExact,
		responses:   []MockRunResponse{response},
	})
	return m
}

// OnSequence registers an exact match pattern with multiple responses
// Each call to the command returns the next response in sequence
func (m *MockRunExecutor) OnSequence(command string, responses ...MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockRunPattern{
		pattern:     command,
		patternType: patternExact,
		responses:   responses,
	})
	return m
}

// OnPattern registers a glob-style pattern (supports * and ?)
func (m *MockRunExecutor) OnPattern(pattern string, response MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockRunPattern{
		pattern:     pattern,
		patternType: patternGlob,
		responses:   []MockRunResponse{response},
	})
	return m
}

// OnPatternSequence registers a glob-style pattern with multiple responses
func (m *MockRunExecutor) OnPatternSequence(pattern string, responses ...MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockRunPattern{
		pattern:     pattern,
		patternType: patternGlob,
		responses:   responses,
	})
	return m
}

// OnRegex registers a regex pattern
func (m *MockRunExecutor) OnRegex(pattern string, response MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	re := regexp.MustCompile(pattern)
	m.responses = append(m.responses, mockRunPattern{
		pattern:     pattern,
		patternType: patternRegex,
		regex:       re,
		responses:   []MockRunResponse{response},
	})
	return m
}

// OnRegexSequence registers a regex pattern with multiple responses
func (m *MockRunExecutor) OnRegexSequence(pattern string, responses ...MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	re := regexp.MustCompile(pattern)
	m.responses = append(m.responses, mockRunPattern{
		pattern:     pattern,
		patternType: patternRegex,
		regex:       re,
		responses:   responses,
	})
	return m
}

// SetSequentialResponses sets responses that will be returned in order
// regardless of the command. This takes precedence over pattern matching.
func (m *MockRunExecutor) SetSequentialResponses(responses ...MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sequentialResponses = responses
	m.sequentialIndex = 0
	return m
}

// SetDefault sets the default response for unmatched commands
func (m *MockRunExecutor) SetDefault(response MockRunResponse) *MockRunExecutor {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.DefaultResponse = &response
	return m
}

// Execute executes a command and returns the mocked response
// This is a simpler interface for direct testing
func (m *MockRunExecutor) Execute(ctx context.Context, command string, args []string, dir string, env map[string]string) (stdout, stderr string, exitCode int, err error) {
	stdout, stderr, exitCode, _, err = m.ExecuteCommand(ctx, command, dir, 0, env)
	return
}

// ExecuteCommand implements handlers.RunExecutor interface
// This is used by ExecuteRunStepActivity to mock shell command execution
func (m *MockRunExecutor) ExecuteCommand(
	ctx context.Context,
	command string,
	workingDir string,
	timeoutMs int,
	env map[string]string,
) (stdout, stderr string, exitCode int, interrupted bool, err error) {
	m.mu.Lock()

	callIndex := len(m.Calls)
	m.Calls = append(m.Calls, MockRunCall{
		Command:   command,
		Args:      nil,
		Dir:       workingDir,
		Env:       env,
		Timestamp: time.Now(),
	})

	resp := *m.DefaultResponse

	// Check for sequential responses first
	if len(m.sequentialResponses) > 0 {
		resp = m.sequentialResponses[m.sequentialIndex%len(m.sequentialResponses)]
		m.sequentialIndex++
		m.mu.Unlock()

		if resp.Delay > 0 {
			time.Sleep(resp.Delay)
		}
		m.recordCompletion(callIndex, resp.Delay)
		return resp.Stdout, resp.Stderr, resp.ExitCode, false, resp.Error
	}

	// Find matching pattern
	for i := range m.responses {
		pattern := &m.responses[i]
		if m.matches(pattern, command) {
			resp = pattern.responses[pattern.callCount%len(pattern.responses)]
			pattern.callCount++
			m.mu.Unlock()

			if resp.Delay > 0 {
				time.Sleep(resp.Delay)
			}
			m.recordCompletion(callIndex, resp.Delay)
			return resp.Stdout, resp.Stderr, resp.ExitCode, false, resp.Error
		}
	}

	m.mu.Unlock()

	if resp.Delay > 0 {
		time.Sleep(resp.Delay)
	}
	m.recordCompletion(callIndex, resp.Delay)
	return resp.Stdout, resp.Stderr, resp.ExitCode, false, resp.Error
}

func (m *MockRunExecutor) recordCompletion(callIndex int, delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if callIndex < 0 || callIndex >= len(m.Calls) {
		return
	}

	m.Calls[callIndex].EndTimestamp = time.Now()
	m.Calls[callIndex].Completed = true
	m.Calls[callIndex].ExecutionDelay = delay
}

// matches checks if a command matches a pattern
func (m *MockRunExecutor) matches(pattern *mockRunPattern, command string) bool {
	switch pattern.patternType {
	case patternExact:
		return command == pattern.pattern
	case patternGlob:
		return globMatch(pattern.pattern, command)
	case patternRegex:
		return pattern.regex.MatchString(command)
	}
	return false
}

// globMatch implements glob-style pattern matching
func globMatch(pattern, str string) bool {
	// Convert glob to regex
	regexStr := "^"
	for _, ch := range pattern {
		switch ch {
		case '*':
			regexStr += ".*"
		case '?':
			regexStr += "."
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			regexStr += "\\" + string(ch)
		default:
			regexStr += string(ch)
		}
	}
	regexStr += "$"

	re := regexp.MustCompile(regexStr)
	return re.MatchString(str)
}

// Reset clears all recorded calls and responses
func (m *MockRunExecutor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Calls = make([]MockRunCall, 0)
	m.responses = nil
	m.sequentialResponses = nil
	m.sequentialIndex = 0
}

// CallCount returns the number of times Execute was called
func (m *MockRunExecutor) CallCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.Calls)
}

// GetCalls returns a copy of all recorded calls
func (m *MockRunExecutor) GetCalls() []MockRunCall {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]MockRunCall, len(m.Calls))
	copy(result, m.Calls)
	return result
}

// WasCalled checks if a command matching the pattern was called
func (m *MockRunExecutor) WasCalled(pattern string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, call := range m.Calls {
		if globMatch(pattern, call.Command) {
			return true
		}
	}
	return false
}

// CallCountFor returns the number of times a command matching the pattern was called
func (m *MockRunExecutor) CallCountFor(pattern string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, call := range m.Calls {
		if globMatch(pattern, call.Command) {
			count++
		}
	}
	return count
}
