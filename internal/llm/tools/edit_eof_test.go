// Copyright (c) 2025 Reliant Labs
package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// editFixture writes body to a temp file and returns the path plus a ready
// tool context. Every test here drives the real edit tool against a real
// LocalClient daemon, so it exercises the same read → splice → write path a
// live tool call takes.
func editFixture(t *testing.T, body string) (string, *editTool, func(EditParams) string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "server.go")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	chatID, thread := "eof-chat-"+t.Name(), "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })
	tool := &editTool{}

	run := func(p EditParams) string {
		t.Helper()
		p.FilePath = path
		resp, err := tool.Execute(newTestToolContext(t, dir, chatID, thread), p)
		require.NoError(t, err)
		if resp.IsError {
			return "ERROR: " + resp.Content
		}
		return ""
	}
	return path, tool, run
}

func readRaw(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// TestEditOldStringEndingAtEOFMatches reproduces the live-run failure: the
// agent issued this exact edit four times (03:57:03, 03:57:57, 03:59:14,
// 03:59:51) and got "old_string not found in file" every time, because the
// reader had already thrown away the file's final newline. Deleting or
// replacing the LAST declaration in a file is what "split this file" work
// produces, so this fires on every run that reorganizes code.
func TestEditOldStringEndingAtEOFMatches(t *testing.T) {
	const body = `package api

func (s *Server) ListOrders(ctx context.Context, req *pb.ListOrdersRequest) (*pb.ListOrdersResponse, error) {
	return crud.HandleList(s.crudListOrdersOp())(ctx, req)
}
`
	path, _, run := editFixture(t, body)

	// old_string spans to EOF and includes the file's final newline.
	oldString := "func (s *Server) ListOrders(ctx context.Context, req *pb.ListOrdersRequest) (*pb.ListOrdersResponse, error) {\n\treturn crud.HandleList(s.crudListOrdersOp())(ctx, req)\n}\n"

	if got := run(EditParams{OldString: oldString, NewString: ""}); got != "" {
		t.Fatalf("edit whose old_string ends at EOF was rejected: %s", got)
	}
	if got := readRaw(t, path); got != "package api\n\n" {
		t.Errorf("after delete: got %q, want %q", got, "package api\n\n")
	}
}

// TestEditPreservesTrailingNewline covers consequence 2: an edit anywhere in
// the file used to rewrite it without its final newline, which git renders as
// "\ No newline at end of file" on every touched .tsx/.sql/.yaml/.md. (Go hid
// it because agents run gofmt -w afterwards.)
func TestEditPreservesTrailingNewline(t *testing.T) {
	const body = "alpha\nbravo\ncharlie\n"
	path, _, run := editFixture(t, body)

	if got := run(EditParams{OldString: "bravo", NewString: "BRAVO"}); got != "" {
		t.Fatalf("mid-file edit failed: %s", got)
	}
	want := "alpha\nBRAVO\ncharlie\n"
	if got := readRaw(t, path); got != want {
		t.Errorf("after mid-file edit: got %q, want %q", got, want)
	}
}

// TestEditDoesNotAddTrailingNewline is the other half of the invariant: a file
// that never ended in a newline must not gain one. The fix must be exactness,
// not a helpful newline-appender.
func TestEditDoesNotAddTrailingNewline(t *testing.T) {
	const body = "alpha\nbravo\ncharlie"
	path, _, run := editFixture(t, body)

	if got := run(EditParams{OldString: "bravo", NewString: "BRAVO"}); got != "" {
		t.Fatalf("mid-file edit failed: %s", got)
	}
	want := "alpha\nBRAVO\ncharlie"
	if got := readRaw(t, path); got != want {
		t.Errorf("after mid-file edit: got %q, want %q", got, want)
	}
}

// TestEditPreservesCRLF: bufio.ScanLines strips a trailing \r from every token
// (dropCR), so a one-word edit to a CRLF file silently rewrote every line in
// the file as LF — a whole-file diff for a three-character change.
func TestEditPreservesCRLF(t *testing.T) {
	const body = "alpha\r\nbravo\r\ncharlie\r\n"
	path, _, run := editFixture(t, body)

	if got := run(EditParams{OldString: "bravo", NewString: "BRAVO"}); got != "" {
		t.Fatalf("mid-file edit failed: %s", got)
	}
	want := "alpha\r\nBRAVO\r\ncharlie\r\n"
	if got := readRaw(t, path); got != want {
		t.Errorf("after mid-file edit: got %q, want %q", got, want)
	}
}

// TestEditReplaceLastDeclaration is the replace (not delete) form of the same
// EOF bug, and the shape "move this function to another file" actually takes.
func TestEditReplaceLastDeclaration(t *testing.T) {
	const body = "package api\n\nfunc A() {}\n\nfunc B() {}\n"
	path, _, run := editFixture(t, body)

	if got := run(EditParams{OldString: "func B() {}\n", NewString: "func B() int { return 0 }\n"}); got != "" {
		t.Fatalf("replace of last declaration was rejected: %s", got)
	}
	want := "package api\n\nfunc A() {}\n\nfunc B() int { return 0 }\n"
	if got := readRaw(t, path); got != want {
		t.Errorf("after replace: got %q, want %q", got, want)
	}
}

// TestInsertAtPreservesTrailingNewline: insert_at rebuilds the file with
// Split/Join over the same corrupted content, so it dropped the final newline
// too. It must also not let an insert land past the real last line.
func TestInsertAtPreservesTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha\nbravo\ncharlie\n"), 0o644))

	chatID, thread := "insert-chat", "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })

	tool := &insertAtTool{}
	resp, err := tool.Execute(newTestToolContext(t, dir, chatID, thread), InsertAtParams{
		FilePath:   path,
		AnchorText: "charlie",
		Position:   "after",
		Content:    "delta",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "insert_at failed: %s", resp.Content)

	want := "alpha\nbravo\ncharlie\ndelta\n"
	if got := readRaw(t, path); got != want {
		t.Errorf("after insert_at: got %q, want %q", got, want)
	}
}

// TestViewNoPhantomTrailingLine guards the display side of a byte-exact read:
// "a\nb\n" is a two-line file, so view must number two lines, not three. Naive
// strings.Split on exact content would append an empty final element and print
// a bogus numbered blank line at the bottom of every file.
func TestViewNoPhantomTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha\nbravo\n"), 0o644))

	tool := &viewTool{}
	resp, err := tool.Execute(newTestToolContext(t, dir, "view-chat", "0"), ViewParams{FilePath: path})
	require.NoError(t, err)
	require.False(t, resp.IsError, "view failed: %s", resp.Content)

	want := "<file>\n     1|alpha\n     2|bravo\n</file>\n"
	if resp.Content != want {
		t.Errorf("view output:\n  got  %q\n  want %q", resp.Content, want)
	}
}

