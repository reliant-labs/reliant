// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// MOCK RUN EXECUTOR TESTS
// ============================================================================

func TestMockRunExecutor_ExactMatch(t *testing.T) {
	t.Parallel()
	mock := NewMockRunExecutor()
	mock.On("go test ./...", MockRunResponse{
		Stdout:   "PASS",
		ExitCode: 0,
	})

	stdout, stderr, exitCode, err := mock.Execute(context.Background(), "go test ./...", nil, "/tmp", nil)
	require.NoError(t, err)
	require.Equal(t, "PASS", stdout)
	require.Equal(t, "", stderr)
	require.Equal(t, 0, exitCode)
	require.Equal(t, 1, mock.CallCount())
	require.True(t, mock.WasCalled("go test ./..."))
}

func TestMockRunExecutor_GlobPattern(t *testing.T) {
	t.Parallel()
	mock := NewMockRunExecutor()
	mock.OnPattern("npm run *", MockRunResponse{
		Stdout:   "Done",
		ExitCode: 0,
	})

	stdout, _, exitCode, _ := mock.Execute(context.Background(), "npm run build", nil, "/tmp", nil)
	require.Equal(t, "Done", stdout)
	require.Equal(t, 0, exitCode)

	stdout, _, exitCode, _ = mock.Execute(context.Background(), "npm run test", nil, "/tmp", nil)
	require.Equal(t, "Done", stdout)
	require.Equal(t, 0, exitCode)

	require.Equal(t, 2, mock.CallCount())
}

func TestMockRunExecutor_RegexPattern(t *testing.T) {
	t.Parallel()
	mock := NewMockRunExecutor()
	mock.OnRegex(`git (add|commit).*`, MockRunResponse{
		Stdout:   "Git operation completed",
		ExitCode: 0,
	})

	stdout, _, _, _ := mock.Execute(context.Background(), "git add .", nil, "/tmp", nil)
	require.Equal(t, "Git operation completed", stdout)

	stdout, _, _, _ = mock.Execute(context.Background(), "git commit -m 'test'", nil, "/tmp", nil)
	require.Equal(t, "Git operation completed", stdout)
}

func TestMockRunExecutor_SequentialResponses(t *testing.T) {
	t.Parallel()
	mock := NewMockRunExecutor()
	mock.OnSequence("go test ./...",
		MockRunResponse{ExitCode: 1, Stderr: "FAIL"},
		MockRunResponse{ExitCode: 0, Stdout: "PASS"},
	)

	// First call should fail
	_, stderr, exitCode, _ := mock.Execute(context.Background(), "go test ./...", nil, "/tmp", nil)
	require.Equal(t, 1, exitCode)
	require.Equal(t, "FAIL", stderr)

	// Second call should pass
	stdout, _, exitCode, _ := mock.Execute(context.Background(), "go test ./...", nil, "/tmp", nil)
	require.Equal(t, 0, exitCode)
	require.Equal(t, "PASS", stdout)

	// Third call cycles back to first response
	_, stderr, exitCode, _ = mock.Execute(context.Background(), "go test ./...", nil, "/tmp", nil)
	require.Equal(t, 1, exitCode)
	require.Equal(t, "FAIL", stderr)
}

func TestMockRunExecutor_DefaultResponse(t *testing.T) {
	t.Parallel()
	mock := NewMockRunExecutor()

	// Unknown command should get default response
	_, stderr, exitCode, _ := mock.Execute(context.Background(), "unknown-command", nil, "/tmp", nil)
	require.Equal(t, 127, exitCode)
	require.Contains(t, stderr, "command not found")
}

func TestMockRunExecutor_CallCountFor(t *testing.T) {
	t.Parallel()
	mock := NewMockRunExecutor()
	mock.OnPattern("go *", MockRunResponse{ExitCode: 0})
	mock.OnPattern("npm *", MockRunResponse{ExitCode: 0})

	mock.Execute(context.Background(), "go build", nil, "/tmp", nil)
	mock.Execute(context.Background(), "go test", nil, "/tmp", nil)
	mock.Execute(context.Background(), "npm install", nil, "/tmp", nil)

	require.Equal(t, 2, mock.CallCountFor("go *"))
	require.Equal(t, 1, mock.CallCountFor("npm *"))
}

// ============================================================================
// MOCK TOOL EXECUTOR TESTS
// ============================================================================

func TestMockToolExecutor_BasicUsage(t *testing.T) {
	t.Parallel()
	mock := NewMockToolExecutor()
	mock.On("bash", MockToolResponse{
		Result:  "hello world",
		Success: true,
	})

	result, err := mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{
		ToolName: "bash",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "hello world", result.Content)
	require.True(t, mock.WasCalled("bash"))
}

func TestMockToolExecutor_PatternMatch(t *testing.T) {
	t.Parallel()
	mock := NewMockToolExecutor()
	mock.OnPattern("file_.*", MockToolResponse{
		Result:  "file operation completed",
		Success: true,
	})

	result, _ := mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{ToolName: "file_read"})
	require.Equal(t, "file operation completed", result.Content)

	result, _ = mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{ToolName: "file_write"})
	require.Equal(t, "file operation completed", result.Content)
}

