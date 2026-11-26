// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

func callCommand(t *testing.T, h CommandHandler, req, resp any) error {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h(context.Background(), payload)
	if err != nil {
		return err
	}
	if resp != nil {
		if err := json.Unmarshal(out, resp); err != nil {
			t.Fatalf("decode %s: %v", out, err)
		}
	}
	return nil
}

func writeTransferFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// =============================================================================
// fs.read_range
// =============================================================================

// A file read piece by piece comes back byte for byte, every piece with the
// same version, and the last one marked eof.
func TestFSReadRange_ReadsAFileInPieces(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	content := bytes.Repeat([]byte("0123456789abcdef"), (daemon.MaxReadRangeBytes*5/2)/16)
	path := filepath.Join(root, "big.bin")
	writeTransferFile(t, path, content)

	var got []byte
	var version string
	for offset := int64(0); ; {
		var piece daemon.ReadRangeResponse
		// Asking for more than a piece is clamped to one.
		if err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{
			Path: path, ViewerRoot: root, Offset: offset, Length: 8 << 20, IfVersion: version,
		}, &piece); err != nil {
			t.Fatalf("read at %d: %v", offset, err)
		}
		data, err := base64.StdEncoding.DecodeString(piece.Data)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > daemon.MaxReadRangeBytes {
			t.Fatalf("piece of %d bytes, want at most %d", len(data), daemon.MaxReadRangeBytes)
		}
		if !piece.ViewerGuarded {
			t.Fatal("reply is not marked viewer_guarded")
		}
		if version == "" {
			version = piece.Version
		} else if piece.Version != version {
			t.Fatalf("version changed from %q to %q on an unchanged file", version, piece.Version)
		}
		if piece.TotalSize != int64(len(content)) {
			t.Fatalf("total size %d, want %d", piece.TotalSize, len(content))
		}
		got = append(got, data...)
		offset += int64(len(data))
		if piece.EOF {
			break
		}
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("read %d bytes, not the file's %d", len(got), len(content))
	}

	// At the end: no bytes, eof. Past it: refused.
	var end daemon.ReadRangeResponse
	if err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: path, ViewerRoot: root, Offset: int64(len(content))}, &end); err != nil {
		t.Fatal(err)
	}
	if end.Data != "" || !end.EOF {
		t.Fatalf("read at the end = %+v, want empty and eof", end)
	}
	err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: path, ViewerRoot: root, Offset: int64(len(content)) + 1}, nil)
	if err == nil || !strings.Contains(err.Error(), daemon.ReadRangeOffsetMarker) {
		t.Fatalf("read past the end = %v, want %q", err, daemon.ReadRangeOffsetMarker)
	}
}

// A file rewritten between pieces is refused, so a download never stitches
// two versions together.
func TestFSReadRange_DetectsAFileChangingMidDownload(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	path := filepath.Join(root, "log.txt")
	writeTransferFile(t, path, []byte("first version"))

	var first daemon.ReadRangeResponse
	if err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: path, ViewerRoot: root, Length: 5}, &first); err != nil {
		t.Fatal(err)
	}
	writeTransferFile(t, path, []byte("second, longer version"))
	err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: path, ViewerRoot: root, Offset: 5, IfVersion: first.Version}, nil)
	if err == nil || !strings.Contains(err.Error(), daemon.FileChangedMarker) {
		t.Fatalf("read of a changed file = %v, want %q", err, daemon.FileChangedMarker)
	}
}

func TestFSReadRange_RequiresTheViewerRootAndAFile(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	writeTransferFile(t, filepath.Join(root, "a.txt"), []byte("a"))
	if err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: filepath.Join(root, "a.txt")}, nil); err == nil {
		t.Fatal("a read without viewer_root was served")
	}
	if err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: root, ViewerRoot: root}, nil); err == nil {
		t.Fatal("a directory was read as a file")
	}
}

