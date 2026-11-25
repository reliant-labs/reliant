package file_system

// Staged uploads: what BeginFileUpload, UploadFileChunk, CommitFileUpload,
// AbortFileUpload and GetFileUploadStatus share.
//
// A file reaches the machine that holds the workspace in pieces: the daemon
// stages them in a part file outside the workspace and moves it into place at
// commit (internal/daemon/upload.go is the command contract). Each rpc is one
// ordinary daemon command, scoped like every other write of this service:
// resolveScope places it, scope.resolve confines its destination, and the
// daemon checks the destination again on its own disk, symlinks resolved.
//
// A daemon older than the staged-upload commands answers fs.upload_begin with
// daemonerr.IsOutdated. A file small enough to cross in one command (up to
// legacyUploadMaxSize) is then written whole by the commands that daemon does
// have: a "legacy." upload id carries the destination, and its one chunk
// writes the file. Anything larger asks the user to update that machine.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const (
	// uploadChunkSize is the piece size BeginFileUpload tells the client to
	// send. 1 MiB is ~1.4 MB of base64 on the server→daemon leg, which a slow
	// machine link still carries well inside the 15s the direct API host's
	// gateway allows a unary request.
	uploadChunkSize = 1 << 20
	// uploadMaxChunkBytes is the largest piece UploadFileChunk accepts, so a
	// later client may send bigger pieces without a server change.
	uploadMaxChunkBytes = 4 << 20
	// legacyUploadMaxSize is the largest file written whole, in one command,
	// to a daemon without the staged-upload commands.
	legacyUploadMaxSize = 20 << 20
	// uploadMaxNameBytes is the longest file name most filesystems allow.
	uploadMaxNameBytes = 255
	// uploadRenameAttempts bounds the "name (n).ext" candidates the rename
	// policy tries, as the daemon's own commit does.
	uploadRenameAttempts = 1000
	// legacyUploadIDPrefix marks an upload id this server minted for a legacy
	// daemon. A daemon's ids are hex, so the two never collide.
	legacyUploadIDPrefix = "legacy."
	// fileSystemReadMaxBytes caps one request to this service (see Register).
	fileSystemReadMaxBytes = 48 << 20
)

// Daemon command budgets. FileSystemRPCDeadlines (deadlines.go) derives each
// upload rpc's request deadline from them, so the daemon's answer (or its
// timeout) arrives before the rpc is cancelled. A chunk carries up to 4 MiB
// to a possibly slow machine, and a commit may copy a 2 GiB part file across
// filesystems before it links it into place.
const (
	uploadBeginTimeoutMs  int32 = 25_000
	uploadChunkTimeoutMs  int32 = 55_000
	uploadCommitTimeoutMs int32 = 115_000
	uploadAbortTimeoutMs  int32 = 25_000
	uploadStatusTimeoutMs int32 = 25_000
	legacyStatTimeoutMs   int32 = 10_000
)

// uploadMaxSize is the largest file this server accepts:
// ApiServerConfig.max_workspace_upload_size, else 2 GiB.
func (s *Service) uploadMaxSize() int64 {
	if s.deps.API == nil {
		return daemon.DefaultUploadMaxSize
	}
	if n := s.deps.API.GetMaxWorkspaceUploadSize(); n > 0 {
		return n
	}
	return daemon.DefaultUploadMaxSize
}

// uploadConflictWire is a conflict policy as it crosses the wire to the
// daemon. UNSPECIFIED is FAIL, as the proto documents.
func uploadConflictWire(p pb.UploadConflictPolicy) (string, error) {
	switch p {
	case pb.UploadConflictPolicy_UPLOAD_CONFLICT_POLICY_UNSPECIFIED, pb.UploadConflictPolicy_UPLOAD_CONFLICT_POLICY_FAIL:
		return daemon.UploadConflictFail, nil
	case pb.UploadConflictPolicy_UPLOAD_CONFLICT_POLICY_OVERWRITE:
		return daemon.UploadConflictOverwrite, nil
	case pb.UploadConflictPolicy_UPLOAD_CONFLICT_POLICY_RENAME:
		return daemon.UploadConflictRename, nil
	}
	return "", svcerr.InvalidArgument(fmt.Sprintf("unknown conflict policy %d", p))
}

// validUploadConflict reports whether s is one of the daemon.UploadConflict*
// values.
func validUploadConflict(s string) bool {
	return s == daemon.UploadConflictFail || s == daemon.UploadConflictOverwrite || s == daemon.UploadConflictRename
}