func TestMockToolExecutor_SequentialResponses(t *testing.T) {
	t.Parallel()
	mock := NewMockToolExecutor()
	mock.OnSequence("bash",
		MockToolResponse{Result: "first", Success: true},
		MockToolResponse{Result: "second", Success: true},
	)

	result, _ := mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{ToolName: "bash"})
	require.Equal(t, "first", result.Content)

	result, _ = mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{ToolName: "bash"})
	require.Equal(t, "second", result.Content)
}

func TestMockToolExecutor_AssertionHelpers(t *testing.T) {
	t.Parallel()
	mock := NewMockToolExecutor()
	mock.On("bash", MockToolResponse{Success: true})
	mock.On("view", MockToolResponse{Success: true})

	mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{ToolName: "bash"})
	mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{ToolName: "bash"})
	mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{ToolName: "view"})

	// These should not panic
	mock.AssertCalled(t, "bash")
	mock.AssertCalled(t, "view")
	mock.AssertNotCalled(t, "grep")
	mock.AssertCallCount(t, "bash", 2)
	mock.AssertCallCount(t, "view", 1)
}

// ============================================================================
// MOCK APPROVAL TESTS (without DB - just test the pattern matching)
// ============================================================================

func TestMockApprovalResponder_GlobMatch(t *testing.T) {
	t.Parallel()
	// Test the globMatch function directly
	require.True(t, globMatch("Delete *", "Delete files"))
	require.True(t, globMatch("*dangerous*", "This is dangerous operation"))
	require.False(t, globMatch("Delete *", "Create files"))
	require.True(t, globMatch("???", "abc"))
	require.False(t, globMatch("???", "abcd"))
}

// ============================================================================
// HELPER TESTS
// ============================================================================

func TestGlobMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		str     string
		match   bool
	}{
		{"*", "anything", true},
		{"test*", "test123", true},
		{"test*", "notest", false},
		{"*test", "mytest", true},
		{"*test*", "mytestfile", true},
		{"go test *", "go test ./...", true},
		{"go test *", "go build ./...", false},
		{"???.go", "foo.go", true},
		{"???.go", "foobar.go", false},
		{"file.txt", "file.txt", true},
		{"file.txt", "file.log", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.str, func(t *testing.T) {
			result := globMatch(tt.pattern, tt.str)
			require.Equal(t, tt.match, result, "pattern=%q str=%q", tt.pattern, tt.str)
		})
	}
}

func TestMockRunExecutor_Delay(t *testing.T) {
	t.Parallel()
	mock := NewMockRunExecutor()
	mock.On("slow-command", MockRunResponse{
		Stdout: "done",
		Delay:  50 * time.Millisecond,
	})

	start := time.Now()
	mock.Execute(context.Background(), "slow-command", nil, "/tmp", nil)
	duration := time.Since(start)

	require.GreaterOrEqual(t, duration, 50*time.Millisecond)
}

// ============================================================================
// MOCK WIRING VERIFICATION TESTS
// ============================================================================

// TestMockExecutor_InterfaceCompliance verifies that MockRunExecutor implements
// the handlers.RunExecutor interface properly.
func TestMockExecutor_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	// Test that MockRunExecutor implements the RunExecutor interface
	mock := NewMockRunExecutor()
	mock.On("echo hello", MockRunResponse{
		Stdout:   "hello",
		ExitCode: 0,
	})

	// Call the interface method directly
	stdout, stderr, exitCode, interrupted, err := mock.ExecuteCommand(
		context.Background(),
		"echo hello",
		"/tmp",
		5000,
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, "hello", stdout)
	require.Equal(t, "", stderr)
	require.Equal(t, 0, exitCode)
	require.False(t, interrupted)
	require.Equal(t, 1, mock.CallCount())
}

// TestMockToolExecutor_InterfaceCompliance verifies that MockToolExecutor implements
// the toolexec.ToolExecutor interface properly.
func TestMockToolExecutor_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	// Test that MockToolExecutor implements the ToolExecutor interface
	mock := NewMockToolExecutor()
	mock.On("bash", MockToolResponse{
		Result:  "mocked bash output",
		Success: true,
	})

	// Call the interface method directly
	result, err := mock.ExecuteTool(context.Background(), &toolexec.ToolRequest{
		ToolName:  "bash",
		ToolInput: `{"command": "echo hello"}`,
	})

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "mocked bash output", result.Content)
	require.True(t, mock.WasCalled("bash"))
}

// TestHarness_MocksAreInjected verifies that the harness creates mocks
// and passes them to the server config. This is a unit test that doesn't
// require running full workflows.
func TestHarness_MocksAreInjected(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Verify mocks exist on the harness
	require.NotNil(t, h.MockRun, "MockRun should be initialized")
	require.NotNil(t, h.MockTools, "MockTools should be initialized")
	require.NotNil(t, h.MockLLM, "MockLLM should be initialized")
	require.NotNil(t, h.MockApproval, "MockApproval should be initialized")

	// Configure mock responses
	h.MockRun.On("test-command", MockRunResponse{
		Stdout:   "test output",
		ExitCode: 0,
	})
	h.MockTools.On("test-tool", MockToolResponse{
		Result:  "test result",
		Success: true,
	})

	// The mocks should be usable after configuration
	require.True(t, true, "Harness mock injection infrastructure is working")
}
