package daemon

// Staged uploads: the daemon commands behind FileSystemService's
// BeginFileUpload / UploadFileChunk / CommitFileUpload / AbortFileUpload /
// GetFileUploadStatus.
//
// A file is uploaded in pieces into a part file the daemon keeps OUTSIDE the
// workspace (UploadPartialDir, under ~/.reliant), then moved into place
// atomically at commit. Each piece is one ordinary daemon command, small
// enough to stay far below the transport's chunked-request cap, and a session
// survives a failed piece, so an upload resumes from the bytes that arrived.
//
// They are commands of their own, never flags on fs.write_binary_file: a
// daemon ignores a request field it does not know, and answers a command it
// does not know with the registry's "older version of Reliant" error
// (daemonerr.IsOutdated), which the server turns into its whole-file fallback.
//
// Daemon errors cross the wire as plain strings, so the marker texts below are
// the contract between the daemon's handlers (internal/toolexec/daemonruntime)
// and the server (internal/handlers/file_system), which matches them with
// strings.Contains. Change one only together with both sides.
const (
	UploadBeginCommand  = "fs.upload_begin"
	UploadChunkCommand  = "fs.upload_chunk"
	UploadCommitCommand = "fs.upload_commit"
	UploadAbortCommand  = "fs.upload_abort"
	UploadStatusCommand = "fs.upload_status"
)

// The conflict policies, as they cross the wire in "on_conflict".
const (
	UploadConflictFail      = "fail"
	UploadConflictOverwrite = "overwrite"
	UploadConflictRename    = "rename"
)

// UploadPartialDir is where the daemon stages uploads, relative to its home
// directory: ~/.reliant/uploads/.partial/<upload_id>.part, beside a
// <upload_id>.json describing the session.
const UploadPartialDir = ".reliant/uploads/.partial"

// DefaultUploadMaxSize is the cap a daemon applies when the request names
// none (2 GiB, the server's default max_workspace_upload_size).
const DefaultUploadMaxSize int64 = 2 << 30

// Error markers. A daemon error containing one means:
const (
	// UploadExistsMarker: a file already exists at the destination and the
	// policy is fail. It is fileExistsMarker, the same text
	// fs.create_binary_file refuses with, so one classifier reads both.
	UploadExistsMarker = fileExistsMarker
	// UploadNotFoundMarker: no session with that id on this machine (never
	// begun, committed, aborted, swept as stale, or begun for another
	// workspace root).
	UploadNotFoundMarker = "upload session not found"
	// UploadOffsetMarker: a chunk's offset is beyond the bytes received so
	// far (a gap). The text goes on to say how many bytes arrived.
	UploadOffsetMarker = "upload offset mismatch"
	// UploadIncompleteMarker: commit before every byte arrived.
	UploadIncompleteMarker = "upload incomplete"
	// UploadTooLargeMarker: the declared size exceeds the cap, or a chunk
	// would write past the declared size.
	UploadTooLargeMarker = "upload too large"
	// UploadNoSpaceMarker: the machine's disk cannot hold the file.
	UploadNoSpaceMarker = "not enough free disk space"
	// UploadRefusedPathMarker: the destination is outside the workspace root
	// (after resolving symlinks), inside a .git directory, or a directory.
	UploadRefusedPathMarker = "upload destination refused"
)

// UploadBeginRequest is fs.upload_begin's payload.
type UploadBeginRequest struct {
	// Root is the absolute workspace root the destination must stay inside,
	// after symlinks are resolved. Every later command of the session must
	// name the same root.
	Root string `json:"root"`
	// Path is the absolute destination file path.
	Path string `json:"path"`
	// Size is the file's total size in bytes.
	Size int64 `json:"size"`
	// MaxSize caps Size; 0 means DefaultUploadMaxSize.
	MaxSize int64 `json:"max_size,omitempty"`
	// OnConflict is one of the UploadConflict* values; "" means fail.
	OnConflict string `json:"on_conflict,omitempty"`
}

// UploadBeginResponse is fs.upload_begin's reply.
type UploadBeginResponse struct {
	UploadID string `json:"upload_id"`
	// Path is the absolute destination path, as resolved.
	Path string `json:"path"`
}

// UploadChunkRequest is fs.upload_chunk's payload.
type UploadChunkRequest struct {
	UploadID string `json:"upload_id"`
	Root     string `json:"root"`
	Offset   int64  `json:"offset"`
	// Data is base64: the command envelope is JSON.
	Data string `json:"data"`
}

// UploadProgress is fs.upload_chunk's reply.
type UploadProgress struct {
	// BytesReceived counts the bytes that arrived contiguously from offset 0.
	BytesReceived int64 `json:"bytes_received"`
}

// UploadSessionRequest is the payload of fs.upload_commit, fs.upload_abort and
// fs.upload_status.
type UploadSessionRequest struct {
	UploadID string `json:"upload_id"`
	Root     string `json:"root"`
	// OnConflict, on fs.upload_commit only, overrides the session's policy
	// when set.
	OnConflict string `json:"on_conflict,omitempty"`
}

// UploadCommitResponse is fs.upload_commit's reply.
type UploadCommitResponse struct {
	// Path is the absolute path the file now has (with the rename policy, the
	// free name it took).
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Created is false when the upload replaced an existing file.
	Created bool `json:"created"`
}

// UploadStatusResponse is fs.upload_status's reply. An unknown session is
// Exists false, not an error.
type UploadStatusResponse struct {
	Exists        bool  `json:"exists"`
	BytesReceived int64 `json:"bytes_received"`
	Size          int64 `json:"size"`
}

// UploadAbortResponse is fs.upload_abort's reply. Aborting an unknown session
// succeeds: the outcome the caller wanted already holds.
type UploadAbortResponse struct{}
