package daemon

import "context"

// Executor provides command execution on the daemon's machine.
// Implementations must be safe for concurrent use.
type Executor interface {
	// RunCommand executes a command synchronously and returns the full result.
	// The command is run via the system shell (bash -c on unix).
	// If TimeoutMs is set and exceeded, CommandResult.TimedOut is true.
	// For long-running commands, use StartBackground instead.
	RunCommand(ctx context.Context, req *RunCommandRequest) (*CommandResult, error)

	// StartBackground starts a command in the background and returns immediately
	// with a process ID that can be used to retrieve output, check status, or kill it.
	StartBackground(ctx context.Context, req *RunCommandRequest) (processID string, err error)

	// GetProcessOutput retrieves output from a background process.
	// Supports offset/limit pagination, tail mode, and regex filtering.
	// Returns ProcessOutput with the requested slice and metadata for further reads.
	GetProcessOutput(ctx context.Context, processID string, opts *OutputOpts) (*ProcessOutput, error)

	// KillProcess terminates a background process.
	// Sends SIGTERM first, then SIGKILL if the process doesn't exit promptly.
	// No error if the process has already exited.
	KillProcess(ctx context.Context, processID string) error

	// ListProcesses lists all background processes, both running and completed.
	ListProcesses(ctx context.Context) ([]*ProcessInfo, error)
}
