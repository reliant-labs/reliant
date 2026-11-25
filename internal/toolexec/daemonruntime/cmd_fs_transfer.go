// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"archive/zip"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// Large downloads and folder archives: fs.read_range, and fs.archive_prepare
// / fs.archive_status / fs.archive_read / fs.archive_release. The wire
// contract (payloads, marker texts) is internal/daemon/transfer.go.
//
// fs.read_range reads one piece of a file through the file viewer's
// credential guard (openForRead), exactly as fs.read_binary_file reads a
// whole one.
//
// A folder archive is two files in the archive directory, outside every
// workspace: <id>.zip, and <id>.json describing it (the workspace root it was
// prepared for, its state). fs.archive_prepare walks the folder up front, so
// it can refuse one over the cap before writing anything, and zips it in the
// background; fs.archive_status follows that. The id is the only way to name
// an archive: fs.archive_read opens nothing but <id>.zip in the archive
// directory, for an id this daemon minted, so no client can make it read any
// other path. An archive nobody touched for archiveStaleAfter is swept by the
// next prepare.

func init() {
	RegisterCommand(daemon.ReadRangeCommand, handleFSReadRange)
	RegisterCommand(daemon.ArchivePrepareCommand, defaultArchives.handlePrepare)
	RegisterCommand(daemon.ArchiveStatusCommand, defaultArchives.handleStatus)
	RegisterCommand(daemon.ArchiveReadCommand, defaultArchives.handleRead)
	RegisterCommand(daemon.ArchiveReleaseCommand, defaultArchives.handleRelease)
}

// =============================================================================
// fs.read_range
// =============================================================================

func handleFSReadRange(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.ReadRangeRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	// The viewer root is required, not optional as on fs.read_binary_file:
	// this command exists only for the Files tab's downloads, which are
	// always guarded.
	if req.Path == "" || req.ViewerRoot == "" {
		return nil, errors.New("invalid payload: path and viewer_root are required")
	}
	// Re-resolved immediately before use, as fs.read_binary_file does.
	resolved, err := daemonpolicy.ResolveFile(ctx, req.Path)
	if err != nil {
		return nil, err
	}
	f, err := openForRead(resolved, req.ViewerRoot)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	resp, err := readRange(f, resolved, req.Offset, req.Length, req.IfVersion)
	if err != nil {
		return nil, err
	}
	resp.ViewerGuarded = true
	return json.Marshal(resp)
}

// readRange reads up to length bytes of the open file f (named name in
// errors) from offset. The version is the open file's, read before and after
// the bytes, so a file that changes while a piece is read fails like one that
// changed between pieces.
func readRange(f *os.File, name string, offset int64, length int, ifVersion string) (daemon.ReadRangeResponse, error) {
	info, err := f.Stat()
	if err != nil {
		return daemon.ReadRangeResponse{}, fmt.Errorf("stat %s: %w", name, err)
	}
	if info.IsDir() {
		return daemon.ReadRangeResponse{}, fmt.Errorf("%s is a directory", name)
	}
	version := daemon.FileVersion(info)
	if ifVersion != "" && ifVersion != version {
		return daemon.ReadRangeResponse{}, fmt.Errorf("%s: %s", daemon.FileChangedMarker, name)
	}
	size := info.Size()
	if offset < 0 || offset > size {
		return daemon.ReadRangeResponse{}, fmt.Errorf("%s: offset %d, and %s is %d bytes", daemon.ReadRangeOffsetMarker, offset, name, size)
	}
	if length <= 0 || length > daemon.MaxReadRangeBytes {
		length = daemon.MaxReadRangeBytes
	}
	want := min(int64(length), size-offset)
	buf := make([]byte, want)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return daemon.ReadRangeResponse{}, fmt.Errorf("read %s: %w", name, err)
	}
	if after, err := f.Stat(); err != nil || daemon.FileVersion(after) != version || int64(n) != want {
		return daemon.ReadRangeResponse{}, fmt.Errorf("%s: %s", daemon.FileChangedMarker, name)
	}
	return daemon.ReadRangeResponse{
		Data:      base64.StdEncoding.EncodeToString(buf[:n]),
		TotalSize: size,
		Version:   version,
		EOF:       offset+int64(n) >= size,
	}, nil
}

// =============================================================================
// Folder archives
// =============================================================================

