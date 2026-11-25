// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// Staged uploads: fs.upload_begin / fs.upload_chunk / fs.upload_commit /
// fs.upload_abort / fs.upload_status. The wire contract (payloads, marker
// texts) is internal/daemon/upload.go; placing the finished file is
// daemon.PlaceUpload.
//
// A session is two files in the staging directory, outside every workspace:
// <id>.part holds the bytes received so far, and <id>.json the session (root,
// destination, size, conflict policy). The bytes received ARE the part file's
// size, so there is no counter to drift from the data. A session survives a
// daemon restart, and one nobody touched for uploadStaleAfter is swept by the
// next begin.

func init() {
	RegisterCommand(daemon.UploadBeginCommand, defaultUploads.handleBegin)
	RegisterCommand(daemon.UploadChunkCommand, defaultUploads.handleChunk)
	RegisterCommand(daemon.UploadCommitCommand, defaultUploads.handleCommit)
	RegisterCommand(daemon.UploadAbortCommand, defaultUploads.handleAbort)
	RegisterCommand(daemon.UploadStatusCommand, defaultUploads.handleStatus)
	RegisterCommand(daemon.UserUploadBeginCommand, defaultUploads.handleUserBegin)
}

// uploadStaleAfter is how long a session may sit untouched before a begin
// sweeps it: an upload the browser abandoned (a closed tab) leaves its part
// file behind, and nothing else would ever remove it.
const uploadStaleAfter = 24 * time.Hour

// defaultUploads stages under the daemon user's home directory
// (daemon.UploadPartialDir). Tests build their own uploadStore on a temporary
// directory instead of changing the home directory.
var defaultUploads = &uploadStore{dir: defaultUploadDir}

func defaultUploadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory for staged uploads: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(daemon.UploadPartialDir)), nil
}

// errUploadNotFound carries daemon.UploadNotFoundMarker.
var errUploadNotFound = errors.New(daemon.UploadNotFoundMarker)

type uploadStore struct {
	// dir is the staging directory.
	dir func() (string, error)
	// userRoot is the uploads folder user uploads go to; nil is
	// daemon.UserUploadsRoot (~/.reliant/uploads).
	userRoot func() (string, error)
	// now is the sweep's clock; nil is time.Now.
	now func() time.Time
	// available reports the free bytes of the filesystem holding a path;
	// nil is daemon.AvailableBytes.
	available func(string) (uint64, bool)

	// Every command on one session runs under that session's lock, so a
	// chunk and a commit (or two re-sent chunks) never interleave.
	mu    sync.Mutex
	locks map[string]*uploadLock
}

type uploadLock struct {
	mu   sync.Mutex
	refs int
}