// TestViewCRLFNotShownAsControlChars: with a byte-exact read the \r reaches
// the formatter, which must strip it for display (addLineNumbers already does)
// without that stripping leaking back into anything that gets written.
func TestViewCRLFRendersClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha\r\nbravo\r\n"), 0o644))

	tool := &viewTool{}
	resp, err := tool.Execute(newTestToolContext(t, dir, "view-crlf-chat", "0"), ViewParams{FilePath: path})
	require.NoError(t, err)
	require.False(t, resp.IsError, "view failed: %s", resp.Content)

	want := "<file>\n     1|alpha\n     2|bravo\n</file>\n"
	if resp.Content != want {
		t.Errorf("view output:\n  got  %q\n  want %q", resp.Content, want)
	}
}

// TestEditLinesPreservesTrailingNewline: edit_lines reported an extra phantom
// line and, on the corrupted content, dropped the final newline on write.
func TestEditLinesPreservesTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha\nbravo\ncharlie\n"), 0o644))

	chatID, thread := "editlines-chat", "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })

	tool := &editLinesTool{}
	ctx := newTestToolContext(t, dir, chatID, thread)

	// The file has exactly 3 lines; line 4 must not exist.
	resp, err := tool.Execute(ctx, EditLinesParams{
		FilePath: path, Operation: "delete", StartLine: 4, EndLine: 4,
	})
	require.NoError(t, err)
	require.True(t, resp.IsError, "delete of nonexistent line 4 should fail, got: %s", resp.Content)

	resp, err = tool.Execute(ctx, EditLinesParams{
		FilePath: path, Operation: "replace", StartLine: 2, EndLine: 2, NewContent: "BRAVO",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "edit_lines replace failed: %s", resp.Content)

	want := "alpha\nBRAVO\ncharlie\n"
	if got := readRaw(t, path); got != want {
		t.Errorf("after edit_lines: got %q, want %q", got, want)
	}
}
