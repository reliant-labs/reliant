// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// uploadFixture is one staging directory and one workspace root, private to
// its test, so every test runs in parallel without touching $HOME.
type uploadFixture struct {
	t       *testing.T
	store   *uploadStore
	staging string
	root    string
}

func newUploadFixture(t *testing.T) *uploadFixture {
	t.Helper()
	staging := filepath.Join(t.TempDir(), "uploads", ".partial")
	return &uploadFixture{
		t:       t,
		store:   &uploadStore{dir: func() (string, error) { return staging, nil }},
		staging: staging,
		root:    t.TempDir(),
	}
}

func (f *uploadFixture) call(handler CommandHandler, req any, resp any) error {
	f.t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := handler(context.Background(), payload)
	if err != nil {
		return err
	}
	if resp != nil {
		if err := json.Unmarshal(out, resp); err != nil {
			f.t.Fatalf("decode %s: %v", out, err)
		}
	}
	return nil
}

func (f *uploadFixture) begin(name string, size int64, policy string) (daemon.UploadBeginResponse, error) {
	f.t.Helper()
	var resp daemon.UploadBeginResponse
	err := f.call(f.store.handleBegin, daemon.UploadBeginRequest{
		Root: f.root, Path: filepath.Join(f.root, name), Size: size, OnConflict: policy,
	}, &resp)
	return resp, err
}

func (f *uploadFixture) mustBegin(name string, size int64, policy string) string {
	f.t.Helper()
	resp, err := f.begin(name, size, policy)
	if err != nil {
		f.t.Fatalf("begin %s: %v", name, err)
	}
	return resp.UploadID
}

func (f *uploadFixture) chunk(id string, offset int64, data []byte) (int64, error) {
	f.t.Helper()
	var resp daemon.UploadProgress
	err := f.call(f.store.handleChunk, daemon.UploadChunkRequest{
		UploadID: id, Root: f.root, Offset: offset, Data: base64.StdEncoding.EncodeToString(data),
	}, &resp)
	return resp.BytesReceived, err
}

func (f *uploadFixture) mustChunk(id string, offset int64, data []byte) {
	f.t.Helper()
	if _, err := f.chunk(id, offset, data); err != nil {
		f.t.Fatalf("chunk at %d: %v", offset, err)
	}
}

func (f *uploadFixture) commit(id, policy string) (daemon.UploadCommitResponse, error) {
	f.t.Helper()
	var resp daemon.UploadCommitResponse
	err := f.call(f.store.handleCommit, daemon.UploadSessionRequest{UploadID: id, Root: f.root, OnConflict: policy}, &resp)
	return resp, err
}

func (f *uploadFixture) status(id string) daemon.UploadStatusResponse {
	f.t.Helper()
	var resp daemon.UploadStatusResponse
	if err := f.call(f.store.handleStatus, daemon.UploadSessionRequest{UploadID: id, Root: f.root}, &resp); err != nil {
		f.t.Fatalf("status: %v", err)
	}
	return resp
}

func (f *uploadFixture) abort(id string) error {
	f.t.Helper()
	return f.call(f.store.handleAbort, daemon.UploadSessionRequest{UploadID: id, Root: f.root}, nil)
}

// stagingEntries lists the staging directory.
func (f *uploadFixture) stagingEntries() []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.staging)
	if err != nil {
		f.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func wantMarker(t *testing.T, err error, marker string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), marker) {
		t.Fatalf("err = %v, want one containing %q", err, marker)
	}
}