// validateUploadName refuses a file name that is not a single path element:
// a separator of either convention (the daemon may run Windows), "." or "..",
// a NUL (which truncates the path at the syscall), or a name no filesystem
// takes.
func validateUploadName(name string) error {
	switch {
	case name == "":
		return svcerr.InvalidArgument("filename is required")
	case name == "." || name == "..":
		return svcerr.InvalidArgument(fmt.Sprintf("%q is not a file name", name))
	case strings.ContainsAny(name, "/\\\x00"):
		return svcerr.InvalidArgument("filename must be a plain name, with no path separators")
	case len(name) > uploadMaxNameBytes:
		return svcerr.InvalidArgument(fmt.Sprintf("filename is longer than %d bytes", uploadMaxNameBytes))
	}
	return nil
}

// uploadDest is where an upload lands: the absolute path the daemon acts on,
// and the workspace-relative path (forward slashes) the client is shown.
type uploadDest struct {
	abs string
	rel string
}

// uploadRoot is the workspace root an upload is confined to, as the daemon
// receives it: scope.resolve's absolute, cleaned base. Every command of a
// session names it, and the daemon refuses a session begun for another root.
func (w workspaceScope) uploadRoot() (string, error) {
	return w.resolve("")
}

// resolveUploadDest confines filename in directory to the workspace. The
// directory is resolved like any write path (scope.resolve), and the joined
// destination is resolved again, so neither can leave the workspace; a
// destination inside a .git directory is refused, as the daemon refuses it
// too.
func (w workspaceScope) resolveUploadDest(directory, filename string) (uploadDest, error) {
	if err := validateUploadName(filename); err != nil {
		return uploadDest{}, err
	}
	dir, err := w.resolve(directory)
	if err != nil {
		return uploadDest{}, err
	}
	abs, err := w.resolve(ospath.Join(dir, filename))
	if err != nil {
		return uploadDest{}, err
	}
	root, err := w.uploadRoot()
	if err != nil {
		return uploadDest{}, err
	}
	rel, ok := workspaceRelative(root, abs)
	if !ok || rel == "" {
		return uploadDest{}, svcerr.PermissionDenied(daemon.UploadRefusedPathMarker + ": the destination is outside the workspace")
	}
	for _, segment := range strings.Split(rel, "/") {
		// EqualFold: on a case-insensitive disk (macOS, Windows) .GIT is .git.
		if strings.EqualFold(segment, ".git") {
			return uploadDest{}, svcerr.PermissionDenied(daemon.UploadRefusedPathMarker + ": files cannot be uploaded into a .git directory")
		}
	}
	return uploadDest{abs: abs, rel: rel}, nil
}

