// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFileFixtures are the byte patterns a file reader must round-trip
// unchanged. Every one of them was corrupted by the bufio.Scanner reader:
// ScanLines drops the terminator from each token and dropCR strips \r, so
// strings.Join(lines, "\n") could never reproduce the input.
var readFileFixtures = []struct {
	name string
	body string
}{
	{"trailing newline", "package main\n\nfunc main() {}\n"},
	{"no trailing newline", "package main\n\nfunc main() {}"},
	{"empty file", ""},
	{"only a newline", "\n"},
	{"blank line at eof", "a\nb\n\n"},
	{"crlf", "line one\r\nline two\r\n"},
	{"crlf no trailing newline", "line one\r\nline two"},
	{"long line over 64KB", strings.Repeat("x", 200*1024) + "\n"},
	{"long line over 1MB no newline", strings.Repeat("y", 2*1024*1024)},
}

// TestReadFileIsByteExact is the core contract: FileContent.Content for an
// unbounded read is the file's bytes, verbatim.
func TestReadFileIsByteExact(t *testing.T) {
	c := NewLocalClient()
	for _, tc := range readFileFixtures {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			fc, err := c.ReadFile(context.Background(), path, nil)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if fc.Content != tc.body {
				t.Errorf("content not byte-exact:\n  want %q\n  got  %q",
					clip(tc.body), clip(fc.Content))
			}
			if fc.Size != int64(len(tc.body)) {
				t.Errorf("Size = %d, want %d", fc.Size, len(tc.body))
			}
		})
	}
}

// TestReadFileTotalLines pins the line-count semantics: a line is a run of
// bytes ending at a \n, plus any trailing fragment. "a\nb\n" and "a\nb" are
// both two lines.
func TestReadFileTotalLines(t *testing.T) {
	cases := []struct {
		body string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"\n", 1},
		{"a\n\n", 2},
	}
	c := NewLocalClient()
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "f.txt")
		if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		fc, err := c.ReadFile(context.Background(), path, nil)
		if err != nil {
			t.Fatalf("%q: %v", tc.body, err)
		}
		if fc.TotalLines != tc.want {
			t.Errorf("%q: TotalLines = %d, want %d", tc.body, fc.TotalLines, tc.want)
		}
	}
}

// TestReadFileWindowsAreByteExact checks the offset/limit slice. Each window
// must be the exact bytes of those lines in the file, terminators included, so
// concatenating consecutive windows rebuilds the file.
func TestReadFileWindowsAreByteExact(t *testing.T) {
	const body = "one\ntwo\nthree\nfour\nfive\n"
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewLocalClient()

	cases := []struct {
		offset, limit int
		want          string
		truncated     bool
	}{
		{0, 2, "one\ntwo\n", true},
		{2, 2, "three\nfour\n", true},
		{4, 2, "five\n", false},
		{0, 5, body, false},
		{0, 0, body, false},
		{9, 2, "", false},
	}
	for _, tc := range cases {
		fc, err := c.ReadFile(context.Background(), path, &ReadFileOpts{Offset: tc.offset, Limit: tc.limit})
		if err != nil {
			t.Fatalf("offset=%d limit=%d: %v", tc.offset, tc.limit, err)
		}
		if fc.Content != tc.want {
			t.Errorf("offset=%d limit=%d: content = %q, want %q", tc.offset, tc.limit, fc.Content, tc.want)
		}
		if fc.Truncated != tc.truncated {
			t.Errorf("offset=%d limit=%d: Truncated = %v, want %v", tc.offset, tc.limit, fc.Truncated, tc.truncated)
		}
		if fc.TotalLines != 5 {
			t.Errorf("offset=%d limit=%d: TotalLines = %d, want 5", tc.offset, tc.limit, fc.TotalLines)
		}
	}

	// Windowed reads must tile the file exactly.
	var rebuilt strings.Builder
	for off := 0; off < 5; off += 2 {
		fc, err := c.ReadFile(context.Background(), path, &ReadFileOpts{Offset: off, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		rebuilt.WriteString(fc.Content)
	}
	if rebuilt.String() != body {
		t.Errorf("tiled windows = %q, want %q", rebuilt.String(), body)
	}
}

func clip(s string) string {
	if len(s) > 120 {
		return s[:120] + "...(" + itoa(len(s)) + " bytes)"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
