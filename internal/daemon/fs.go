package daemon

import "context"

// FileSystem provides file operations on the daemon's filesystem.
// All paths are absolute or relative to the daemon's working directory.
// Implementations must be safe for concurrent use.
type FileSystem interface {
	// ReadFile reads a file and returns its content.
	// If opts is non-nil, Offset and Limit control partial reads (line-based).
	// Returns the file content, total line count, and whether output was truncated.
	ReadFile(ctx context.Context, path string, opts *ReadFileOpts) (*FileContent, error)

	// WriteFile writes content to a file, creating parent directories as needed.
	// Returns the previous content (for diff generation) and metadata about the write.
	// If the file didn't exist, WriteResult.Created is true.
	WriteFile(ctx context.Context, path string, content string) (*WriteResult, error)

	// PatchFile applies a set of edits (old_string → new_string replacements) to a file.
	// Each edit is applied independently. If an edit's OldString is not found,
	// it is reported in PatchResult.Failed. Edits that match are applied in order.
	// This is the primitive backing the edit tool.
	PatchFile(ctx context.Context, path string, edits []PatchEdit) (*PatchResult, error)

	// StatFile returns metadata about a file or directory.
	// If the path does not exist, FileStat.Exists is false (no error is returned).
	StatFile(ctx context.Context, path string) (*FileStat, error)

	// ListDirectory lists entries in a directory (non-recursive).
	// Returns an error if the path is not a directory or does not exist.
	ListDirectory(ctx context.Context, path string) ([]DirEntry, error)

	// GlobFiles finds files matching a glob pattern.
	// The pattern follows standard filepath.Match syntax with ** support.
	// Results are sorted by modification time (most recent first).
	GlobFiles(ctx context.Context, pattern string, opts *GlobOpts) (*GlobResult, error)

	// SearchFiles searches file contents using regex or literal patterns (grep-like).
	// The pattern is a regex by default; set SearchOpts.FixedStrings for literal matching.
	// Returns matching files/lines depending on OutputMode.
	SearchFiles(ctx context.Context, pattern string, opts *SearchOpts) (*SearchResult, error)

	// FindReplace performs find-and-replace across files matching a glob.
	// If FindReplaceOpts.Preview is true, returns diffs without modifying files.
	FindReplace(ctx context.Context, pattern string, replacement string, opts *FindReplaceOpts) (*FindReplaceResult, error)

	// CreateDirectory creates a directory and all parent directories.
	// No error if the directory already exists.
	CreateDirectory(ctx context.Context, path string) error

	// DeletePath removes a file or directory.
	// For directories, removal is recursive. No error if the path does not exist.
	DeletePath(ctx context.Context, path string) error

	// ReadBinaryFile reads a file's raw bytes up to maxBytes.
	// Returns the raw bytes, or error if the file exceeds maxBytes or doesn't exist.
	ReadBinaryFile(ctx context.Context, path string, maxBytes int64) ([]byte, error)
}
