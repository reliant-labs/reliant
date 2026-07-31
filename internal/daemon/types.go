package daemon

import "time"

// ---------------------------------------------------------------------------
// FileSystem types
// ---------------------------------------------------------------------------

// ReadFileOpts controls partial file reads.
type ReadFileOpts struct {
	// Offset is the starting line (0-based). Lines before this are skipped.
	Offset int `json:"offset,omitempty"`
	// Limit is the maximum number of lines to return. 0 means no limit.
	Limit int `json:"limit,omitempty"`
}

// FileContent is the result of reading a file.
type FileContent struct {
	// Content is the file text (or the requested slice of it).
	Content string `json:"content"`
	// TotalLines is the total number of lines in the file.
	TotalLines int `json:"total_lines"`
	// Truncated is true if the output was limited by Offset/Limit.
	Truncated bool `json:"truncated"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
}

// WriteResult is the result of writing a file.
type WriteResult struct {
	// OldContent is the previous file content, for diff generation.
	// Empty if the file was newly created.
	OldContent string `json:"old_content,omitempty"`
	// Created is true if the file did not exist before this write.
	Created bool `json:"created"`
	// ModTime is the new modification time after the write.
	ModTime time.Time `json:"mod_time"`
	// BytesWritten is the number of bytes written.
	BytesWritten int `json:"bytes_written"`
}

// PatchEdit describes a single find-and-replace edit within a file.
type PatchEdit struct {
	// OldString is the text to find. Must match exactly (including whitespace).
	OldString string `json:"old_string"`
	// NewString is the replacement text.
	NewString string `json:"new_string"`
	// ReplaceAll replaces all occurrences if true; otherwise only the first match.
	ReplaceAll bool `json:"replace_all,omitempty"`
}

// PatchResult is the result of applying edits to a file.
type PatchResult struct {
	// NewContent is the full file content after all successful edits.
	NewContent string `json:"new_content"`
	// Applied lists the edits that were successfully applied.
	Applied []PatchApplied `json:"applied"`
	// Failed lists the edits that could not be applied.
	Failed []PatchFailed `json:"failed,omitempty"`
}

// PatchApplied describes a successfully applied edit.
type PatchApplied struct {
	// Index is the 0-based index of the edit in the original edits slice.
	Index int `json:"index"`
	// Replaced is the number of replacements made (>1 when ReplaceAll is true).
	Replaced int `json:"replaced"`
}

// PatchFailed describes an edit that could not be applied.
type PatchFailed struct {
	// Index is the 0-based index of the edit in the original edits slice.
	Index int `json:"index"`
	// Reason describes why the edit failed (e.g., "old_string not found").
	Reason string `json:"reason"`
}

// FileStat holds metadata about a file or directory.
type FileStat struct {
	// Exists is false if the path does not exist.
	Exists bool `json:"exists"`
	// Size is the file size in bytes (0 for directories).
	Size int64 `json:"size"`
	// ModTime is the last modification time.
	ModTime time.Time `json:"mod_time"`
	// IsDir is true if the path is a directory.
	IsDir bool `json:"is_dir"`
	// Mode is the file permission string (e.g., "-rw-r--r--").
	Mode string `json:"mode"`
}

// DirEntry represents a single entry in a directory listing.
type DirEntry struct {
	// Name is the entry name (not the full path).
	Name string `json:"name"`
	// IsDir is true if the entry is a directory.
	IsDir bool `json:"is_dir"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
}

// GlobOpts controls glob file search behavior.
type GlobOpts struct {
	// BaseDir restricts the search to a subtree.
	BaseDir string `json:"base_dir,omitempty"`
	// IncludeIgnored includes commonly ignored directories (node_modules, .git, etc.).
	IncludeIgnored bool `json:"include_ignored,omitempty"`
	// MaxResults limits the number of returned paths. 0 means no limit.
	MaxResults int `json:"max_results,omitempty"`
}

// GlobResult is the result of a glob file search.
type GlobResult struct {
	// Files is the list of matching file paths.
	Files []string `json:"files"`
	// Truncated is true if MaxResults was hit.
	Truncated bool `json:"truncated"`
}

