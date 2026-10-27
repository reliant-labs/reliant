// Copyright (c) 2025 Reliant Labs
package shellaudit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func record(t *testing.T, e Entry) (line string, got map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	New(&buf).Record(e)
	line = buf.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("an entry is exactly one line, got %q", line)
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, line)
	}
	return line, got
}

func TestRecordWritesTheInvestigationFields(t *testing.T) {
	t.Parallel()
	exit := 7
	_, got := record(t, Entry{
		Time:       time.Date(2026, 10, 26, 17, 55, 40, 0, time.UTC),
		ChatID:     "chat-123",
		Thread:     "thread-abc",
		Cwd:        "/home/workspace/projects/p",
		Command:    "go clean -i ./mypkg\nls",
		Outcome:    OutcomeCompleted,
		ExitCode:   &exit,
		DurationMs: 1234,
	})
	want := map[string]any{
		"chat_id":     "chat-123",
		"thread":      "thread-abc",
		"cwd":         "/home/workspace/projects/p",
		"command":     "go clean -i ./mypkg\nls",
		"outcome":     "completed",
		"exit_code":   float64(7),
		"duration_ms": float64(1234),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v (entry %v)", k, got[k], v, got)
		}
	}
	if got["time"] != "2026-10-26T17:55:40Z" {
		t.Errorf("time = %v", got["time"])
	}
}

func TestRecordOmitsTheExitCodeOfACommandThatNeverRan(t *testing.T) {
	t.Parallel()
	_, got := record(t, Entry{Command: "go clean -cache", Outcome: OutcomeRefused, Refusal: "shared_state"})
	if _, ok := got["exit_code"]; ok {
		t.Fatalf("a refused command has no exit code: %v", got)
	}
	if got["refusal"] != "shared_state" || got["outcome"] != "refused" {
		t.Fatalf("refusal not recorded: %v", got)
	}
}

func TestRecordRedactsSecrets(t *testing.T) {
	t.Parallel()
	secrets := []struct{ name, command, secret string }{
		{"password equals", "mysql --host=db --password=hunter2hunter2 -e 'select 1'", "hunter2hunter2"},
		{"password space", "psql --password s3cr3tpassw0rd -c 'select 1'", "s3cr3tpassw0rd"},
		{"quoted password", `tool --password "correct horse battery"`, "correct horse battery"},
		{"token flag", "gh api --token abc123def456ghi", "abc123def456ghi"},
		{"authorization bearer", `curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig" https://x`, "eyJhbGciOiJIUzI1NiJ9"},
		{"authorization lowercase", `curl -H 'authorization: Basic dXNlcjpwYXNz' https://x`, "dXNlcjpwYXNz"},
		{"authorization bare", `curl -H "Authorization: tok_0123456789" https://x`, "tok_0123456789"},
		{"token env", "GITHUB_TOKEN=ghp_0123456789abcdefghij0123456789abcdef git push", "ghp_0123456789abcdefghij0123456789abcdef"},
		{"key env", "OPENAI_API_KEY=sk-live-notarealkey1234 python run.py", "sk-live-notarealkey1234"},
		{"secret env", "export CLIENT_SECRET='a b c d e f' && run", "a b c d e f"},
		{"docker -e", "docker run -e DB_PASSWORD=pa55word-xyz img", "pa55word-xyz"},
		{"url userinfo", "git clone https://user:pa55w0rdvalue@github.com/x/y.git", "pa55w0rdvalue"},
		{"token shape in argument", "echo AKIAABCDEFGHIJKLMNOP", "AKIAABCDEFGHIJKLMNOP"},
	}
	for _, tc := range secrets {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line, got := record(t, Entry{Command: tc.command, Outcome: OutcomeCompleted})
			if strings.Contains(line, tc.secret) {
				t.Fatalf("secret %q written to the log: %s", tc.secret, line)
			}
			if !strings.Contains(got["command"].(string), "[redacted]") {
				t.Fatalf("no redaction marker in %q", got["command"])
			}
		})
	}
}

