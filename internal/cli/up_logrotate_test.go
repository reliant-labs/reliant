package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// storageRotatedLog is a deliberate duplicate of `rotatedLog` in
// internal/storage/logs.go. Rotated names produced here MUST match it or
// `forge storage gc` will never expire them. Duplicated rather than exported so
// the storage package keeps its surface small; if logs.go's pattern changes,
// this test is the tripwire.
var storageRotatedLog = regexp.MustCompile(`^(.+)\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[^/]+\.log$`)

// logDirNames returns every file in dir, sorted. It is the one place this file
// discovers the directory's contents, so the non-empty check lives here and
// every caller inherits it: the current stream always exists, so an empty dir
// means the writer never created it rather than "no rotation happened".
func logDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("log dir %s is empty: the writer never created the stream", dir)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// rotatedSiblings returns the rotated files in dir for the given stream,
// sorted by name.
func rotatedSiblings(t *testing.T, dir, stream string) []string {
	t.Helper()
	var rotated []string
	for _, name := range logDirNames(t, dir) {
		match := storageRotatedLog.FindStringSubmatch(name)
		if len(match) == 2 && match[1] == stream {
			rotated = append(rotated, name)
		}
	}
	return rotated
}

func TestLogRotateRotatesPastCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.log")
	w, err := newRotatingLogWriter(path, 100)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer w.Close()

	// 8 lines of 20 bytes each = 160 bytes, so exactly one rotation at the
	// first line boundary past 100.
	for i := 0; i < 8; i++ {
		if _, err := fmt.Fprintf(w, "line-%013d\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	rotated := rotatedSiblings(t, dir, "svc")
	if len(rotated) != 1 {
		t.Fatalf("want exactly 1 rotated file, got %d: %v", len(rotated), logDirNames(t, dir))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current stream must still exist at the stable path: %v", err)
	}

	rotatedBody, err := os.ReadFile(filepath.Join(dir, rotated[0]))
	if err != nil {
		t.Fatalf("read rotated: %v", err)
	}
	currentBody, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if len(rotatedBody) < 100 {
		t.Errorf("rotated file should hold at least the cap, got %d bytes", len(rotatedBody))
	}
	if len(currentBody) == 0 {
		t.Error("current file should hold the lines written after rotation")
	}

	// Nothing lost: every line appears exactly once across the two files.
	all := string(rotatedBody) + string(currentBody)
	for i := 0; i < 8; i++ {
		want := fmt.Sprintf("line-%013d\n", i)
		if n := strings.Count(all, want); n != 1 {
			t.Errorf("line %d appears %d times across files, want 1", i, n)
		}
	}
}

func TestLogRotateNeverSplitsALine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.log")
	// Tiny cap: every single write is past it, so rotation is attempted on
	// every chunk. Writing a line in three chunks must not rotate mid-line.
	w, err := newRotatingLogWriter(path, 1)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer w.Close()

	for i := 0; i < 5; i++ {
		for _, chunk := range []string{"AAA", "BBB", fmt.Sprintf("CCC-%d\n", i)} {
			if _, err := w.Write([]byte(chunk)); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, name := range append(rotatedSiblings(t, dir, "svc"), "svc.log") {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(body) == 0 {
			continue
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
			if !regexp.MustCompile(`^AAABBBCCC-\d$`).MatchString(line) {
				t.Errorf("%s: line split across files or corrupted: %q", name, line)
			}
		}
	}
}

func TestLogRotateNameMatchesStorageExpiryPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reliant-api-server.log")
	w, err := newRotatingLogWriter(path, 10)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("0123456789abcdef\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	rotated := rotatedSiblings(t, dir, "reliant-api-server")
	if len(rotated) != 1 {
		t.Fatalf("want 1 rotated file matching storage's rotatedLog, got %d", len(rotated))
	}
	match := storageRotatedLog.FindStringSubmatch(rotated[0])
	if match[1] != "reliant-api-server" {
		t.Errorf("storage would attribute %q to stream %q, want %q", rotated[0], match[1], "reliant-api-server")
	}
	if strings.Contains(rotated[0], ":") {
		t.Errorf("rotated name must be colon-free, got %q", rotated[0])
	}
}

func TestLogRotateConcurrentWritersKeepLinesIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.log")
	rw, err := newRotatingLogWriter(path, 2000)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer rw.Close()
	// The up tee shares one sink between the stdout and stderr goroutines
	// through lockedWriter — exactly this shape.
	sink := &lockedWriter{w: rw}

	const writers, perWriter = 4, 200
	var wg sync.WaitGroup
	for id := 0; id < writers; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				line := fmt.Sprintf("writer-%d-line-%04d\n", id, i)
				if _, err := sink.Write([]byte(line)); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(id)
	}
	wg.Wait()
	if err := rw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	valid := regexp.MustCompile(`^writer-\d-line-\d{4}$`)
	seen := map[string]int{}
	for _, name := range append(rotatedSiblings(t, dir, "svc"), "svc.log") {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(body) == 0 {
			continue
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
			if !valid.MatchString(line) {
				t.Fatalf("%s: interleaved or split line: %q", name, line)
			}
			seen[line]++
		}
	}
	if len(seen) != writers*perWriter {
		t.Errorf("got %d distinct lines, want %d", len(seen), writers*perWriter)
	}
	for line, n := range seen {
		if n != 1 {
			t.Errorf("line %q written %d times, want 1", line, n)
		}
	}
}

func TestUpLogRotateBytesEnvOverride(t *testing.T) {
	t.Setenv(logRotateEnvVar, "4096")
	if got := upLogRotateBytes(); got != 4096 {
		t.Errorf("env override: got %d, want 4096", got)
	}
	t.Setenv(logRotateEnvVar, "not-a-number")
	if got := upLogRotateBytes(); got != defaultLogRotateBytes {
		t.Errorf("malformed value should fall back to the default, got %d", got)
	}
	t.Setenv(logRotateEnvVar, "0")
	if got := upLogRotateBytes(); got != 0 {
		t.Errorf("0 should disable rotation, got %d", got)
	}
	os.Unsetenv(logRotateEnvVar)
	if got := upLogRotateBytes(); got != defaultLogRotateBytes {
		t.Errorf("unset: got %d, want %d", got, defaultLogRotateBytes)
	}
}

func TestLogRotateDisabledKeepsOneFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.log")
	w, err := newRotatingLogWriter(path, 0)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer w.Close()
	for i := 0; i < 100; i++ {
		if _, err := fmt.Fprintf(w, "line-%d\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if rotated := rotatedSiblings(t, dir, "svc"); len(rotated) != 0 {
		t.Errorf("cap 0 must not rotate, got %v", rotated)
	}
}