// SearchOpts controls file content search behavior.
type SearchOpts struct {
	// BaseDir restricts the search to a subtree.
	BaseDir string `json:"base_dir,omitempty"`
	// FileGlob filters which files to search (e.g., "*.go").
	FileGlob string `json:"file_glob,omitempty"`
	// FileType filters by file type (e.g., "go", "ts"). Maps to ripgrep --type.
	FileType string `json:"file_type,omitempty"`
	// ContextBefore includes N lines before each match.
	ContextBefore int `json:"context_before,omitempty"`
	// ContextAfter includes N lines after each match.
	ContextAfter int `json:"context_after,omitempty"`
	// CaseInsensitive enables case-insensitive matching.
	CaseInsensitive bool `json:"case_insensitive,omitempty"`
	// FixedStrings treats the pattern as a literal string, not a regex.
	FixedStrings bool `json:"fixed_strings,omitempty"`
	// WordBoundary matches whole words only.
	WordBoundary bool `json:"word_boundary,omitempty"`
	// Multiline enables multiline mode where . matches newlines.
	Multiline bool `json:"multiline,omitempty"`
	// IncludeIgnored includes commonly ignored directories (node_modules, .git, etc.).
	IncludeIgnored bool `json:"include_ignored,omitempty"`
	// MaxResults limits the number of returned matches. 0 means no limit.
	MaxResults int `json:"max_results,omitempty"`
	// OutputMode controls what is returned: "content", "files_with_matches", or "count".
	OutputMode string `json:"output_mode,omitempty"`
}

// SearchResult is the result of a file content search.
type SearchResult struct {
	// Matches contains the search hits.
	Matches []SearchMatch `json:"matches"`
	// Truncated is true if MaxResults was hit.
	Truncated bool `json:"truncated"`
}

// SearchMatch is a single search hit.
type SearchMatch struct {
	// File is the path to the matching file.
	File string `json:"file"`
	// Line is the 1-based line number of the match (0 in files_with_matches mode).
	Line int `json:"line,omitempty"`
	// Content is the matching line text (empty in files_with_matches mode).
	Content string `json:"content,omitempty"`
	// MatchCount is the number of matches in this file (used in count mode).
	MatchCount int `json:"match_count,omitempty"`
}

// FindReplaceOpts controls find-and-replace behavior.
type FindReplaceOpts struct {
	// BaseDir restricts the operation to a subtree.
	BaseDir string `json:"base_dir,omitempty"`
	// FileGlob filters which files to operate on (e.g., "**/*.go").
	FileGlob string `json:"file_glob,omitempty"`
	// UseRegex treats the find pattern as a regex with capture group support.
	UseRegex bool `json:"use_regex,omitempty"`
	// IgnoreCase enables case-insensitive matching.
	IgnoreCase bool `json:"ignore_case,omitempty"`
	// Preview returns diffs without modifying any files.
	Preview bool `json:"preview,omitempty"`
}

// FindReplaceResult is the result of a find-and-replace operation.
type FindReplaceResult struct {
	// FilesChanged is the number of files that were modified (or would be in preview).
	FilesChanged int `json:"files_changed"`
	// Changes lists per-file details.
	Changes []FindReplaceChange `json:"changes"`
}

// FindReplaceChange describes the replacements made in a single file.
type FindReplaceChange struct {
	// File is the path to the changed file.
	File string `json:"file"`
	// Replacements is the number of replacements made in this file.
	Replacements int `json:"replacements"`
	// Diff is a unified diff preview (populated only in preview mode).
	Diff string `json:"diff,omitempty"`
}

// ---------------------------------------------------------------------------
// Executor types
// ---------------------------------------------------------------------------