func TestRecordKeepsTheRestOfARedactedCommandReadable(t *testing.T) {
	t.Parallel()
	_, got := record(t, Entry{Command: "GITHUB_TOKEN=ghp_0123456789abcdefghij0123456789abcdef git push origin main", Outcome: OutcomeCompleted})
	if want := "GITHUB_TOKEN=[redacted] git push origin main"; got["command"] != want {
		t.Fatalf("command = %q, want %q", got["command"], want)
	}
	_, got = record(t, Entry{Command: `curl -H "Authorization: Bearer abcdefghijklmnop" https://x`, Outcome: OutcomeCompleted})
	if want := `curl -H "Authorization: Bearer [redacted]" https://x`; got["command"] != want {
		t.Fatalf("command = %q, want %q", got["command"], want)
	}
}

func TestRecordLeavesOrdinaryCommandsAlone(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"go test -count=1 ./internal/...",
		"GOFLAGS=-mod=mod go build ./...",
		"SSH_KEY_PATH=/home/u/.ssh/id git fetch",
		"git commit -m 'rotate the key and the token handling'",
		"rg -n 'password' internal/",
		"echo monkey=banana",
	} {
		_, got := record(t, Entry{Command: command, Outcome: OutcomeCompleted})
		if got["command"] != command {
			t.Errorf("command %q was changed to %q", command, got["command"])
		}
	}
}

func TestRecordTruncatesLongCommandsAfterRedacting(t *testing.T) {
	t.Parallel()
	// The secret straddles the cut: redacting after truncating would leave half
	// of it in the log.
	command := strings.Repeat("x", MaxCommandBytes-10) + " GITHUB_TOKEN=ghp_0123456789abcdefghij0123456789abcdef"
	line, got := record(t, Entry{Command: command, Outcome: OutcomeCompleted})
	if strings.Contains(line, "ghp_0123") {
		t.Fatalf("part of a secret survived truncation: %s", line)
	}
	cmd := got["command"].(string)
	if len(cmd) > MaxCommandBytes+64 {
		t.Fatalf("command not truncated: %d bytes", len(cmd))
	}
	if !strings.Contains(cmd, "truncated") {
		t.Fatalf("truncation not marked: %q", cmd[len(cmd)-80:])
	}
	if got["command_bytes"] != float64(len(command)) {
		t.Fatalf("command_bytes = %v, want the original length %d", got["command_bytes"], len(command))
	}
}

func TestTruncationNeverSplitsARune(t *testing.T) {
	t.Parallel()
	command := strings.Repeat("é", MaxCommandBytes) // 2 bytes each
	line, got := record(t, Entry{Command: command})
	if !json.Valid([]byte(line)) {
		t.Fatal("invalid JSON")
	}
	if strings.ContainsRune(got["command"].(string), '\uFFFD') {
		t.Fatal("a rune was split by the cut")
	}
}

func TestNoSinkIsANoOp(t *testing.T) {
	t.Parallel()
	var l *Log
	l.Record(Entry{Command: "ls"}) // must not panic
	if l.Enabled() {
		t.Fatal("a nil log reports itself enabled")
	}
}

func TestConfigureWritesAndRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "shell-audit.log")
	l, err := Open(path, Options{MaxSizeMB: 1, MaxBackups: 2, MaxAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Record(Entry{ChatID: "c1", Command: "echo first", Outcome: OutcomeCompleted})
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"echo first"`) {
		t.Fatalf("not written: %v %q", err, data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the audit log is readable by others: %v", info.Mode())
	}

	// ~2.5 MB through a 1 MB limit must roll the file over.
	filler := strings.Repeat("a", 1000)
	for range 2500 {
		l.Record(Entry{ChatID: "c1", Command: "echo " + filler, Outcome: OutcomeCompleted})
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never rotated: %v", entries)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