// workspaceRelative is abs relative to root, with forward slashes, and
// whether abs is root or beneath it. Both are absolute paths on the daemon,
// in its convention (ospath): only a Windows path has its backslashes turned,
// since a POSIX name may contain one.
func workspaceRelative(root, abs string) (string, bool) {
	root, abs = ospath.Clean(root), ospath.Clean(abs)
	if abs == root {
		return "", true
	}
	if !strings.HasPrefix(abs, root) {
		return "", false
	}
	rest := abs[len(root):]
	windows := !strings.HasPrefix(root, "/")
	switch {
	case strings.HasSuffix(root, "/") || (windows && strings.HasSuffix(root, `\`)):
		// root is a filesystem root ("/", `C:\`): rest is already relative.
	case rest[0] == '/' || (windows && rest[0] == '\\'):
		rest = rest[1:]
	default:
		// "/work/app-secrets" is not beneath "/work/app".
		return "", false
	}
	if windows {
		rest = strings.ReplaceAll(rest, `\`, "/")
	}
	return rest, rest != ""
}

// uploadRelativePath is the workspace-relative path of a file the daemon
// reports placing at abs, which is beneath root as sent. A daemon that
// reported another spelling of it (root with its symlinks resolved, say) has
// still placed the file in the folder the client chose, so the client is
// given dest's folder with the name the daemon chose rather than an error for
// an upload that succeeded.
func uploadRelativePath(root, abs string) string {
	if rel, ok := workspaceRelative(root, abs); ok {
		return rel
	}
	logging.Warn("Upload committed outside the root it was sent", "root", root, "path", abs)
	return ospath.Base(abs)
}

// validateUploadID refuses an id the daemon cannot have minted. The daemon
// names its part files after the id, so this keeps a path out of it here as
// well as there.
func validateUploadID(id string) error {
	if id == "" {
		return svcerr.InvalidArgument("upload_id is required")
	}
	if len(id) > 128 {
		return svcerr.InvalidArgument("malformed upload_id")
	}
	for _, c := range id {
		if !uploadIDChar(c) {
			return svcerr.InvalidArgument("malformed upload_id")
		}
	}
	return nil
}

// uploadIDChar reports whether c may appear in a daemon's upload id.
func uploadIDChar(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		return true
	}
	return false
}

// uploadDaemonError maps a failed upload command by the daemon's own words
// (the markers in internal/daemon/upload.go) to the code that says what
// happened; anything else stays sendCommand's mapping. Only text the daemon
// reported is matched, never the transport's.
func uploadDaemonError(err error) error {
	text, ok := toolexec.DaemonReportedText(err)
	if !ok {
		return err
	}
	switch {
	case strings.Contains(text, daemon.UploadExistsMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrAlreadyExists, text), err)
	case strings.Contains(text, daemon.UploadNotFoundMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, text), err)
	case strings.Contains(text, daemon.UploadOffsetMarker),
		strings.Contains(text, daemon.UploadIncompleteMarker),
		strings.Contains(text, daemon.UploadTooLargeMarker),
		strings.Contains(text, daemon.UploadNoSpaceMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrFailedPrecondition, text), err)
	case strings.Contains(text, daemon.UploadRefusedPathMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrPermissionDenied, text), err)
	}
	return err
}

// sendUploadCommand is sendCommand with the upload markers mapped.
func (s *Service) sendUploadCommand(ctx context.Context, userID string, target machinewake.Target, commandType string, req, resp any, timeoutMs int32) error {
	if err := s.sendCommand(ctx, userID, target, commandType, req, resp, timeoutMs); err != nil {
		return uploadDaemonError(err)
	}
	return nil
}

// formatUploadBytes renders a byte count for a refusal a person reads.
func formatUploadBytes(n int64) string {
	const (
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n >= gib && n%gib == 0:
		return fmt.Sprintf("%d GB", n/gib)
	case n >= gib:
		return fmt.Sprintf("%.1f GB", float64(n)/gib)
	case n >= mib && n%mib == 0:
		return fmt.Sprintf("%d MB", n/mib)
	case n >= mib:
		return fmt.Sprintf("%.1f MB", float64(n)/mib)
	}
	return fmt.Sprintf("%d bytes", n)
}

// uploadRenameCandidate is the n-th name the rename policy tries: the name
// itself, then "report (1).xlsx", "report (2).xlsx", .... A dotfile's leading
// dot is part of its name, not an extension (".env (1)").
func uploadRenameCandidate(name string, n int) string {
	if n == 0 {
		return name
	}
	stem, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		stem, ext = name[:i], name[i:]
	}
	return fmt.Sprintf("%s (%d)%s", stem, n, ext)
}

// ---------------------------------------------------------------------------
// The whole-file fallback for daemons without the staged-upload commands.
// ---------------------------------------------------------------------------

// legacyUpload is what a legacy upload id carries. It is not a capability:
// every rpc re-resolves it through the caller's scope, so it names nothing
// the caller could not name in a BeginFileUpload of their own.
type legacyUpload struct {
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Policy string `json:"policy"`
}

func encodeLegacyUploadID(u legacyUpload) string {
	raw, _ := json.Marshal(u) // a struct of strings and an int cannot fail
	return legacyUploadIDPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// decodeLegacyUploadID reports whether id is a legacy upload id and, if so,
// what it carries. A malformed one is InvalidArgument.
func decodeLegacyUploadID(id string) (legacyUpload, bool, error) {
	encoded, ok := strings.CutPrefix(id, legacyUploadIDPrefix)
	if !ok {
		return legacyUpload{}, false, nil
	}
	malformed := svcerr.InvalidArgument("malformed upload_id")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return legacyUpload{}, true, malformed
	}
	var u legacyUpload
	if err := json.Unmarshal(raw, &u); err != nil {
		return legacyUpload{}, true, malformed
	}
	if u.Size < 0 || u.Size > legacyUploadMaxSize || !validUploadConflict(u.Policy) {
		return legacyUpload{}, true, malformed
	}
	if err := validateUploadName(u.Name); err != nil {
		return legacyUpload{}, true, err
	}
	return u, true, nil
}

// machineName is how a refusal names the machine target is: the name its
// owner gave it, else its hostname, else "this machine".
func (s *Service) machineName(ctx context.Context, target machinewake.Target) string {
	if target.DaemonID != "" {
		if d, err := s.deps.Placement.GetDaemon(ctx, target.DaemonID); err == nil && d != nil {
			if d.Name != "" {
				return d.Name
			}
			if d.Hostname != nil && *d.Hostname != "" {
				return *d.Hostname
			}
		}
	}
	return "this machine"
}

// legacyStat is the part of fs.stat's reply a legacy upload reads.
type legacyStat struct {
	Exists bool `json:"exists"`
	IsDir  bool `json:"is_dir"`
}

func (s *Service) statUploadDest(ctx context.Context, userID string, scope workspaceScope, abs string) (legacyStat, error) {
	var st legacyStat
	err := s.sendCommand(ctx, userID, scope.target, "fs.stat", map[string]any{"path": abs}, &st, legacyStatTimeoutMs)
	return st, err
}

// beginLegacyUpload is BeginFileUpload on a daemon without fs.upload_begin:
// nothing is staged, and the id carries the destination for the one chunk
// that writes the file. A destination that exists is refused here, as the
// staged begin refuses it, so the client asks the user before sending bytes.
func (s *Service) beginLegacyUpload(ctx context.Context, userID string, scope workspaceScope, directory string, dest uploadDest, filename string, size int64, policy string, maxSize int64) (*pb.BeginFileUploadResponse, error) {
	if size > legacyUploadMaxSize {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("Update Reliant on %s to upload files larger than %s",
			s.machineName(ctx, scope.target), formatUploadBytes(legacyUploadMaxSize)))
	}
	st, err := s.statUploadDest(ctx, userID, scope, dest.abs)
	if err != nil {
		return nil, err
	}
	if st.IsDir && policy != daemon.UploadConflictRename {
		return nil, svcerr.PermissionDenied(daemon.UploadRefusedPathMarker + ": a folder named " + filename + " is already there")
	}
	if st.Exists && policy == daemon.UploadConflictFail {
		return nil, svcerr.AlreadyExists(daemon.UploadExistsMarker + ": " + dest.rel)
	}
	return &pb.BeginFileUploadResponse{
		UploadId:    encodeLegacyUploadID(legacyUpload{Dir: directory, Name: filename, Size: size, Policy: policy}),
		ChunkSize:   int32(max(size, 1)),
		Path:        dest.rel,
		MaxSize:     maxSize,
		Destination: pb.UploadDestination_UPLOAD_DESTINATION_WORKSPACE,
	}, nil
}

// legacyWriteResponse is the part of fs.write_binary_file's (and
// fs.create_binary_file's) reply a legacy upload reads.
type legacyWriteResponse struct {
	Created bool `json:"created"`
}

// uploadLegacyChunk is UploadFileChunk for a legacy id: the one chunk is the
// whole file, written by the commands an older daemon has. fail creates it
// only where nothing is (fs.create_binary_file), overwrite replaces it
// (fs.write_binary_file), and rename creates it under the first free
// "name (n).ext".
func (s *Service) uploadLegacyChunk(ctx context.Context, userID string, scope workspaceScope, u legacyUpload, offset int64, data []byte) (*pb.UploadFileChunkResponse, error) {
	if offset != 0 {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("%s: this machine takes the file in one piece, from offset 0: 0 bytes received", daemon.UploadOffsetMarker))
	}
	if int64(len(data)) != u.Size {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("this machine takes the whole file (%d bytes) in one chunk; got %d bytes", u.Size, len(data)))
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	write := func(command string, dest uploadDest) error {
		var resp legacyWriteResponse
		return s.sendUploadCommand(ctx, userID, scope.target, command, map[string]any{"path": dest.abs, "data": encoded}, &resp, uploadChunkTimeoutMs)
	}

	var placed uploadDest
	switch u.Policy {
	case daemon.UploadConflictOverwrite, daemon.UploadConflictFail:
		dest, err := scope.resolveUploadDest(u.Dir, u.Name)
		if err != nil {
			return nil, err
		}
		command := daemon.CreateBinaryFileCommand
		if u.Policy == daemon.UploadConflictOverwrite {
			command = "fs.write_binary_file"
		}
		if err := write(command, dest); err != nil {
			return nil, err
		}
		placed = dest
	case daemon.UploadConflictRename:
		for n := 0; ; n++ {
			if n > uploadRenameAttempts {
				return nil, svcerr.AlreadyExists(fmt.Sprintf("%s: no free name for %s after %d tries", daemon.UploadExistsMarker, u.Name, uploadRenameAttempts))
			}
			dest, err := scope.resolveUploadDest(u.Dir, uploadRenameCandidate(u.Name, n))
			if err != nil {
				return nil, err
			}
			// A stat first, so a taken name costs a round trip rather than
			// another copy of the file.
			st, err := s.statUploadDest(ctx, userID, scope, dest.abs)
			if err != nil {
				return nil, err
			}
			if st.Exists {
				continue
			}
			err = write(daemon.CreateBinaryFileCommand, dest)
			if svcerr.Code(err) == connect.CodeAlreadyExists {
				continue // taken since the stat
			}
			if err != nil {
				return nil, err
			}
			placed = dest
			break
		}
	}
	return &pb.UploadFileChunkResponse{BytesReceived: u.Size, CommittedPath: &placed.rel}, nil
}

// legacyUploadCommitted is CommitFileUpload's refusal for a legacy id: its
// chunk already placed the file.
func legacyUploadCommitted() error {
	return svcerr.FailedPrecondition("this upload was written whole by its chunk; there is nothing to commit")
}