// archiveStaleAfter is how long an archive may sit untouched before a
// prepare sweeps it: one the browser abandoned (a closed tab) is never
// released, and it may hold gigabytes. Every read touches it, so a slow
// download in progress is not swept.
const archiveStaleAfter = 6 * time.Hour

// archiveMaxRunning bounds the archives one machine zips at once: each one
// reads a whole folder and writes as much again.
const archiveMaxRunning = 4

// archiveReleaseWait bounds how long a release waits for a zip in progress
// to notice it was cancelled before deleting its files.
const archiveReleaseWait = 10 * time.Second

// defaultArchives builds under the daemon user's home directory
// (daemon.ArchiveDir). Tests build their own archiveStore on a temporary
// directory instead of changing the home directory.
var defaultArchives = &archiveStore{dir: defaultArchiveDir}

func defaultArchiveDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory for folder archives: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(daemon.ArchiveDir)), nil
}

// errArchiveNotFound carries daemon.ArchiveNotFoundMarker.
var errArchiveNotFound = errors.New(daemon.ArchiveNotFoundMarker)

type archiveStore struct {
	// dir is the archive directory.
	dir func() (string, error)
	// now is the sweep's clock; nil is time.Now.
	now func() time.Time
	// available reports the free bytes of the filesystem holding a path;
	// nil is daemon.AvailableBytes.
	available func(string) (uint64, bool)
	// beforeFile, when set, runs before each file is zipped. Tests use it
	// to hold a zip in progress.
	beforeFile func()

	// mu guards jobs, and the metadata write that ends a job, so a status
	// or release never sees a job gone from memory and not yet on disk.
	mu   sync.Mutex
	jobs map[string]*archiveJob
}

// archiveJob is an archive being zipped.
type archiveJob struct {
	root       string
	cancel     context.CancelFunc
	done       chan struct{}
	released   bool // guarded by archiveStore.mu
	filesTotal int64
	bytesTotal int64
	filesDone  atomic.Int64
	bytesDone  atomic.Int64
	skipped    atomic.Int64
}