// uploadSession is <id>.json.
type uploadSession struct {
	Root       string    `json:"root"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	OnConflict string    `json:"on_conflict"`
	CreatedAt  time.Time `json:"created_at"`
	// Destination is daemon.UploadDestinationUserUploads for a session
	// fs.user_upload_begin opened: Path is then in UploadsRoot, the uploads
	// folder resolved at begin, and Root only binds the session to the
	// workspace that scoped it. Empty for a workspace upload, whose Path is
	// inside Root.
	Destination string `json:"destination,omitempty"`
	UploadsRoot string `json:"uploads_root,omitempty"`
}

// userUploads reports whether the session is a user upload.
func (s uploadSession) userUploads() bool {
	return s.Destination == daemon.UploadDestinationUserUploads
}

// lock takes upload id's lock and returns its release.
func (s *uploadStore) lock(id string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*uploadLock{}
	}
	l := s.locks[id]
	if l == nil {
		l = &uploadLock{}
		s.locks[id] = l
	}
	l.refs++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.locks, id)
		}
		s.mu.Unlock()
	}
}

func (s *uploadStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *uploadStore) availableBytes(path string) (uint64, bool) {
	if s.available != nil {
		return s.available(path)
	}
	return daemon.AvailableBytes(path)
}

// validUploadID reports whether id is what newUploadID makes: 32 lowercase
// hex digits. It is joined into a file path, so nothing else is accepted.
func validUploadID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func checkUploadID(id string) error {
	if !validUploadID(id) {
		return fmt.Errorf("%w: invalid upload id", errUploadNotFound)
	}
	return nil
}

func newUploadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func uploadFiles(dir, id string) (part, meta string) {
	return filepath.Join(dir, id+".part"), filepath.Join(dir, id+".json")
}

func normalizeUploadPolicy(policy string) (string, error) {
	switch policy {
	case "", daemon.UploadConflictFail:
		return daemon.UploadConflictFail, nil
	case daemon.UploadConflictOverwrite, daemon.UploadConflictRename:
		return policy, nil
	default:
		return "", fmt.Errorf("unknown on_conflict %q", policy)
	}
}

// load reads session id for root and the bytes it has received. A session
// that does not exist, is half-removed, or was begun for another root is
// errUploadNotFound.
func (s *uploadStore) load(id, root string) (sess uploadSession, part, meta string, received int64, err error) {
	dir, err := s.dir()
	if err != nil {
		return sess, "", "", 0, err
	}
	part, meta = uploadFiles(dir, id)
	notFound := fmt.Errorf("%w: %s", errUploadNotFound, id)
	data, err := os.ReadFile(meta)
	if errors.Is(err, fs.ErrNotExist) {
		return sess, "", "", 0, notFound
	}
	if err != nil {
		return sess, "", "", 0, fmt.Errorf("reading upload session %s: %w", id, err)
	}
	if json.Unmarshal(data, &sess) != nil || root == "" || filepath.Clean(sess.Root) != filepath.Clean(root) {
		return uploadSession{}, "", "", 0, notFound
	}
	info, err := os.Stat(part)
	if errors.Is(err, fs.ErrNotExist) {
		return uploadSession{}, "", "", 0, notFound
	}
	if err != nil {
		return uploadSession{}, "", "", 0, fmt.Errorf("reading upload %s: %w", id, err)
	}
	return sess, part, meta, info.Size(), nil
}

// removeUpload deletes a session: the description first, so a session whose part
// could not be removed is already gone (load needs both), and no chunk can
// write into a part that now shares its inode with a placed file.
func removeUpload(part, meta string) {
	_ = os.Remove(meta)
	_ = os.Remove(part)
}

// =============================================================================
// fs.upload_begin
// =============================================================================

func (s *uploadStore) handleBegin(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.UploadBeginRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Root == "" || req.Path == "" {
		return nil, errors.New("invalid payload: root and path are required")
	}
	policy, err := normalizeUploadPolicy(req.OnConflict)
	if err != nil {
		return nil, err
	}
	if err := checkUploadSize(req.Size, req.MaxSize); err != nil {
		return nil, err
	}

	// Both resolved with allowMissing, as for any write; a no-op for
	// unconfined callers. ValidateUploadDest is the boundary that holds for
	// every caller: inside root once symlinks are resolved, and not .git.
	if _, err := daemonpolicy.ResolveDir(ctx, req.Root); err != nil {
		return nil, err
	}
	path, err := daemonpolicy.ResolveDir(ctx, req.Path)
	if err != nil {
		return nil, err
	}
	dest, err := daemon.ValidateUploadDest(req.Root, path)
	if err != nil {
		return nil, err
	}
	// Refused now, before any byte is sent; commit checks again, since the
	// file can appear while the upload runs.
	if policy == daemon.UploadConflictFail {
		if _, err := os.Lstat(dest); err == nil {
			return nil, fmt.Errorf("%s: %s", daemon.UploadExistsMarker, dest)
		}
	}

	return s.open(uploadSession{Root: filepath.Clean(req.Root), Path: dest, Size: req.Size, OnConflict: policy})
}

// checkUploadSize refuses a negative size, or one over maxSize (0 is
// daemon.DefaultUploadMaxSize).
func checkUploadSize(size, maxSize int64) error {
	if size < 0 {
		return fmt.Errorf("invalid size %d", size)
	}
	if maxSize <= 0 {
		maxSize = daemon.DefaultUploadMaxSize
	}
	if size > maxSize {
		return fmt.Errorf("%s: %d bytes exceeds the limit of %d bytes", daemon.UploadTooLargeMarker, size, maxSize)
	}
	return nil
}

// open stages a new session for sess (its destination already checked): an
// empty part file and the session description, after sweeping stale
// sessions and checking both disks have room.
func (s *uploadStore) open(sess uploadSession) ([]byte, error) {
	dir, err := s.dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the upload staging directory: %w", err)
	}
	s.sweep(dir)
	if err := s.checkSpace(sess.Size, dir, filepath.Dir(sess.Path)); err != nil {
		return nil, err
	}

	id, err := newUploadID()
	if err != nil {
		return nil, err
	}
	part, meta := uploadFiles(dir, id)
	// 0644, not 0600: at commit the part becomes the destination file itself
	// (a hard link shares its mode), so it takes a new file's usual mode.
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("creating upload %s: %w", id, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return nil, fmt.Errorf("creating upload %s: %w", id, err)
	}
	sess.CreatedAt = s.clock().UTC()
	if err := writeUploadSession(dir, meta, sess); err != nil {
		_ = os.Remove(part)
		return nil, fmt.Errorf("recording upload %s: %w", id, err)
	}
	return json.Marshal(daemon.UploadBeginResponse{UploadID: id, Path: sess.Path})
}

// =============================================================================
// fs.user_upload_begin
// =============================================================================

func (s *uploadStore) userUploadsRoot() (string, error) {
	if s.userRoot != nil {
		return s.userRoot()
	}
	return daemon.UserUploadsRoot()
}

// handleUserBegin opens a session whose file goes to the uploads folder
// (daemon/user_uploads.go): the destination is resolved here, from this
// daemon's own home directory, and the request only names the folder and the
// file. A taken name is always kept: the session commits under the rename
// policy.
func (s *uploadStore) handleUserBegin(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.UserUploadBeginRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Root == "" {
		return nil, errors.New("invalid payload: root is required")
	}
	if err := checkUploadSize(req.Size, req.MaxSize); err != nil {
		return nil, err
	}
	// The workspace root binds the session; a confined caller must be
	// allowed it, as for any upload.
	if _, err := daemonpolicy.ResolveDir(ctx, req.Root); err != nil {
		return nil, err
	}
	uploadsRoot, err := s.userUploadsRoot()
	if err != nil {
		return nil, err
	}
	uploadsRoot = filepath.Clean(uploadsRoot)
	// 0700, as the staging folder beside it: what a person uploads is theirs.
	if err := os.MkdirAll(uploadsRoot, 0o700); err != nil {
		return nil, fmt.Errorf("creating the uploads folder: %w", err)
	}
	dest, err := daemon.ResolveUserUploadDest(uploadsRoot, req.Folder, req.Filename)
	if err != nil {
		return nil, err
	}
	// A confined caller (a connector) is refused unless its root covers the
	// uploads folder; a no-op for first-party callers.
	if _, err := daemonpolicy.ResolveDir(ctx, dest); err != nil {
		return nil, err
	}
	return s.open(uploadSession{
		Root:        filepath.Clean(req.Root),
		Path:        dest,
		Size:        req.Size,
		OnConflict:  daemon.UploadConflictRename,
		Destination: daemon.UploadDestinationUserUploads,
		UploadsRoot: uploadsRoot,
	})
}

// writeUploadSession writes meta through a temporary file and a rename, so a
// reader never sees half a session.
func writeUploadSession(dir, meta string, sess uploadSession) error {
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(meta)+".*.tmp")
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), meta); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// checkSpace refuses an upload the disks cannot hold: the part file needs
// size bytes in the staging directory, and the destination's filesystem
// needs as much again only when it is another one (a hard link costs
// nothing), but asking each for size is right in both cases. A filesystem
// whose free space cannot be read is not checked; a write that then runs out
// fails with the same marker (handleChunk).
func (s *uploadStore) checkSpace(size int64, paths ...string) error {
	if size <= 0 {
		return nil
	}
	for _, p := range paths {
		if free, ok := s.availableBytes(p); ok && uint64(size) > free { //nolint:gosec // size > 0 above
			return fmt.Errorf("%s: the upload needs %d bytes and the disk holding %s has %d free",
				daemon.UploadNoSpaceMarker, size, p, free)
		}
	}
	return nil
}

// sweep removes sessions, and stray temporary files, nobody has touched for
// uploadStaleAfter. A session's age is its newer file's mtime: every chunk
// writes the part.
func (s *uploadStore) sweep(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := s.clock()
	ids := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".part"), strings.HasSuffix(name, ".json"):
			// Both suffixes are five bytes long.
			if id := name[:len(name)-len(".part")]; validUploadID(id) {
				ids[id] = true
			}
		case strings.HasSuffix(name, ".tmp"):
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > uploadStaleAfter {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
	for id := range ids {
		s.sweepSession(dir, id, now)
	}
}

func (s *uploadStore) sweepSession(dir, id string, now time.Time) {
	unlock := s.lock(id)
	defer unlock()
	part, meta := uploadFiles(dir, id)
	var newest time.Time
	for _, p := range []string{part, meta} {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if !newest.IsZero() && now.Sub(newest) > uploadStaleAfter {
		removeUpload(part, meta)
	}
}

// =============================================================================
// fs.upload_chunk
// =============================================================================

func (s *uploadStore) handleChunk(_ context.Context, payload []byte) ([]byte, error) {
	var req daemon.UploadChunkRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		// The payload is file content: describe it, never quote it.
		opens, closes := payloadFraming(payload)
		return nil, fmt.Errorf("invalid payload (%d bytes, json_opens=%t, json_closes=%t): %w",
			len(payload), opens, closes, err)
	}
	if err := checkUploadID(req.UploadID); err != nil {
		return nil, err
	}
	if req.Offset < 0 {
		return nil, fmt.Errorf("invalid offset %d", req.Offset)
	}
	// Decoded before anything touches the part file, so a malformed chunk
	// writes nothing.
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return nil, fmt.Errorf("decode base64 chunk of upload %s: %w", req.UploadID, err)
	}

	unlock := s.lock(req.UploadID)
	defer unlock()
	sess, part, _, received, err := s.load(req.UploadID, req.Root)
	if err != nil {
		return nil, err
	}
	if req.Offset > received {
		return nil, fmt.Errorf("%s: %d bytes received, chunk starts at %d", daemon.UploadOffsetMarker, received, req.Offset)
	}
	end := req.Offset + int64(len(data))
	if end > sess.Size {
		return nil, fmt.Errorf("%s: the chunk ends at byte %d, past the declared size of %d bytes", daemon.UploadTooLargeMarker, end, sess.Size)
	}
	if len(data) > 0 {
		f, err := os.OpenFile(part, os.O_WRONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("opening upload %s: %w", req.UploadID, err)
		}
		// WriteAt, so a chunk re-sent after a lost reply rewrites the same
		// bytes and the upload resumes where the part file ends.
		_, writeErr := f.WriteAt(data, req.Offset)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			if errors.Is(err, syscall.ENOSPC) {
				return nil, fmt.Errorf("%s: writing upload %s: %w", daemon.UploadNoSpaceMarker, req.UploadID, err)
			}
			return nil, fmt.Errorf("writing upload %s: %w", req.UploadID, err)
		}
		received = max(received, end)
	}
	return json.Marshal(daemon.UploadProgress{BytesReceived: received})
}

// =============================================================================
// fs.upload_commit
// =============================================================================

func (s *uploadStore) handleCommit(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.UploadSessionRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := checkUploadID(req.UploadID); err != nil {
		return nil, err
	}
	var override string
	if req.OnConflict != "" {
		p, err := normalizeUploadPolicy(req.OnConflict)
		if err != nil {
			return nil, err
		}
		override = p
	}

	unlock := s.lock(req.UploadID)
	defer unlock()
	sess, part, meta, received, err := s.load(req.UploadID, req.Root)
	if err != nil {
		return nil, err
	}
	if received != sess.Size {
		return nil, fmt.Errorf("%s: %d of %d bytes received", daemon.UploadIncompleteMarker, received, sess.Size)
	}
	policy := sess.OnConflict
	if override != "" {
		policy = override
	}
	// The destination's boundary: the workspace root, or for a user upload
	// the uploads folder resolved at begin.
	validate := func(path string) (string, error) { return daemon.ValidateUploadDest(sess.Root, path) }
	dirMode := os.FileMode(0o755)
	if sess.userUploads() {
		// Chat never asks about a clash: a user upload always keeps both.
		policy = daemon.UploadConflictRename
		validate = func(path string) (string, error) { return daemon.ValidateUserUploadDest(sess.UploadsRoot, path) }
		dirMode = 0o700
	}

	// The destination is checked again: the workspace may have changed
	// since begin (a directory replaced by a symlink out of it).
	path, err := daemonpolicy.ResolveDir(ctx, sess.Path)
	if err != nil {
		return nil, err
	}
	dest, err := validate(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), dirMode); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	// And once more over the directories just created.
	if dest, err = validate(dest); err != nil {
		return nil, err
	}
	// A refusal (the file exists under policy fail) keeps the session, so
	// the caller can commit again with another policy without re-sending.
	final, created, err := daemon.PlaceUpload(part, dest, policy)
	if err != nil {
		return nil, err
	}
	removeUpload(part, meta)
	return json.Marshal(daemon.UploadCommitResponse{Path: final, Size: sess.Size, Created: created, Destination: sess.Destination})
}

// =============================================================================
// fs.upload_abort
// =============================================================================

func (s *uploadStore) handleAbort(_ context.Context, payload []byte) ([]byte, error) {
	var req daemon.UploadSessionRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := checkUploadID(req.UploadID); err != nil {
		return nil, err
	}
	unlock := s.lock(req.UploadID)
	defer unlock()
	_, part, meta, _, err := s.load(req.UploadID, req.Root)
	switch {
	case errors.Is(err, errUploadNotFound):
		// Already gone, or another workspace's: nothing of the caller's is
		// left to remove.
	case err != nil:
		return nil, err
	default:
		removeUpload(part, meta)
	}
	return json.Marshal(daemon.UploadAbortResponse{})
}

// =============================================================================
// fs.upload_status
// =============================================================================

func (s *uploadStore) handleStatus(_ context.Context, payload []byte) ([]byte, error) {
	var req daemon.UploadSessionRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := checkUploadID(req.UploadID); err != nil {
		return nil, err
	}
	unlock := s.lock(req.UploadID)
	defer unlock()
	sess, _, _, received, err := s.load(req.UploadID, req.Root)
	switch {
	case errors.Is(err, errUploadNotFound):
		return json.Marshal(daemon.UploadStatusResponse{})
	case err != nil:
		return nil, err
	}
	return json.Marshal(daemon.UploadStatusResponse{Exists: true, BytesReceived: received, Size: sess.Size})
}
