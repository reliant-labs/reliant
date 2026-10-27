// Copyright (c) 2025 Reliant Labs

// Package shellaudit keeps a durable, per-machine record of every command the
// shell tool is asked to run: the command line, which chat and thread asked,
// where, and how it ended. It exists for investigations of shared-state damage.
// On 2026-10-26 something wiped the machine's whole Go build cache twice and
// nothing recorded who, so the only evidence was file birth times.
//
// One JSON object per line, in a rotated file beside the daemon's other logs.
// Secrets are redacted before anything is written; a command line is bounded.
//
//forge:exclude-contract: one append-only log writer and a redaction function; the sink is an io.Writer and the tests drive it through that
package shellaudit

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gopkg.in/natefinch/lumberjack.v2"
)

// FileName is the audit log's name inside the daemon's logs directory.
const FileName = "shell-audit.log"

// MaxCommandBytes bounds the command text of one entry. A heredoc that writes a
// file is a command line too; the head of it says what was asked.
const MaxCommandBytes = 4096

// Outcome says how a shell invocation ended.
type Outcome string

const (
	// OutcomeCompleted: the command ran to an exit.
	OutcomeCompleted Outcome = "completed"
	// OutcomeRefused: a guard refused it; it never reached the machine.
	OutcomeRefused Outcome = "refused"
	// OutcomeBackgrounded: it was started in the background (or moved there);
	// its exit is not known here.
	OutcomeBackgrounded Outcome = "backgrounded"
	// OutcomeError: dispatch failed before an exit status existed.
	OutcomeError Outcome = "error"
)

// Entry is one shell invocation.
type Entry struct {
	Time       time.Time
	ChatID     string
	Thread     string
	Cwd        string
	Command    string
	Outcome    Outcome
	Refusal    string // the guard that refused it, for OutcomeRefused
	ExitCode   *int   // nil when the command did not run to an exit
	DurationMs int64
	Background bool
	ProcessID  string
	TimedOut   bool
}

type auditRecord struct {
	Time         string `json:"time"`
	ChatID       string `json:"chat_id,omitempty"`
	Thread       string `json:"thread,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Command      string `json:"command"`
	CommandBytes int    `json:"command_bytes,omitempty"`
	Outcome      string `json:"outcome"`
	Refusal      string `json:"refusal,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	Background   bool   `json:"background,omitempty"`
	ProcessID    string `json:"process_id,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
}

// Log writes entries to a sink. A nil *Log discards them, so a caller needs no
// special case where auditing is not configured.
type Log struct {
	mu     sync.Mutex
	w      io.Writer
	closer io.Closer
}

// New audits to w.
func New(w io.Writer) *Log { return &Log{w: w} }

// Options sizes the rotated file.
type Options struct {
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
}

// Open audits to a rotated file at path, created 0600 inside a 0700 directory:
// a command line can hold anything its author typed, and the file is for the
// machine's operator.
func Open(path string, o Options) (*Log, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// lumberjack creates the file 0600 when it does not exist; touch it first so
	// the mode holds for a file that already exists too.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	if o.MaxSizeMB <= 0 {
		o.MaxSizeMB = 20
	}
	if o.MaxBackups <= 0 {
		o.MaxBackups = 5
	}
	if o.MaxAgeDays <= 0 {
		o.MaxAgeDays = 60
	}
	lj := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    o.MaxSizeMB,
		MaxBackups: o.MaxBackups,
		MaxAge:     o.MaxAgeDays,
		Compress:   true,
		LocalTime:  false,
	}
	return &Log{w: lj, closer: lj}, nil
}

// Enabled reports whether entries are being written anywhere.
func (l *Log) Enabled() bool { return l != nil && l.w != nil }

// Close flushes and closes a file opened with Open.
func (l *Log) Close() error {
	if l == nil || l.closer == nil {
		return nil
	}
	return l.closer.Close()
}

// Record writes one entry. It never fails the caller: an audit log that cannot
// be written must not stop the work it describes.
func (l *Log) Record(e Entry) {
	if !l.Enabled() {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	cmd := Redact(e.Command)
	r := auditRecord{
		Time:       e.Time.UTC().Format(time.RFC3339Nano),
		ChatID:     e.ChatID,
		Thread:     e.Thread,
		Cwd:        e.Cwd,
		Command:    cmd,
		Outcome:    string(e.Outcome),
		Refusal:    e.Refusal,
		ExitCode:   e.ExitCode,
		DurationMs: e.DurationMs,
		Background: e.Background,
		ProcessID:  e.ProcessID,
		TimedOut:   e.TimedOut,
	}
	if len(cmd) > MaxCommandBytes {
		r.Command = truncateUTF8(cmd, MaxCommandBytes) + "…[truncated]"
		r.CommandBytes = len(e.Command)
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(line)
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

const redacted = "[redacted]"

var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Authorization: Bearer x / Basic x / x, in a header argument or a bare word.
	{regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|basic|token|digest)?\s*)[^\s"']+`), "${1}" + redacted},
	{regexp.MustCompile(`(?i)(proxy-authorization\s*[:=]\s*(?:bearer|basic)?\s*)[^\s"']+`), "${1}" + redacted},
	// --password=x / --password x / --api-key=x / --token "x y": a flag whose
	// name ends in a secret word.
	{regexp.MustCompile(`(?i)(--?[A-Za-z0-9_-]*(?:password|passwd|token|secret|api-?key|apikey|access-?key|private-?key|credentials?)\s*[= ]\s*)("[^"]*"|'[^']*'|[^\s"']+)`), "${1}" + redacted},
	// NAME_TOKEN=x, NAME_KEY=x, NAME_SECRET=x (also after export / -e / --env).
	{regexp.MustCompile(`(\b[A-Za-z0-9_]*(?:_TOKEN|_KEY|_SECRET|_PASSWORD|_PASSWD|_CREDENTIALS?)|\b(?:TOKEN|SECRET|PASSWORD|PASSWD|APIKEY|API_KEY))=("[^"]*"|'[^']*'|[^\s"']+)`), "${1}=" + redacted},
	// user:password@host in a URL.
	{regexp.MustCompile(`(\b[a-z][a-z0-9+.-]*://[^\s/:@"']+:)[^\s/@"']+(@)`), "${1}" + redacted + "${2}"},
	// Well-known key shapes, wherever they appear.
	{regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|(?:rlat|dpat|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_\-][A-Za-z0-9_\-]{10,}|AIza[0-9A-Za-z_\-]{20,}|ya29\.[0-9A-Za-z_\-]+|AKIA[0-9A-Z]{16})`), redacted},
}

// Redact removes obvious secrets from a command line: Authorization headers,
// --password/--token style flags, NAME_TOKEN/_KEY/_SECRET assignments,
// credentials in URLs, and well-known key shapes. It keeps the rest of the line
// readable. It is a net for the common cases, not a guarantee.
func Redact(s string) string {
	for _, p := range redactions {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

var defaultLog atomic.Pointer[Log]

// SetDefault installs the process-wide audit log and returns the previous one
// (nil for none), so a test can restore it. The daemon installs one at start.
func SetDefault(l *Log) *Log { return defaultLog.Swap(l) }

// Default is the process-wide audit log; nil discards.
func Default() *Log { return defaultLog.Load() }
