// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// newUserUploadFixture is an upload fixture whose uploads folder is the
// parent of its staging folder, as ~/.reliant/uploads is of
// ~/.reliant/uploads/.partial.
func newUserUploadFixture(t *testing.T) (*uploadFixture, string) {
	t.Helper()
	f := newUploadFixture(t)
	uploads := filepath.Dir(f.staging)
	f.store.userRoot = func() (string, error) { return uploads, nil }
	return f, uploads
}

func (f *uploadFixture) userBegin(folder, name string, size int64) (daemon.UploadBeginResponse, error) {
	f.t.Helper()
	var resp daemon.UploadBeginResponse
	err := f.call(f.store.handleUserBegin, daemon.UserUploadBeginRequest{
		Root: f.root, Folder: folder, Filename: name, Size: size,
	}, &resp)
	return resp, err
}

// userUpload uploads content as name into folder and commits it.
func (f *uploadFixture) userUpload(folder, name string, content []byte) daemon.UploadCommitResponse {
	f.t.Helper()
	begun, err := f.userBegin(folder, name, int64(len(content)))
	if err != nil {
		f.t.Fatalf("begin %s: %v", name, err)
	}
	f.mustChunk(begun.UploadID, 0, content)
	done, err := f.commit(begun.UploadID, "")
	if err != nil {
		f.t.Fatalf("commit %s: %v", name, err)
	}
	return done
}

func TestUserUpload_PlacesTheFileInTheUploadsFolder(t *testing.T) {
	t.Parallel()
	f, uploads := newUserUploadFixture(t)
	const chat = "8d1c2f6e-5b0a-4c1e-9f3e-2a7b6c5d4e3f"

	begun, err := f.userBegin(chat, "report.xlsx", int64(len(uploadContent)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	want := filepath.Join(uploads, chat, "report.xlsx")
	if begun.Path != want {
		t.Fatalf("begin path = %q, want %q", begun.Path, want)
	}
	f.mustChunk(begun.UploadID, 0, uploadContent[:7])
	f.mustChunk(begun.UploadID, 7, uploadContent[7:])
	done, err := f.commit(begun.UploadID, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if done.Path != want || done.Destination != daemon.UploadDestinationUserUploads || !done.Created {
		t.Fatalf("commit = %+v, want %s as a new user upload", done, want)
	}
	if got := readUploaded(t, want); !bytes.Equal(got, uploadContent) {
		t.Fatal("the placed file differs from what was sent")
	}
	// Nothing reached the workspace, and the staging folder is empty again.
	if entries, err := os.ReadDir(f.root); err != nil || len(entries) != 0 {
		t.Fatalf("workspace: %v, %v", entries, err)
	}
	if left := f.stagingEntries(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(uploads, chat))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("uploads folder mode = %v, want 0700", perm)
		}
	}
}

func TestUserUpload_ANameClashKeepsBoth(t *testing.T) {
	t.Parallel()
	f, uploads := newUserUploadFixture(t)
	first := f.userUpload("chat1", "data.csv", []byte("one"))
	// Whatever the commit asks for, a user upload never replaces or refuses.
	begun, err := f.userBegin("chat1", "data.csv", 3)
	if err != nil {
		t.Fatalf("begin over an existing name: %v", err)
	}
	f.mustChunk(begun.UploadID, 0, []byte("two"))
	second, err := f.commit(begun.UploadID, daemon.UploadConflictOverwrite)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if first.Path != filepath.Join(uploads, "chat1", "data.csv") || second.Path != filepath.Join(uploads, "chat1", "data (1).csv") {
		t.Fatalf("paths = %q, %q", first.Path, second.Path)
	}
	if got := string(readUploaded(t, first.Path)); got != "one" {
		t.Fatalf("the first upload was replaced: %q", got)
	}
}

func TestUserUpload_EscapeAttemptsAreRefused(t *testing.T) {
	t.Parallel()
	f, uploads := newUserUploadFixture(t)
	cases := map[string][2]string{
		"empty folder":           {"", "x.txt"},
		"dot-dot folder":         {"..", "x.txt"},
		"traversing folder":      {"../etc", "x.txt"},
		"nested folder":          {"a/b", "x.txt"},
		"backslash folder":       {`a\b`, "x.txt"},
		"absolute folder":        {"/tmp", "x.txt"},
		"the staging folder":     {".partial", "x.txt"},
		"hidden folder":          {".ssh", "x.txt"},
		"leading dash folder":    {"-rf", "x.txt"},
		"folder too long":        {strings.Repeat("a", 129), "x.txt"},
		"empty name":             {"chat", ""},
		"dot name":               {"chat", "."},
		"dot-dot name":           {"chat", ".."},
		"traversing name":        {"chat", "../../x.txt"},
		"backslash name":         {"chat", `..\x.txt`},
		"absolute name":          {"chat", "/etc/passwd"},
		"NUL in the name":        {"chat", "x\x00.txt"},
		"control char in name":   {"chat", "x\n.txt"},
		"name longer than 255 B": {"chat", strings.Repeat("é", 128) + ".txt"},
		"a .git name":            {"chat", ".git"},
	}
	for name, c := range cases {
		_, err := f.userBegin(c[0], c[1], 1)
		if err == nil || !strings.Contains(err.Error(), daemon.UploadRefusedPathMarker) {
			t.Errorf("%s: err = %v, want the refusal", name, err)
		}
	}
	// Nothing was staged, and nothing but the staging folder exists.
	entries, err := os.ReadDir(uploads)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".partial" {
			t.Errorf("a refused begin left %s in the uploads folder", e.Name())
		}
	}
}

