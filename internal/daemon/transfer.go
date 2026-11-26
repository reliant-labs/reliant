package daemon

import (
	"fmt"
	"os"
)

// Large downloads and folder archives: the daemon commands behind
// FileSystemService's ReadFileRange / PrepareFolderArchive /
// GetFolderArchiveStatus / ReleaseFolderArchive.
//
// A file is downloaded in pieces (fs.read_range), each one ordinary daemon
// command far below the transport's chunked-reply cap, so no hop ever holds
// the whole file. A folder is zipped by the daemon into a temporary archive
// OUTSIDE the workspace (ArchiveDir, under ~/.reliant), which the client then
// reads in pieces by its id (fs.archive_read) and deletes
// (fs.archive_release).
//
// The archive id is the only handle on an archive: the daemon mints it (32
// random hex digits), names the archive's files after it in ArchiveDir, and
// accepts nothing else, so a client can never steer fs.archive_read at a path.
// Every command of an archive also names the workspace root it was prepared
// for, and the daemon refuses an archive prepared for another root.
//
// As with the upload commands (upload.go), these are commands of their own,
// never flags on fs.read_binary_file: a daemon answers a command it does not
// know with the registry's "older version of Reliant" error
// (daemonerr.IsOutdated), which the server reports as an update to make. The
// marker texts below are the contract between the daemon's handlers
// (internal/toolexec/daemonruntime) and the server
// (internal/handlers/file_system), which matches them with strings.Contains.
// Change one only together with both sides.
const (
	ReadRangeCommand      = "fs.read_range"
	ArchivePrepareCommand = "fs.archive_prepare"
	ArchiveStatusCommand  = "fs.archive_status"
	ArchiveReadCommand    = "fs.archive_read"
	ArchiveReleaseCommand = "fs.archive_release"
)

// MaxReadRangeBytes is the most one fs.read_range or fs.archive_read returns:
// 1 MiB, ~1.4 MB of base64 in the reply, which a slow machine link carries
// well inside a command's budget.
const MaxReadRangeBytes = 1 << 20

// ArchiveDir is where the daemon builds folder archives, relative to its home
// directory: ~/.reliant/uploads/.partial/archives/<archive_id>.zip, beside a
// <archive_id>.json describing it. It is beneath the upload staging directory
// (UploadPartialDir), whose sweep leaves subdirectories alone.
const ArchiveDir = UploadPartialDir + "/archives"

// DefaultArchiveMaxBytes is the most file content an archive holds when the
// request names no cap (2 GiB, the server's default max_folder_archive_size).
const DefaultArchiveMaxBytes int64 = 2 << 30

// ArchiveMaxEntries is the most files one archive holds, whatever their size:
// a folder of a million tiny files would take as long to zip as a large one
// and produce an archive nobody can open usefully.
const ArchiveMaxEntries = 50_000

// The states of an archive, as they cross the wire in "state".
const (
	ArchiveStateRunning = "running"
	ArchiveStateReady   = "ready"
	ArchiveStateFailed  = "failed"
)

// Error markers. A daemon error containing one means:
const (
	// FileChangedMarker: an fs.read_range named an if_version the file no
	// longer has (it was rewritten, grew or shrank since an earlier piece).
	FileChangedMarker = "file changed during download"
	// ReadRangeOffsetMarker: the offset is negative or past the end of the
	// file.
	ReadRangeOffsetMarker = "read offset out of range"
	// ArchiveNotFoundMarker: no archive with that id on this machine (never
	// prepared, released, swept as stale, or prepared for another workspace
	// root).
	ArchiveNotFoundMarker = "folder archive not found"
	// ArchiveNotReadyMarker: fs.archive_read before zipping finished.
	ArchiveNotReadyMarker = "folder archive not ready"
	// ArchiveTooLargeMarker: the folder holds more than the cap (bytes or
	// entries).
	ArchiveTooLargeMarker = "folder too large to archive"
	// ArchiveRefusedPathMarker: the folder is outside the workspace root
	// (after resolving symlinks), or is not a folder.
	ArchiveRefusedPathMarker = "folder archive refused"
)