// RunCommandRequest describes a command to execute.
type RunCommandRequest struct {
	// Command is the shell command to run (passed to bash -c).
	Command string `json:"command"`
	// WorkingDir is the working directory for the command. Defaults to daemon cwd.
	WorkingDir string `json:"working_dir,omitempty"`
	// Env is additional environment variables to set for the command.
	Env map[string]string `json:"env,omitempty"`
	// TimeoutMs is the maximum execution time in milliseconds. 0 means default timeout.
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

// CommandResult is the result of a synchronous command execution.
type CommandResult struct {
	// ExitCode is the process exit code.
	ExitCode int `json:"exit_code"`
	// Stdout is the standard output.
	Stdout string `json:"stdout"`
	// Stderr is the standard error output.
	Stderr string `json:"stderr"`
	// Combined is the interleaved stdout+stderr in chronological order.
	Combined string `json:"combined"`
	// DurationMs is the wall-clock execution time in milliseconds.
	DurationMs int64 `json:"duration_ms"`
	// TimedOut is true if the command exceeded TimeoutMs.
	TimedOut bool `json:"timed_out"`
	// OOMKilled is true when the command's death was attributed to the kernel
	// OOM killer (SIGKILL-shaped exit plus an oom_kill recorded in the
	// workspace cgroup during the command's lifetime). The actionable
	// explanation is appended to Stderr/Combined so all consumers surface it.
	OOMKilled bool `json:"oom_killed,omitempty"`
	// OutputIncomplete is true when the command finished but a process it
	// spawned outlived it holding the output pipe open, so collection stopped
	// at ExecWaitDelay instead of at EOF. Stdout/Stderr are complete up to that
	// point and ExitCode is the command's own. The explanation is appended to
	// Stderr/Combined so all consumers surface it.
	OutputIncomplete bool `json:"output_incomplete,omitempty"`
}

// OutputOpts controls how background process output is retrieved.
// Tail mode and regex mode are mutually exclusive.
type OutputOpts struct {
	// Offset is the byte offset to start reading from.
	Offset int `json:"offset,omitempty"`
	// Limit is the maximum number of bytes to return.
	Limit int `json:"limit,omitempty"`
	// TailLines returns only the last N lines. Cannot be combined with Regex.
	TailLines int `json:"tail_lines,omitempty"`
	// Regex filters output to lines matching this pattern. Cannot be combined with TailLines.
	Regex string `json:"regex,omitempty"`
}

// ProcessOutput is a slice of output from a background process.
type ProcessOutput struct {
	// Output is the requested output text.
	Output string `json:"output"`
	// HasMore is true if more output is available beyond this slice.
	HasMore bool `json:"has_more"`
	// NextOffset is the byte offset to use for the next read.
	NextOffset int `json:"next_offset"`
	// TotalBytes is the total output size available.
	TotalBytes int `json:"total_bytes"`
}

// PortInfo describes a network port a background process is listening on.
// Address is the bind address (e.g. "0.0.0.0", "127.0.0.1", "::") and is
// load-bearing for preview: only publicly-bound ports (0.0.0.0/::) are
// reachable through the workspace proxy, so a 127.0.0.1-only dev server is not
// previewable and callers surface nothing for it.
type PortInfo struct {
	// Port is the TCP/UDP port number.
	Port int `json:"port"`
	// Protocol is "tcp" or "udp".
	Protocol string `json:"protocol"`
	// State is the socket state (e.g. "LISTEN").
	State string `json:"state"`
	// Address is the bind address (e.g. "0.0.0.0", "127.0.0.1", "::").
	Address string `json:"address"`
}

// ProcessInfo describes a background process.
type ProcessInfo struct {
	// ID is the unique process identifier.
	ID string `json:"id"`
	// Command is the command that was executed.
	Command string `json:"command"`
	// Status is the process state: "running", "completed", "failed", or "killed".
	Status string `json:"status"`
	// ExitCode is the process exit code (nil if still running).
	ExitCode *int `json:"exit_code,omitempty"`
	// StartTime is when the process was started.
	StartTime time.Time `json:"start_time"`
	// EndTime is when the process exited (nil if still running).
	EndTime *time.Time `json:"end_time,omitempty"`
	// Ports are the network ports the process is currently listening on
	// (running processes only). Used to surface a proxied preview URL to the
	// agent for dev servers.
	Ports []PortInfo `json:"ports,omitempty"`
	// DaemonID is the control-plane identity of the daemon this process runs on,
	// stamped by the remote daemon runtime so the orchestrator can build the
	// env-aware proxied preview URL (empty for a fully-local/in-process daemon,
	// whose loopback is already reachable by the user).
	DaemonID string `json:"daemon_id,omitempty"`
}