func TestUserUpload_ASymlinkedFolderCannotCarryTheFileOut(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f, uploads := newUserUploadFixture(t)
	outside := t.TempDir()
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(uploads, "evil")); err != nil {
		t.Fatal(err)
	}
	_, err := f.userBegin("evil", "x.txt", 1)
	wantMarker(t, err, daemon.UploadRefusedPathMarker)

	// A folder swapped for a symlink between begin and commit is refused at
	// commit, and nothing lands outside.
	begun, err := f.userBegin("chat", "x.txt", 3)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	f.mustChunk(begun.UploadID, 0, []byte("abc"))
	if err := os.MkdirAll(filepath.Join(uploads, "chat"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(uploads, "chat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(uploads, "chat")); err != nil {
		t.Fatal(err)
	}
	_, err = f.commit(begun.UploadID, "")
	wantMarker(t, err, daemon.UploadRefusedPathMarker)
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside the uploads folder: %v, %v", entries, err)
	}
}

func TestUserUpload_SessionIsBoundToItsWorkspaceRoot(t *testing.T) {
	t.Parallel()
	f, _ := newUserUploadFixture(t)
	begun, err := f.userBegin("chat", "x.bin", 3)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	other := t.TempDir()
	var progress daemon.UploadProgress
	err = f.call(f.store.handleChunk, daemon.UploadChunkRequest{UploadID: begun.UploadID, Root: other, Data: "YWJj"}, &progress)
	wantMarker(t, err, daemon.UploadNotFoundMarker)
	if err := f.call(f.store.handleUserBegin, daemon.UserUploadBeginRequest{Folder: "chat", Filename: "x", Size: 1}, nil); err == nil {
		t.Fatal("a begin naming no workspace root was accepted")
	}
}

func TestUserUpload_SizeOverTheCapIsRefused(t *testing.T) {
	t.Parallel()
	f, _ := newUserUploadFixture(t)
	err := f.call(f.store.handleUserBegin, daemon.UserUploadBeginRequest{
		Root: f.root, Folder: "chat", Filename: "big.zip", Size: 11, MaxSize: 10,
	}, nil)
	wantMarker(t, err, daemon.UploadTooLargeMarker)
}

func TestUserUpload_CommandIsRegistered(t *testing.T) {
	t.Parallel()
	// An invalid payload is refused by the handler, not by the registry's
	// "older version" answer.
	payload, _ := json.Marshal(daemon.UserUploadBeginRequest{})
	_, err := DefaultRegistry().Handle(context.Background(), daemon.UserUploadBeginCommand, payload)
	if err == nil || strings.Contains(err.Error(), "doesn't support") {
		t.Fatalf("%s: %v", daemon.UserUploadBeginCommand, err)
	}
}
