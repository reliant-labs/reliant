// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// readFile drives the real fs.read_file handler end-to-end (JSON in, JSON out),
// which is the path every tool call takes against a remote workspace daemon.
func readFile(t *testing.T, path string, opts *daemon.ReadFileOpts) daemon.FileContent {
	t.Helper()
	payload, err := json.Marshal(fsReadFileRequest{Path: path, Opts: opts})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleFSReadFile(context.Background(), payload)
	if err != nil {
		t.Fatalf("fs.read_file: %v", err)
	}
	var fc daemon.FileContent
	if err := json.Unmarshal(out, &fc); err != nil {
		t.Fatal(err)
	}
	return fc
}

// TestFSReadFileIsByteExact is the remote-daemon twin of
// TestReadFileIsByteExact. Both readers must agree, byte for byte, or an edit
// behaves differently depending on where the daemon runs.
func TestFSReadFileIsByteExact(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"trailing newline", "package main\n\nfunc main() {}\n"},
		{"no trailing newline", "package main\n\nfunc main() {}"},
		{"empty file", ""},
		{"only a newline", "\n"},
		{"blank line at eof", "a\nb\n\n"},
		{"crlf", "line one\r\nline two\r\n"},
		{"long line over 64KB", strings.Repeat("x", 200*1024) + "\n"},
		{"long line over 10MB", strings.Repeat("z", 12*1024*1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			fc := readFile(t, path, nil)
			if fc.Content != tc.body {
				got, want := fc.Content, tc.body
				if len(got) > 120 {
					got = got[:120] + "..."
				}
				if len(want) > 120 {
					want = want[:120] + "..."
				}
				t.Errorf("content not byte-exact (len %d, want %d):\n  want %q\n  got  %q",
					len(fc.Content), len(tc.body), want, got)
			}
		})
	}
}

// TestFSReadFileMatchesLocalClient asserts the two implementations of the same
// contract cannot drift: same file, same opts, same FileContent.
func TestFSReadFileMatchesLocalClient(t *testing.T) {
	bodies := []string{
		"one\ntwo\nthree\n",
		"one\ntwo\nthree",
		"",
		"\r\n\r\n",
		strings.Repeat("q", 3*1024*1024) + "\ntail\n",
	}
	optsList := []*daemon.ReadFileOpts{
		nil,
		{Offset: 0, Limit: 2},
		{Offset: 1, Limit: 1},
		{Offset: 5, Limit: 3},
	}
	local := daemon.NewLocalClient()
	for i, body := range bodies {
		path := filepath.Join(t.TempDir(), "f.txt")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		for j, opts := range optsList {
			remote := readFile(t, path, opts)
			lfc, err := local.ReadFile(context.Background(), path, opts)
			if err != nil {
				t.Fatalf("body %d opts %d: LocalClient.ReadFile: %v", i, j, err)
			}
			if remote.Content != lfc.Content {
				t.Errorf("body %d opts %d: content mismatch (remote %d bytes, local %d bytes)",
					i, j, len(remote.Content), len(lfc.Content))
			}
			if remote.TotalLines != lfc.TotalLines {
				t.Errorf("body %d opts %d: TotalLines remote=%d local=%d",
					i, j, remote.TotalLines, lfc.TotalLines)
			}
			if remote.Truncated != lfc.Truncated {
				t.Errorf("body %d opts %d: Truncated remote=%v local=%v",
					i, j, remote.Truncated, lfc.Truncated)
			}
		}
	}
}