// ReadRangeRequest is fs.read_range's payload.
type ReadRangeRequest struct {
	// Path is the absolute file path.
	Path string `json:"path"`
	// ViewerRoot is the workspace root: the read goes through the file
	// viewer's credential guard (as fs.read_binary_file's viewer_root), and
	// the reply carries viewer_guarded.
	ViewerRoot string `json:"viewer_root"`
	Offset     int64  `json:"offset"`
	// Length is capped at MaxReadRangeBytes; 0 means that much.
	Length int `json:"length"`
	// IfVersion, when set, refuses the read with FileChangedMarker unless the
	// file is still that version.
	IfVersion string `json:"if_version,omitempty"`
}

// ReadRangeResponse is the reply of fs.read_range and fs.archive_read.
type ReadRangeResponse struct {
	// Data is base64: the command envelope is JSON.
	Data      string `json:"data"`
	TotalSize int64  `json:"total_size"`
	// Version is the file's size and modification time, read from the open
	// file the bytes came from (FileVersion).
	Version string `json:"version"`
	EOF     bool   `json:"eof"`
	// Filename, on an fs.archive_read reply only, is the name to save the
	// archive as: the folder's name + ".zip".
	Filename string `json:"filename,omitempty"`
	// ViewerGuarded is set on an fs.read_range reply that passed the viewer's
	// credential guard; the server refuses a reply without it.
	ViewerGuarded bool `json:"viewer_guarded,omitempty"`
}

// FileVersion is a file's identity and version as fs.read_range reports it:
// its size and modification time, from the file the bytes are read from. A
// file rewritten in place, or replaced by another, almost always changes one
// of them, so a download that sees the version change between pieces fails
// rather than stitching two versions together.
func FileVersion(info os.FileInfo) string {
	return fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano())
}

// ArchivePrepareRequest is fs.archive_prepare's payload.
type ArchivePrepareRequest struct {
	// Root is the absolute workspace root: the folder, and every file a
	// symlink in it leads to, must stay inside it after symlinks are
	// resolved. Every later command of the archive must name the same root.
	Root string `json:"root"`
	// Path is the absolute folder to zip.
	Path string `json:"path"`
	// MaxBytes caps the file content the archive holds; 0 means
	// DefaultArchiveMaxBytes.
	MaxBytes int64 `json:"max_bytes,omitempty"`
}

// ArchivePrepareResponse is fs.archive_prepare's reply. Zipping goes on in
// the background after it answers; fs.archive_status follows it.
type ArchivePrepareResponse struct {
	ArchiveID  string `json:"archive_id"`
	TotalFiles int64  `json:"total_files"`
	TotalBytes int64  `json:"total_bytes"`
}

// ArchiveRequest is the payload of fs.archive_status and fs.archive_release.
type ArchiveRequest struct {
	ArchiveID string `json:"archive_id"`
	Root      string `json:"root"`
}

// ArchiveReadRequest is fs.archive_read's payload.
type ArchiveReadRequest struct {
	ArchiveID string `json:"archive_id"`
	Root      string `json:"root"`
	Offset    int64  `json:"offset"`
	// Length is capped at MaxReadRangeBytes; 0 means that much.
	Length int `json:"length"`
}

// ArchiveStatusResponse is fs.archive_status's reply.
type ArchiveStatusResponse struct {
	// State is one of the ArchiveState* values.
	State       string `json:"state"`
	FilesDone   int64  `json:"files_done"`
	FilesTotal  int64  `json:"files_total"`
	BytesDone   int64  `json:"bytes_done"`
	BytesTotal  int64  `json:"bytes_total"`
	ArchiveSize int64  `json:"archive_size,omitempty"`
	Error       string `json:"error,omitempty"`
	Skipped     int64  `json:"skipped"`
}

// ArchiveReleaseResponse is fs.archive_release's reply. Releasing an unknown
// archive succeeds: the outcome the caller wanted already holds.
type ArchiveReleaseResponse struct{}