// Like fs.read_binary_file, a ranged read never opens a credential store,
// named directly or through a link in the project.
func TestFSReadRange_RefusesCredentialStores(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	key := filepath.Join(home, ".ssh", "id_rsa")
	project := filepath.Join(home, "proj")
	writeTransferFile(t, key, []byte("PRIVATE KEY"))
	writeTransferFile(t, filepath.Join(project, "README.md"), []byte("readme"))
	if err := os.Symlink(key, filepath.Join(project, "key")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{key, filepath.Join(project, "key")} {
		err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: p, ViewerRoot: project}, nil)
		if !errors.Is(err, daemonpolicy.ErrViewerCredential) {
			t.Errorf("read_range(%s) = %v, want ErrViewerCredential", p, err)
		}
	}
	if err := callCommand(t, handleFSReadRange, daemon.ReadRangeRequest{Path: filepath.Join(project, "README.md"), ViewerRoot: project}, nil); err != nil {
		t.Errorf("a project file: %v", err)
	}
}

// =============================================================================
// Folder archives
// =============================================================================

func TestArchivePrepareReservesCapacityBeforeScanning(t *testing.T) {
	f := newArchiveFixture(t)
	writeTransferFile(t, filepath.Join(f.root, "file"), []byte("content"))
	entered := make(chan struct{}, archiveMaxRunning)
	release := make(chan struct{})
	finished := make(chan struct{}, archiveMaxRunning)
	defer func() {
		close(release)
		for range archiveMaxRunning {
			<-finished
		}
	}()
	var calls atomic.Int64
	f.store.available = func(string) (uint64, bool) {
		if calls.Add(1) <= archiveMaxRunning {
			entered <- struct{}{}
			<-release
		}
		return 0, true
	}
	payload, err := json.Marshal(daemon.ArchivePrepareRequest{Root: f.root, Path: f.root})
	if err != nil {
		t.Fatal(err)
	}
	for range archiveMaxRunning {
		go func() {
			_, _ = f.store.handlePrepare(context.Background(), payload)
			finished <- struct{}{}
		}()
	}
	for range archiveMaxRunning {
		<-entered
	}
	_, err = f.store.handlePrepare(context.Background(), payload)
	if err == nil || !strings.Contains(err.Error(), "already being zipped") {
		t.Fatalf("prepare with all slots reserved = %v; want capacity refusal", err)
	}
}

type archiveFixture struct {
	t     *testing.T
	store *archiveStore
	dir   string
	root  string
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	dir := filepath.Join(realTempDir(t), "uploads", ".partial", "archives")
	return &archiveFixture{t: t, store: &archiveStore{dir: func() (string, error) { return dir, nil }}, dir: dir, root: realTempDir(t)}
}

func (f *archiveFixture) prepare(path string, maxBytes int64) (daemon.ArchivePrepareResponse, error) {
	f.t.Helper()
	var resp daemon.ArchivePrepareResponse
	err := callCommand(f.t, f.store.handlePrepare, daemon.ArchivePrepareRequest{Root: f.root, Path: path, MaxBytes: maxBytes}, &resp)
	return resp, err
}

func (f *archiveFixture) status(id, root string) (daemon.ArchiveStatusResponse, error) {
	f.t.Helper()
	var resp daemon.ArchiveStatusResponse
	err := callCommand(f.t, f.store.handleStatus, daemon.ArchiveRequest{ArchiveID: id, Root: root}, &resp)
	return resp, err
}