func readUploaded(t *testing.T, path string) []byte {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var uploadContent = []byte("0123456789\x00\xff\xfe not UTF-8, in several chunks")

func TestUpload_BeginChunkCommitPlacesTheFile(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	size := int64(len(uploadContent))
	resp, err := f.begin(filepath.Join("sub", "dir", "a.bin"), size, "")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !validUploadID(resp.UploadID) || resp.Path != filepath.Join(f.root, "sub", "dir", "a.bin") {
		t.Fatalf("begin = %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(f.root, "sub")); !os.IsNotExist(err) {
		t.Fatalf("begin created the destination's directories (stat: %v)", err)
	}
	for at := int64(0); at < size; at += 10 {
		got, err := f.chunk(resp.UploadID, at, uploadContent[at:min(at+10, size)])
		if err != nil || got != min(at+10, size) {
			t.Fatalf("chunk at %d = %d, %v", at, got, err)
		}
	}
	if st := f.status(resp.UploadID); !st.Exists || st.BytesReceived != size || st.Size != size {
		t.Fatalf("status = %+v", st)
	}
	done, err := f.commit(resp.UploadID, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if done.Path != resp.Path || done.Size != size || !done.Created {
		t.Fatalf("commit = %+v", done)
	}
	if got := readUploaded(t, resp.Path); !bytes.Equal(got, uploadContent) {
		t.Fatalf("placed % x, want % x", got, uploadContent)
	}
	if left := f.stagingEntries(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
	if st := f.status(resp.UploadID); st.Exists {
		t.Fatalf("a committed session still exists: %+v", st)
	}
}

func TestUpload_EmptyFile(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	id := f.mustBegin("empty.txt", 0, "")
	done, err := f.commit(id, "")
	if err != nil || !done.Created || done.Size != 0 {
		t.Fatalf("commit = %+v, %v", done, err)
	}
	if got := readUploaded(t, done.Path); len(got) != 0 {
		t.Fatalf("placed %q", got)
	}
}

func TestUpload_ChunkOffsets(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	id := f.mustBegin("a.bin", 20, "")
	f.mustChunk(id, 0, []byte("0123456789"))

	// A chunk re-sent after a lost reply rewrites the same bytes.
	if got, err := f.chunk(id, 0, []byte("0123456789")); err != nil || got != 10 {
		t.Fatalf("re-sent chunk = %d, %v", got, err)
	}
	if got, err := f.chunk(id, 5, []byte("56789")); err != nil || got != 10 {
		t.Fatalf("overlapping chunk = %d, %v", got, err)
	}

	// A gap is refused, and says how far the upload got.
	_, err := f.chunk(id, 15, []byte("x"))
	wantMarker(t, err, daemon.UploadOffsetMarker)
	wantMarker(t, err, "10 bytes received")

	// Past the declared size is refused, and writes nothing.
	_, err = f.chunk(id, 10, []byte("0123456789X"))
	wantMarker(t, err, daemon.UploadTooLargeMarker)
	if st := f.status(id); st.BytesReceived != 10 {
		t.Fatalf("status = %+v after refused chunks", st)
	}

	// Committing early is refused and keeps what arrived.
	_, err = f.commit(id, "")
	wantMarker(t, err, daemon.UploadIncompleteMarker)
	wantMarker(t, err, "10 of 20")
	f.mustChunk(id, 10, []byte("abcdefghij"))
	done, err := f.commit(id, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := string(readUploaded(t, done.Path)); got != "0123456789abcdefghij" {
		t.Fatalf("placed %q", got)
	}
}

func TestUpload_SizeOverTheCapIsRefused(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	for _, tc := range []struct {
		size, max int64
		ok        bool
	}{
		{size: 10, max: 10, ok: true},
		{size: 11, max: 10},
		{size: daemon.DefaultUploadMaxSize + 1},
		{size: -1},
	} {
		var resp daemon.UploadBeginResponse
		err := f.call(f.store.handleBegin, daemon.UploadBeginRequest{
			Root: f.root, Path: filepath.Join(f.root, "a.bin"), Size: tc.size, MaxSize: tc.max,
		}, &resp)
		switch {
		case tc.ok && err != nil:
			t.Errorf("size %d max %d: %v", tc.size, tc.max, err)
		case !tc.ok && tc.size >= 0:
			wantMarker(t, err, daemon.UploadTooLargeMarker)
		case !tc.ok && err == nil:
			t.Errorf("size %d was accepted", tc.size)
		}
	}
}

func TestUpload_NotEnoughSpaceIsRefused(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	f.store.available = func(string) (uint64, bool) { return 99, true }
	_, err := f.begin("a.bin", 100, "")
	wantMarker(t, err, daemon.UploadNoSpaceMarker)
	f.mustBegin("b.bin", 99, "")
	// A filesystem whose free space cannot be read is not checked.
	f.store.available = func(string) (uint64, bool) { return 0, false }
	f.mustBegin("c.bin", 100, "")
}

func TestUpload_FailPolicyRefusesAnExistingFile(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	dest := filepath.Join(f.root, "a.txt")
	theirs := []byte("theirs")

	t.Run("at begin", func(t *testing.T) {
		if err := os.WriteFile(dest, theirs, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := f.begin("a.txt", 3, daemon.UploadConflictFail)
		wantMarker(t, err, daemon.UploadExistsMarker)
		if err := os.Remove(dest); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("at commit, keeping the session", func(t *testing.T) {
		id := f.mustBegin("a.txt", 3, "")
		f.mustChunk(id, 0, []byte("new"))
		// The file appears while the upload runs.
		if err := os.WriteFile(dest, theirs, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := f.commit(id, "")
		wantMarker(t, err, daemon.UploadExistsMarker)
		if got := readUploaded(t, dest); !bytes.Equal(got, theirs) {
			t.Fatalf("the existing file became %q", got)
		}
		if st := f.status(id); !st.Exists || st.BytesReceived != 3 {
			t.Fatalf("the refused commit lost the session: %+v", st)
		}
		// The person chose Replace: the same session commits with an
		// override, without re-sending anything.
		done, err := f.commit(id, daemon.UploadConflictOverwrite)
		if err != nil {
			t.Fatalf("commit with overwrite: %v", err)
		}
		if done.Created || done.Path != dest {
			t.Fatalf("commit = %+v", done)
		}
		if got := string(readUploaded(t, dest)); got != "new" {
			t.Fatalf("placed %q", got)
		}
	})
}

func TestUpload_RenamePolicyTakesAFreeName(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "a.txt"), []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := f.mustBegin("a.txt", 3, daemon.UploadConflictRename)
	f.mustChunk(id, 0, []byte("new"))
	done, err := f.commit(id, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if want := filepath.Join(f.root, "a (1).txt"); done.Path != want || !done.Created {
		t.Fatalf("commit = %+v, want a new file at %s", done, want)
	}
	if got := string(readUploaded(t, filepath.Join(f.root, "a.txt"))); got != "theirs" {
		t.Fatalf("the existing file became %q", got)
	}
	if got := string(readUploaded(t, done.Path)); got != "new" {
		t.Fatalf("placed %q", got)
	}
}

func TestUpload_OverwritePolicyReplaces(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	dest := filepath.Join(f.root, "a.txt")
	if err := os.WriteFile(dest, []byte("an older, longer file"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := f.mustBegin("a.txt", 3, daemon.UploadConflictOverwrite)
	f.mustChunk(id, 0, []byte("new"))
	done, err := f.commit(id, "")
	if err != nil || done.Created || done.Path != dest {
		t.Fatalf("commit = %+v, %v", done, err)
	}
	if got := string(readUploaded(t, dest)); got != "new" {
		t.Fatalf("placed %q", got)
	}
}

func TestUpload_Abort(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	id := f.mustBegin("a.txt", 10, "")
	f.mustChunk(id, 0, []byte("01234"))
	if err := f.abort(id); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if left := f.stagingEntries(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
	if st := f.status(id); st.Exists {
		t.Fatalf("an aborted session still exists: %+v", st)
	}
	// Aborting what is already gone succeeds.
	if err := f.abort(id); err != nil {
		t.Fatalf("second abort: %v", err)
	}
	if err := f.abort(strings.Repeat("ab", 16)); err != nil {
		t.Fatalf("abort of an unknown session: %v", err)
	}
	_, err := f.chunk(id, 5, []byte("56789"))
	wantMarker(t, err, daemon.UploadNotFoundMarker)
}

func TestUpload_AnotherRootIsNotFound(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	id := f.mustBegin("a.txt", 3, "")
	other := *f
	other.root = t.TempDir()

	if st := other.status(id); st.Exists {
		t.Fatalf("status from another root = %+v", st)
	}
	_, err := other.chunk(id, 0, []byte("abc"))
	wantMarker(t, err, daemon.UploadNotFoundMarker)
	_, err = other.commit(id, "")
	wantMarker(t, err, daemon.UploadNotFoundMarker)
	// Abort from another root succeeds and leaves the session alone.
	if err := other.abort(id); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if st := f.status(id); !st.Exists {
		t.Fatal("another root's abort removed the session")
	}
}

func TestUpload_InvalidIDIsRejected(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	// A file a traversing id would name if it were joined into a path.
	victim := filepath.Join(filepath.Dir(f.staging), "victim.json")
	if err := os.MkdirAll(filepath.Dir(victim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../victim", "ABCDEF0123456789ABCDEF0123456789", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("g", 32)} {
		for name, call := range map[string]func() error{
			"chunk":  func() error { _, err := f.chunk(id, 0, []byte("x")); return err },
			"commit": func() error { _, err := f.commit(id, ""); return err },
			"abort":  func() error { return f.abort(id) },
			"status": func() error {
				return f.call(f.store.handleStatus, daemon.UploadSessionRequest{UploadID: id, Root: f.root}, nil)
			},
		} {
			err := call()
			if err == nil || !strings.Contains(err.Error(), "invalid upload id") {
				t.Errorf("%s(%q) = %v, want the invalid id refusal", name, id, err)
			}
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the file outside the staging directory: %v", err)
	}
}

func TestUpload_RefusedDestinations(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(f.root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		".git":             filepath.Join(f.root, ".git", "config"),
		"dot-dot":          f.root + string(filepath.Separator) + filepath.Join("..", "x.txt"),
		"outside the root": filepath.Join(outside, "x.txt"),
		"a directory":      filepath.Join(f.root, "dir"),
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(f.root, "escape")); err != nil {
			t.Fatal(err)
		}
		cases["a symlinked directory out of the root"] = filepath.Join(f.root, "escape", "x.txt")
	}
	for name, path := range cases {
		err := f.call(f.store.handleBegin, daemon.UploadBeginRequest{Root: f.root, Path: path, Size: 1}, nil)
		if err == nil || !strings.Contains(err.Error(), daemon.UploadRefusedPathMarker) {
			t.Errorf("%s: err = %v, want the refusal", name, err)
		}
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside the root: %v, %v", entries, err)
	}
}

func TestUpload_CommitRechecksTheDestination(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newUploadFixture(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(f.root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := f.mustBegin(filepath.Join("sub", "x.txt"), 3, "")
	f.mustChunk(id, 0, []byte("new"))
	// The directory is swapped for a symlink out of the workspace.
	if err := os.Remove(filepath.Join(f.root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "sub")); err != nil {
		t.Fatal(err)
	}
	_, err := f.commit(id, "")
	wantMarker(t, err, daemon.UploadRefusedPathMarker)
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("the upload escaped the root (stat: %v)", err)
	}
}

func TestUpload_BeginSweepsStaleSessions(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	stale := f.mustBegin("stale.txt", 10, "")
	fresh := f.mustBegin("fresh.txt", 10, "")
	old := time.Now().Add(-uploadStaleAfter - time.Hour)
	part, meta := uploadFiles(f.staging, stale)
	for _, p := range []string{part, meta} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	strayTmp := filepath.Join(f.staging, stale+".json.123.tmp")
	if err := os.WriteFile(strayTmp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(strayTmp, old, old); err != nil {
		t.Fatal(err)
	}

	f.mustBegin("next.txt", 10, "")
	if st := f.status(stale); st.Exists {
		t.Fatalf("a session idle past %s survived a begin: %+v", uploadStaleAfter, st)
	}
	for _, p := range []string{part, meta, strayTmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep (stat: %v)", filepath.Base(p), err)
		}
	}
	if st := f.status(fresh); !st.Exists {
		t.Fatal("the sweep removed a fresh session")
	}
}

func TestUpload_ConcurrentChunksOfOneSession(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t)
	const pieces, piece = 16, 64
	content := bytes.Repeat([]byte("abcdefgh"), pieces*piece/8)
	id := f.mustBegin("a.bin", int64(len(content)), "")
	// Each piece is sent in order by one goroutine, and re-sent at once by
	// several others: the session lock keeps them from interleaving into a
	// gap or a torn write.
	for i := range pieces {
		at := int64(i * piece)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := f.chunk(id, at, content[at:at+piece]); err != nil {
					t.Errorf("chunk at %d: %v", at, err)
				}
			}()
		}
		wg.Wait()
	}
	done, err := f.commit(id, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := readUploaded(t, done.Path); !bytes.Equal(got, content) {
		t.Fatal("the placed file differs from what was sent")
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if len(f.store.locks) != 0 {
		t.Fatalf("%d session locks outlived their commands", len(f.store.locks))
	}
}

func TestUpload_CommandsAreRegistered(t *testing.T) {
	t.Parallel()
	// An unknown session's status is the one call that touches no file.
	payload, _ := json.Marshal(daemon.UploadSessionRequest{UploadID: strings.Repeat("0", 32), Root: t.TempDir()})
	out, err := DefaultRegistry().Handle(context.Background(), daemon.UploadStatusCommand, payload)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(string(out), `"exists":false`) {
		t.Fatalf("status = %s", out)
	}
	for _, cmd := range []string{daemon.UploadBeginCommand, daemon.UploadChunkCommand, daemon.UploadCommitCommand, daemon.UploadAbortCommand} {
		if _, err := DefaultRegistry().Handle(context.Background(), cmd, []byte(`{"upload_id":"x"}`)); err == nil || strings.Contains(err.Error(), "doesn't support") {
			t.Errorf("%s: %v", cmd, err)
		}
	}
}