// archiveMeta is <id>.json.
type archiveMeta struct {
	Root       string    `json:"root"`
	Filename   string    `json:"filename"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	FilesTotal int64     `json:"files_total"`
	BytesTotal int64     `json:"bytes_total"`
	Skipped    int64     `json:"skipped"`
	CreatedAt  time.Time `json:"created_at"`
}

// archiveEntry is one entry of the archive.
type archiveEntry struct {
	// name is the entry's name in the zip: slash-separated, beneath the
	// folder's own name, with a trailing slash for a folder.
	name string
	// path is the file to read (a symlink's own path: opening follows it).
	path string
	dir  bool
	size int64
}

func (s *archiveStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *archiveStore) availableBytes(path string) (uint64, bool) {
	if s.available != nil {
		return s.available(path)
	}
	return daemon.AvailableBytes(path)
}

func archiveFiles(dir, id string) (zipPath, meta string) {
	return filepath.Join(dir, id+".zip"), filepath.Join(dir, id+".json")
}

func checkArchiveID(id string) error {
	if !validUploadID(id) {
		return fmt.Errorf("%w: invalid archive id", errArchiveNotFound)
	}
	return nil
}

func refusedArchive(format string, args ...any) error {
	return fmt.Errorf("%s: %s", daemon.ArchiveRefusedPathMarker, fmt.Sprintf(format, args...))
}

// archiveRelWithin is path relative to root when it is root or beneath it.
func archiveRelWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// isGitSegment reports whether a path element names a .git directory (or
// file, in a worktree); EqualFold, since on a case-insensitive disk .GIT is
// .git.
func isGitSegment(name string) bool { return strings.EqualFold(name, ".git") }

func archiveHasGitSegment(rel string) bool {
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if isGitSegment(seg) {
			return true
		}
	}
	return false
}

// writeJSONFile writes v to path through a temporary file and a rename, so a
// reader never sees half of it.
func writeJSONFile(dir, path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// loadMeta reads an archive's description, refusing one prepared for another
// workspace root as if it did not exist.
func (s *archiveStore) loadMeta(id, root string) (archiveMeta, string, string, error) {
	dir, err := s.dir()
	if err != nil {
		return archiveMeta{}, "", "", err
	}
	zipPath, metaPath := archiveFiles(dir, id)
	data, err := os.ReadFile(metaPath)
	if errors.Is(err, fs.ErrNotExist) {
		return archiveMeta{}, "", "", errArchiveNotFound
	}
	if err != nil {
		return archiveMeta{}, "", "", fmt.Errorf("reading folder archive %s: %w", id, err)
	}
	var m archiveMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return archiveMeta{}, "", "", fmt.Errorf("reading folder archive %s: %w", id, err)
	}
	if m.Root != filepath.Clean(root) {
		return archiveMeta{}, "", "", errArchiveNotFound
	}
	return m, zipPath, metaPath, nil
}

// =============================================================================
// fs.archive_prepare
// =============================================================================

func (s *archiveStore) handlePrepare(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.ArchivePrepareRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Root == "" || req.Path == "" {
		return nil, errors.New("invalid payload: root and path are required")
	}
	if !filepath.IsAbs(req.Root) || !filepath.IsAbs(req.Path) {
		return nil, refusedArchive("the workspace root and the folder must be absolute paths")
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = daemon.DefaultArchiveMaxBytes
	}

	// A no-op for unconfined callers; for confined ones, the folder must be
	// inside their root.
	if _, err := daemonpolicy.ResolveDir(ctx, req.Root); err != nil {
		return nil, err
	}
	folder, err := daemonpolicy.ResolveDir(ctx, req.Path)
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(req.Root))
	if err != nil {
		return nil, refusedArchive("the workspace root %s cannot be resolved: %v", req.Root, err)
	}
	realDir, err := filepath.EvalSymlinks(filepath.Clean(folder))
	if err != nil {
		// A missing folder keeps the os error's words, which the server
		// reports as not found.
		return nil, fmt.Errorf("folder %s: %w", folder, err)
	}
	rel, ok := archiveRelWithin(realRoot, realDir)
	if !ok {
		return nil, refusedArchive("%s is outside the workspace", folder)
	}
	if archiveHasGitSegment(rel) {
		return nil, refusedArchive("%s is inside a .git directory", folder)
	}
	if info, err := os.Stat(realDir); err != nil || !info.IsDir() {
		return nil, refusedArchive("%s is not a folder", folder)
	}
	if err := daemonpolicy.DenyViewerRead(realDir, realRoot); err != nil {
		return nil, err
	}

	dir, err := s.dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the folder archive directory: %w", err)
	}
	s.sweep(dir)
	if s.running() >= archiveMaxRunning {
		return nil, fmt.Errorf("%d folders are already being zipped on this machine; try again when one finishes", archiveMaxRunning)
	}

	top := filepath.Base(realDir)
	if top == "" || top == "." || top == string(filepath.Separator) {
		top = "workspace"
	}
	entries, totalBytes, skipped, err := scanArchive(ctx, realRoot, realDir, top, maxBytes, dir)
	if err != nil {
		return nil, err
	}
	// The archive is at most the content it stores, plus headers.
	if free, ok := s.availableBytes(dir); ok && uint64(totalBytes) > free { //nolint:gosec // totalBytes >= 0
		return nil, fmt.Errorf("%s: the archive needs up to %d bytes and the disk holding %s has %d free",
			daemon.UploadNoSpaceMarker, totalBytes, dir, free)
	}

	id, err := newUploadID()
	if err != nil {
		return nil, err
	}
	var files int64
	for _, e := range entries {
		if !e.dir {
			files++
		}
	}
	_, metaPath := archiveFiles(dir, id)
	meta := archiveMeta{
		Root:       filepath.Clean(req.Root),
		Filename:   top + ".zip",
		State:      daemon.ArchiveStateRunning,
		FilesTotal: files,
		BytesTotal: totalBytes,
		Skipped:    skipped,
		CreatedAt:  s.clock().UTC(),
	}
	if err := writeJSONFile(dir, metaPath, meta); err != nil {
		return nil, fmt.Errorf("recording folder archive %s: %w", id, err)
	}

	jobCtx, cancel := context.WithCancel(context.Background())
	job := &archiveJob{root: meta.Root, cancel: cancel, done: make(chan struct{}), filesTotal: files, bytesTotal: totalBytes}
	job.skipped.Store(skipped)
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = map[string]*archiveJob{}
	}
	s.jobs[id] = job
	s.mu.Unlock()
	go s.build(jobCtx, job, id, dir, meta, entries, realRoot, maxBytes)

	return json.Marshal(daemon.ArchivePrepareResponse{ArchiveID: id, TotalFiles: files, TotalBytes: totalBytes})
}

// running counts the archives being zipped.
func (s *archiveStore) running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// scanArchive lists what the archive of realDir holds, under the entry name
// top. It skips .git, the archive directory itself (a workspace rooted at the
// home directory holds it), folders that are credential stores, symlinks that
// lead outside the workspace, into .git or to a folder (so a link cannot loop
// the walk), and anything neither a file nor a folder. A folder holding more
// than maxBytes of file content, or more than daemon.ArchiveMaxEntries
// entries, is refused before anything is written.
func scanArchive(ctx context.Context, realRoot, realDir, top string, maxBytes int64, archiveDir string) ([]archiveEntry, int64, int64, error) {
	// The staging directory the archive directory sits in holds partial
	// uploads; neither belongs in a download.
	excluded := map[string]bool{}
	for _, p := range []string{archiveDir, filepath.Dir(archiveDir)} {
		excluded[filepath.Clean(p)] = true
		if real, err := filepath.EvalSymlinks(p); err == nil {
			excluded[real] = true
		}
	}

	var (
		entries []archiveEntry
		total   int64
		skipped int64
	)
	add := func(e archiveEntry) error {
		entries = append(entries, e)
		total += e.size
		if len(entries) > daemon.ArchiveMaxEntries {
			return fmt.Errorf("%s: it holds more than %d files and folders", daemon.ArchiveTooLargeMarker, daemon.ArchiveMaxEntries)
		}
		if total > maxBytes {
			return fmt.Errorf("%s: it holds more than %d bytes (the limit is %d bytes)", daemon.ArchiveTooLargeMarker, total, maxBytes)
		}
		return nil
	}
	err := filepath.WalkDir(realDir, func(p string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if p == realDir {
				return walkErr
			}
			skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(realDir, p)
		if err != nil {
			skipped++
			return nil
		}
		name := top
		if rel != "." {
			name = top + "/" + filepath.ToSlash(rel)
		}
		if p != realDir && isGitSegment(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch t := d.Type(); {
		case t.IsDir():
			if excluded[filepath.Clean(p)] {
				return fs.SkipDir
			}
			if p != realDir && daemonpolicy.DenyViewerRead(p, realRoot) != nil {
				skipped++
				return fs.SkipDir
			}
			return add(archiveEntry{name: name + "/", dir: true})
		case t&fs.ModeSymlink != 0:
			target, err := filepath.EvalSymlinks(p)
			if err != nil {
				skipped++
				return nil
			}
			targetRel, ok := archiveRelWithin(realRoot, target)
			if !ok || archiveHasGitSegment(targetRel) {
				skipped++
				return nil
			}
			info, err := os.Stat(target)
			if err != nil || !info.Mode().IsRegular() {
				skipped++
				return nil
			}
			return add(archiveEntry{name: name, path: p, size: info.Size()})
		case t.IsRegular():
			info, err := d.Info()
			if err != nil {
				skipped++
				return nil
			}
			return add(archiveEntry{name: name, path: p, size: info.Size()})
		default:
			skipped++
			return nil
		}
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return entries, total, skipped, nil
}

// build zips entries into the archive, then records how it ended. A released
// archive's files are deleted instead.
func (s *archiveStore) build(ctx context.Context, job *archiveJob, id, dir string, meta archiveMeta, entries []archiveEntry, realRoot string, maxBytes int64) {
	defer close(job.done)
	defer job.cancel()
	zipPath, metaPath := archiveFiles(dir, id)
	err := s.writeArchive(ctx, job, zipPath, entries, realRoot, maxBytes)

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	if job.released {
		_ = os.Remove(metaPath)
		_ = os.Remove(zipPath)
		return
	}
	meta.Skipped = job.skipped.Load()
	if err != nil {
		_ = os.Remove(zipPath)
		meta.State = daemon.ArchiveStateFailed
		meta.Error = err.Error()
	} else {
		meta.State = daemon.ArchiveStateReady
	}
	if werr := writeJSONFile(dir, metaPath, meta); werr != nil {
		// Without its description the archive cannot be read or found
		// again: remove both rather than leave an orphan the sweep must find.
		_ = os.Remove(zipPath)
		_ = os.Remove(metaPath)
	}
}

// errSkipEntry is a file left out of the archive: it could not be opened, it
// is a credential store, or it changed into something outside the workspace
// since the scan.
var errSkipEntry = errors.New("skipped")

func (s *archiveStore) writeArchive(ctx context.Context, job *archiveJob, zipPath string, entries []archiveEntry, realRoot string, maxBytes int64) (err error) {
	f, err := os.OpenFile(zipPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating the archive: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("writing the archive: %w", cerr)
		}
	}()
	zw := zip.NewWriter(f)
	// BestSpeed: the archive is a transport, and zipping time is time the
	// user waits; most of what people download is already compressed.
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestSpeed)
	})

	var written int64
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.dir {
			if _, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store, Modified: s.clock()}); err != nil {
				return fmt.Errorf("writing the archive: %w", err)
			}
			continue
		}
		if s.beforeFile != nil {
			s.beforeFile()
		}
		n, err := addArchiveFile(ctx, zw, e, realRoot, maxBytes-written)
		if errors.Is(err, errSkipEntry) {
			job.skipped.Add(1)
			continue
		}
		if err != nil {
			return err
		}
		written += n
		job.bytesDone.Store(written)
		job.filesDone.Add(1)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("writing the archive: %w", err)
	}
	return nil
}

// addArchiveFile zips one file. It is opened through the viewer's credential
// guard, and must still resolve inside the workspace (and outside .git) to
// the very file opened, so a path swapped for a symlink since the scan cannot
// put a file from elsewhere in the archive. budget is the file content the
// archive may still take.
func addArchiveFile(ctx context.Context, zw *zip.Writer, e archiveEntry, realRoot string, budget int64) (int64, error) {
	f, err := openForRead(e.path, realRoot)
	if err != nil {
		return 0, errSkipEntry
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, errSkipEntry
	}
	real, err := filepath.EvalSymlinks(e.path)
	if err != nil {
		return 0, errSkipEntry
	}
	rel, ok := archiveRelWithin(realRoot, real)
	if !ok || archiveHasGitSegment(rel) || daemonpolicy.DenyCredentialFile(real) != nil {
		return 0, errSkipEntry
	}
	if ri, err := os.Stat(real); err != nil || !os.SameFile(info, ri) {
		return 0, errSkipEntry
	}

	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return 0, errSkipEntry
	}
	hdr.Name = e.name
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return 0, fmt.Errorf("writing the archive: %w", err)
	}
	n, err := io.Copy(w, io.LimitReader(ctxReader{ctx: ctx, r: f}, budget+1))
	if err != nil {
		return n, fmt.Errorf("zipping %s: %w", e.name, err)
	}
	if n > budget {
		return n, fmt.Errorf("%s: its files grew past the limit while it was zipped", daemon.ArchiveTooLargeMarker)
	}
	return n, nil
}

// ctxReader stops a copy once ctx is done, so a released archive stops
// zipping a large file at once.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// sweep removes archives, and stray temporary files, nobody has touched for
// archiveStaleAfter. One being zipped is never removed.
func (s *archiveStore) sweep(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := s.clock()
	ids := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".zip"):
			if id := strings.TrimSuffix(name, ".zip"); validUploadID(id) {
				ids[id] = true
			}
		case strings.HasSuffix(name, ".json"):
			if id := strings.TrimSuffix(name, ".json"); validUploadID(id) {
				ids[id] = true
			}
		case strings.HasSuffix(name, ".tmp"):
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > archiveStaleAfter {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range ids {
		if s.jobs[id] != nil {
			continue
		}
		zipPath, metaPath := archiveFiles(dir, id)
		var newest time.Time
		for _, p := range []string{zipPath, metaPath} {
			if info, err := os.Stat(p); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		if !newest.IsZero() && now.Sub(newest) > archiveStaleAfter {
			_ = os.Remove(metaPath)
			_ = os.Remove(zipPath)
		}
	}
}

// =============================================================================
// fs.archive_status
// =============================================================================

func (s *archiveStore) handleStatus(_ context.Context, payload []byte) ([]byte, error) {
	var req daemon.ArchiveRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := checkArchiveID(req.ArchiveID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	job := s.jobs[req.ArchiveID]
	if job != nil && job.root == filepath.Clean(req.Root) {
		resp := daemon.ArchiveStatusResponse{
			State:      daemon.ArchiveStateRunning,
			FilesDone:  job.filesDone.Load(),
			FilesTotal: job.filesTotal,
			BytesDone:  job.bytesDone.Load(),
			BytesTotal: job.bytesTotal,
			Skipped:    job.skipped.Load(),
		}
		s.mu.Unlock()
		return json.Marshal(resp)
	}
	defer s.mu.Unlock()
	if job != nil {
		return nil, errArchiveNotFound
	}

	meta, zipPath, metaPath, err := s.loadMeta(req.ArchiveID, req.Root)
	if err != nil {
		return nil, err
	}
	resp := daemon.ArchiveStatusResponse{
		State:      meta.State,
		FilesDone:  meta.FilesTotal,
		FilesTotal: meta.FilesTotal,
		BytesDone:  meta.BytesTotal,
		BytesTotal: meta.BytesTotal,
		Skipped:    meta.Skipped,
		Error:      meta.Error,
	}
	switch meta.State {
	case daemon.ArchiveStateReady:
		info, err := os.Stat(zipPath)
		if err != nil {
			return nil, errArchiveNotFound
		}
		resp.ArchiveSize = info.Size()
	case daemon.ArchiveStateRunning:
		// Recorded as running, yet zipped by nothing in this process: the
		// daemon restarted part way. The half-written archive is useless.
		_ = os.Remove(zipPath)
		_ = os.Remove(metaPath)
		resp.State = daemon.ArchiveStateFailed
		resp.Error = "the machine restarted while the folder was being zipped; try again"
		resp.FilesDone, resp.BytesDone = 0, 0
	case daemon.ArchiveStateFailed:
		resp.FilesDone, resp.BytesDone = 0, 0
	}
	return json.Marshal(resp)
}

// =============================================================================
// fs.archive_read
// =============================================================================

func (s *archiveStore) handleRead(_ context.Context, payload []byte) ([]byte, error) {
	var req daemon.ArchiveReadRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := checkArchiveID(req.ArchiveID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	running := s.jobs[req.ArchiveID] != nil
	s.mu.Unlock()
	if running {
		return nil, fmt.Errorf("%s: it is still being zipped", daemon.ArchiveNotReadyMarker)
	}
	meta, zipPath, metaPath, err := s.loadMeta(req.ArchiveID, req.Root)
	if err != nil {
		return nil, err
	}
	switch meta.State {
	case daemon.ArchiveStateReady:
	case daemon.ArchiveStateFailed:
		return nil, fmt.Errorf("%s: zipping failed: %s", daemon.ArchiveNotReadyMarker, meta.Error)
	default:
		return nil, fmt.Errorf("%s: it is still being zipped", daemon.ArchiveNotReadyMarker)
	}
	f, err := os.Open(zipPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errArchiveNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("opening folder archive %s: %w", req.ArchiveID, err)
	}
	defer f.Close()
	resp, err := readRange(f, meta.Filename, req.Offset, req.Length, "")
	if err != nil {
		return nil, err
	}
	resp.Filename = meta.Filename
	// A download in progress keeps its archive from the sweep.
	now := s.clock()
	_ = os.Chtimes(metaPath, now, now)
	return json.Marshal(resp)
}

// =============================================================================
// fs.archive_release
// =============================================================================

func (s *archiveStore) handleRelease(_ context.Context, payload []byte) ([]byte, error) {
	var req daemon.ArchiveRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := checkArchiveID(req.ArchiveID); err != nil {
		return nil, err
	}
	root := filepath.Clean(req.Root)
	s.mu.Lock()
	job := s.jobs[req.ArchiveID]
	if job != nil {
		if job.root != root {
			// Another workspace's archive: not this caller's to delete.
			s.mu.Unlock()
			return json.Marshal(daemon.ArchiveReleaseResponse{})
		}
		job.released = true
		job.cancel()
	}
	s.mu.Unlock()
	if job != nil {
		// build deletes a released archive's files itself when it stops.
		select {
		case <-job.done:
		case <-time.After(archiveReleaseWait):
		}
		return json.Marshal(daemon.ArchiveReleaseResponse{})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, zipPath, metaPath, err := s.loadMeta(req.ArchiveID, req.Root)
	if errors.Is(err, errArchiveNotFound) {
		return json.Marshal(daemon.ArchiveReleaseResponse{})
	}
	if err != nil {
		return nil, err
	}
	_ = os.Remove(metaPath)
	if err := os.Remove(zipPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("deleting folder archive %s: %w", req.ArchiveID, err)
	}
	return json.Marshal(daemon.ArchiveReleaseResponse{})
}