func (f *archiveFixture) wait(id string) daemon.ArchiveStatusResponse {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := f.status(id, f.root)
		if err != nil {
			f.t.Fatalf("status: %v", err)
		}
		if st.State != daemon.ArchiveStateRunning {
			return st
		}
		if time.Now().After(deadline) {
			f.t.Fatal("the archive never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// download reads the whole archive in pieces, as ReadFileRange does.
func (f *archiveFixture) download(id string) ([]byte, string) {
	f.t.Helper()
	var out []byte
	var name string
	for offset := int64(0); ; {
		var piece daemon.ReadRangeResponse
		if err := callCommand(f.t, f.store.handleRead, daemon.ArchiveReadRequest{ArchiveID: id, Root: f.root, Offset: offset, Length: 64}, &piece); err != nil {
			f.t.Fatalf("archive read at %d: %v", offset, err)
		}
		data, _ := base64.StdEncoding.DecodeString(piece.Data)
		out = append(out, data...)
		offset += int64(len(data))
		name = piece.Filename
		if piece.EOF {
			return out, name
		}
	}
}

func (f *archiveFixture) release(id, root string) error {
	f.t.Helper()
	return callCommand(f.t, f.store.handleRelease, daemon.ArchiveRequest{ArchiveID: id, Root: root}, nil)
}

func (f *archiveFixture) leftovers() []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.dir)
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// zipContents maps each entry of a zip to its content ("" for a folder).
func zipContents(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[zf.Name] = string(b)
	}
	return out
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The archive holds the folder under its own name, empty folders included,
// and leaves out .git, symlinks that escape the workspace or lead to a folder,
// and nothing else; a symlink to a file inside the workspace is followed.
func TestFSArchive_ZipsAFolderSkippingGitAndEscapes(t *testing.T) {
	t.Parallel()
	f := newArchiveFixture(t)
	outside := filepath.Join(realTempDir(t), "secret.txt")
	writeTransferFile(t, outside, []byte("outside"))
	src := filepath.Join(f.root, "src")
	writeTransferFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))
	writeTransferFile(t, filepath.Join(src, "sub", "b.bin"), []byte{0, 1, 2, 255})
	writeTransferFile(t, filepath.Join(src, ".git", "config"), []byte("[core]"))
	writeTransferFile(t, filepath.Join(src, "sub", ".git"), []byte("gitdir: elsewhere"))
	writeTransferFile(t, filepath.Join(f.root, "other.txt"), []byte("in the workspace"))
	writeTransferFile(t, filepath.Join(f.root, ".git", "HEAD"), []byte("ref: main"))
	if err := os.MkdirAll(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"link-in":  filepath.Join(f.root, "other.txt"),
		"link-out": outside,
		"link-dir": filepath.Join(src, "sub"),
		"link-git": filepath.Join(f.root, ".git", "HEAD"),
		"dangling": filepath.Join(f.root, "nope"),
	} {
		if err := os.Symlink(target, filepath.Join(src, link)); err != nil {
			t.Fatal(err)
		}
	}

	prep, err := f.prepare(src, 0)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prep.TotalFiles != 3 {
		t.Errorf("total files %d, want 3 (a.txt, sub/b.bin, link-in)", prep.TotalFiles)
	}
	st := f.wait(prep.ArchiveID)
	if st.State != daemon.ArchiveStateReady {
		t.Fatalf("state %q (%s), want ready", st.State, st.Error)
	}
	if st.Skipped != 4 {
		t.Errorf("skipped %d, want 4 (link-out, link-dir, link-git, dangling)", st.Skipped)
	}
	data, name := f.download(prep.ArchiveID)
	if name != "src.zip" {
		t.Errorf("filename %q, want src.zip", name)
	}
	if int64(len(data)) != st.ArchiveSize {
		t.Errorf("downloaded %d bytes, status said %d", len(data), st.ArchiveSize)
	}
	got := zipContents(t, data)
	want := map[string]string{
		"src/":          "",
		"src/a.txt":     "alpha",
		"src/empty/":    "",
		"src/link-in":   "in the workspace",
		"src/sub/":      "",
		"src/sub/b.bin": "\x00\x01\x02\xff",
	}
	if strings.Join(keys(got), ",") != strings.Join(keys(want), ",") {
		t.Fatalf("archive holds %v, want %v", keys(got), keys(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	if err := f.release(prep.ArchiveID, f.root); err != nil {
		t.Fatal(err)
	}
	if left := f.leftovers(); len(left) != 0 {
		t.Fatalf("release left %v behind", left)
	}
	if err := f.release(prep.ArchiveID, f.root); err != nil {
		t.Fatalf("releasing an unknown archive: %v", err)
	}
}

func TestFSArchive_RefusesFoldersOutsideTheWorkspace(t *testing.T) {
	t.Parallel()
	f := newArchiveFixture(t)
	elsewhere := realTempDir(t)
	writeTransferFile(t, filepath.Join(elsewhere, "x.txt"), []byte("x"))
	writeTransferFile(t, filepath.Join(f.root, ".git", "HEAD"), []byte("ref"))
	writeTransferFile(t, filepath.Join(f.root, "file.txt"), []byte("f"))
	if err := os.Symlink(elsewhere, filepath.Join(f.root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{elsewhere, filepath.Join(f.root, "escape"), filepath.Join(f.root, ".git"), filepath.Join(f.root, "file.txt")} {
		if _, err := f.prepare(p, 0); err == nil || !strings.Contains(err.Error(), daemon.ArchiveRefusedPathMarker) {
			t.Errorf("prepare(%s) = %v, want %q", p, err, daemon.ArchiveRefusedPathMarker)
		}
	}
	if _, err := f.prepare(filepath.Join(f.root, "missing"), 0); err == nil || !os.IsNotExist(errors.Unwrap(err)) {
		t.Errorf("prepare of a missing folder = %v, want a not-exist error", err)
	}
	if left := f.leftovers(); len(left) != 0 {
		t.Fatalf("a refused prepare left %v behind", left)
	}
}

// A folder over the cap is refused before anything is written.
func TestFSArchive_RefusesAFolderOverTheCap(t *testing.T) {
	t.Parallel()
	f := newArchiveFixture(t)
	writeTransferFile(t, filepath.Join(f.root, "a.bin"), make([]byte, 600))
	writeTransferFile(t, filepath.Join(f.root, "b.bin"), make([]byte, 600))
	_, err := f.prepare(f.root, 1000)
	if err == nil || !strings.Contains(err.Error(), daemon.ArchiveTooLargeMarker) {
		t.Fatalf("prepare = %v, want %q", err, daemon.ArchiveTooLargeMarker)
	}
	if left := f.leftovers(); len(left) != 0 {
		t.Fatalf("a refused prepare left %v behind", left)
	}
	if _, err := f.prepare(f.root, 1200); err != nil {
		t.Fatalf("exactly at the cap: %v", err)
	}
}

// An archive is bound to the workspace root it was prepared for: another
// root can neither see, read nor release it.
func TestFSArchive_IsBoundToItsWorkspace(t *testing.T) {
	t.Parallel()
	f := newArchiveFixture(t)
	writeTransferFile(t, filepath.Join(f.root, "a.txt"), []byte("a"))
	prep, err := f.prepare(f.root, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.wait(prep.ArchiveID)
	other := realTempDir(t)
	if _, err := f.status(prep.ArchiveID, other); err == nil || !strings.Contains(err.Error(), daemon.ArchiveNotFoundMarker) {
		t.Errorf("status from another root = %v, want not found", err)
	}
	err = callCommand(t, f.store.handleRead, daemon.ArchiveReadRequest{ArchiveID: prep.ArchiveID, Root: other}, nil)
	if err == nil || !strings.Contains(err.Error(), daemon.ArchiveNotFoundMarker) {
		t.Errorf("read from another root = %v, want not found", err)
	}
	if err := f.release(prep.ArchiveID, other); err != nil {
		t.Fatal(err)
	}
	if len(f.leftovers()) != 2 {
		t.Fatalf("another root's release deleted the archive: %v", f.leftovers())
	}
	// An id that is not one the daemon mints is never joined into a path.
	for _, id := range []string{"../../etc/passwd", "", strings.Repeat("A", 32)} {
		if _, err := f.status(id, f.root); err == nil || !strings.Contains(err.Error(), daemon.ArchiveNotFoundMarker) {
			t.Errorf("status(%q) = %v, want not found", id, err)
		}
	}
}

// Releasing an archive still being zipped stops it and deletes everything.
func TestFSArchive_ReleaseWhileZipping(t *testing.T) {
	t.Parallel()
	f := newArchiveFixture(t)
	for i := range 3 {
		writeTransferFile(t, filepath.Join(f.root, string(rune('a'+i))+".txt"), []byte("x"))
	}
	started := make(chan struct{}, 3)
	hold := make(chan struct{})
	f.store.beforeFile = func() {
		started <- struct{}{}
		<-hold
	}
	prep, err := f.prepare(f.root, 0)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	st, err := f.status(prep.ArchiveID, f.root)
	if err != nil || st.State != daemon.ArchiveStateRunning {
		t.Fatalf("status while zipping = %+v, %v", st, err)
	}
	err = callCommand(t, f.store.handleRead, daemon.ArchiveReadRequest{ArchiveID: prep.ArchiveID, Root: f.root}, nil)
	if err == nil || !strings.Contains(err.Error(), daemon.ArchiveNotReadyMarker) {
		t.Fatalf("read while zipping = %v, want %q", err, daemon.ArchiveNotReadyMarker)
	}
	done := make(chan error, 1)
	go func() { done <- f.release(prep.ArchiveID, f.root) }()
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if left := f.leftovers(); len(left) != 0 {
		t.Fatalf("release while zipping left %v behind", left)
	}
}

// An archive the daemon was zipping when it restarted reports failure, and
// its half-written files are removed; a stale archive is swept.
func TestFSArchive_RestartAndSweep(t *testing.T) {
	t.Parallel()
	f := newArchiveFixture(t)
	writeTransferFile(t, filepath.Join(f.root, "a.txt"), []byte("a"))

	// A description left "running" by a daemon that stopped part way.
	const interrupted = "00000000000000000000000000000001"
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	zipPath, metaPath := archiveFiles(f.dir, interrupted)
	writeTransferFile(t, zipPath, []byte("PK half"))
	if err := writeJSONFile(f.dir, metaPath, archiveMeta{Root: f.root, State: daemon.ArchiveStateRunning, Filename: "a.zip"}); err != nil {
		t.Fatal(err)
	}
	st, err := f.status(interrupted, f.root)
	if err != nil || st.State != daemon.ArchiveStateFailed || st.Error == "" {
		t.Fatalf("status of an interrupted archive = %+v, %v; want failed with a reason", st, err)
	}
	if len(f.leftovers()) != 0 {
		t.Fatalf("an interrupted archive left %v behind", f.leftovers())
	}

	prep, err := f.prepare(f.root, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.wait(prep.ArchiveID)
	later := time.Now().Add(archiveStaleAfter + time.Hour)
	f.store.now = func() time.Time { return later }
	next, err := f.prepare(f.root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.status(prep.ArchiveID, f.root); err == nil {
		t.Fatal("a stale archive was not swept")
	}
	f.wait(next.ArchiveID)
}

// A workspace rooted at the home directory holds credential stores and the
// archive directory itself: neither ends up in the archive.
func TestFSArchive_LeavesOutCredentialStoresAndItsOwnDirectory(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	writeTransferFile(t, filepath.Join(home, ".ssh", "id_rsa"), []byte("PRIVATE KEY"))
	writeTransferFile(t, filepath.Join(home, "notes.md"), []byte("notes"))
	dir := filepath.Join(home, filepath.FromSlash(daemon.ArchiveDir))
	writeTransferFile(t, filepath.Join(home, filepath.FromSlash(daemon.UploadPartialDir), "abc.part"), []byte("partial upload"))
	f := &archiveFixture{t: t, store: &archiveStore{dir: func() (string, error) { return dir, nil }}, dir: dir, root: home}

	prep, err := f.prepare(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st := f.wait(prep.ArchiveID); st.State != daemon.ArchiveStateReady {
		t.Fatalf("state %q: %s", st.State, st.Error)
	}
	data, _ := f.download(prep.ArchiveID)
	for name := range zipContents(t, data) {
		if strings.Contains(name, ".ssh") || strings.Contains(name, ".partial") {
			t.Errorf("archive holds %s", name)
		}
	}
	if got := zipContents(t, data)[filepath.Base(home)+"/notes.md"]; got != "notes" {
		t.Errorf("notes.md = %q, want it archived", got)
	}
}
