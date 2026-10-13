// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// DaemonService is the `service` every daemon line carries, so a shared log
// stream can be split by process kind.
const DaemonService = "reliant-daemon"

// DaemonFields are the fields stamped on every daemon log line. An empty field
// is omitted: a daemon that has not registered yet has no daemon_id.
//
// EVERY field in a daemon log line is untrusted. The customer has a shell in
// the workspace pod, so anything that reaches stdout can be written by them,
// including a line that claims to be this daemon's. These fields are for
// correlation only: daemon_id is self-reported so a line can be joined with the
// gateway's server-side logs, which hold the authoritative record. Tenant
// identity (workspace, org, user) is deliberately not here: the log collector
// attaches it from the pod's labels, which only the operator can set, and
// discards any same-named key found in the line.
type DaemonFields struct {
	DaemonID string
	Version  string
}

// DaemonLogConfig is how one daemon process wants its logger built.
type DaemonLogConfig struct {
	// Rotation is the log file every line is written to. Nil or an unopenable
	// path falls back to stdout alone: a daemon must never run with no log.
	Rotation *RotationConfig
	// Stdout adds the process's stdout as a second destination.
	Stdout bool
	// Output is what "stdout" means. Nil is os.Stdout; tests substitute a buffer.
	Output io.Writer
	// Managed marks a hosted daemon (a workspace pod). Its logs are read by us
	// through the node log agent, so JSON is the default format, and the
	// logging variables are removed from the process environment once read:
	// the daemon's children inherit its environment, and a customer's
	// `forge env up` must not start emitting JSON because the pod asked for it.
	Managed bool
	// Fields seeds the correlation fields stamped on every line.
	Fields DaemonFields
	// Settings is the configured level and format; see Settings.
	Settings Settings
}

// DaemonLogLevel is the level a daemon logs at, and whether that level was
// explicitly requested with RELIANT_LOG_LEVEL (s.Level).
//
// An explicit request is honoured in every environment, DEBUG included: the
// daemon's logs are for us, the redactor sits in front of every line, and
// "prod" is what a laptop and a hosted pod both resolve to when nothing is
// set, so the servers' prod floor would make DEBUG unreachable exactly where
// it is needed. Without an explicit request the default is INFO, and the
// ambient DEBUG=true flags stay capped in prod: those are set in people's
// shells for other tools and say nothing about the daemon.
func DaemonLogLevel(s Settings) (level slog.Level, explicit bool) {
	if level, ok := explicitLevel(s); ok {
		return level, true
	}
	return GetLogLevel(s), false
}

// SetupDaemon installs the process logger for the tools daemon: the level and
// format from cfg.Settings, secrets masked and sizes bounded, and the
// daemon's correlation fields on every line (plus trace_id/span_id for a record logged
// with a span in its context).
func SetupDaemon(cfg DaemonLogConfig) {
	level, explicit := DaemonLogLevel(cfg.Settings)

	fields := cfg.Fields
	daemonFields.Store(&fields)

	output := daemonOutput(cfg)
	if cfg.Managed {
		notices.Store(&noticeSink{w: lineLogger{}})
	} else {
		notices.Store(&noticeSink{w: os.Stdout})
	}
	install(output, cfg.Settings, level,
		func(c *installConfig) {
			c.defaultJSON = cfg.Managed
			c.allowProdDebug = explicit
			c.redact = true
			c.fields = &fields
		})

	if cfg.Managed {
		// Already read, into cfg.Settings, by the entrypoint's config load; see
		// DaemonLogConfig.Managed for why they must not stay.
		for _, name := range []string{logFormatEnv, logLevelEnv} {
			_ = os.Unsetenv(name)
		}
	}
}

// daemonOutput opens the rotating file and joins it with stdout when asked.
func daemonOutput(cfg DaemonLogConfig) io.Writer {
	var file io.Writer
	if cfg.Rotation != nil && cfg.Rotation.Filename != "" {
		if err := os.MkdirAll(dirOf(cfg.Rotation.Filename), 0o755); err != nil {
			slog.Warn("Failed to create log directory, logging to stdout", "dir", dirOf(cfg.Rotation.Filename), "error", err)
		} else {
			activeLogger = newRotator(cfg.Rotation)
			file = activeLogger
		}
	}
	stdout := cfg.Output
	if stdout == nil {
		stdout = os.Stdout
	}
	switch {
	case file == nil:
		DefaultOutput = stdout
	case cfg.Stdout:
		DefaultOutput = &teeWriter{writers: []io.Writer{stdout, file}}
	default:
		DefaultOutput = file
	}
	return DefaultOutput
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[:i]
	}
	return "."
}

// SetDaemonID binds the gateway-assigned daemon id to every line logged from
// now on; "" clears it (the daemon set an id aside and is registering anew).
// A no-op before SetupDaemon.
func SetDaemonID(id string) {
	for {
		cur := daemonFields.Load()
		if cur == nil {
			return
		}
		next := *cur
		next.DaemonID = id
		if daemonFields.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// daemonFields is the live identity. The handler reads it per record rather
// than capturing it, because the daemon id arrives after the logger is built
// (the gateway assigns it at registration).
var daemonFields atomic.Pointer[DaemonFields]

// teeWriter writes every line to each destination and keeps going when one
// fails. io.MultiWriter stops at the first error, so a closed stdout pipe
// would also stop the file from receiving the line.
type teeWriter struct {
	mu      sync.Mutex
	writers []io.Writer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var firstErr error
	for _, w := range t.writers {
		if _, err := w.Write(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return len(p), nil
}

// identityHandler stamps the daemon's identity on each record.
type identityHandler struct{ next slog.Handler }

func newIdentityHandler(next slog.Handler, _ *DaemonFields) slog.Handler {
	return &identityHandler{next: next}
}

func (h *identityHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *identityHandler) Handle(ctx context.Context, r slog.Record) error {
	f := daemonFields.Load()
	if f == nil {
		return h.next.Handle(ctx, r)
	}
	present := map[string]bool{}
	r.Attrs(func(a slog.Attr) bool {
		present[a.Key] = true
		return true
	})
	add := func(key, value string) {
		if value != "" && !present[key] {
			r.AddAttrs(slog.String(key, value))
		}
	}
	add("service", DaemonService)
	add("version", f.Version)
	add("daemon_id", f.DaemonID)
	return h.next.Handle(ctx, r)
}

func (h *identityHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &identityHandler{next: h.next.WithAttrs(attrs)}
}

func (h *identityHandler) WithGroup(name string) slog.Handler {
	return &identityHandler{next: h.next.WithGroup(name)}
}

// notices is where the daemon's human status lines go (the "Daemon connected"
// banner). On a person's terminal that is stdout. On a hosted pod there is no
// person, stdout is a JSON stream, and a raw line in it breaks every parser
// that reads the stream; stderr is no better, because the node log agent tags
// all of stderr ERROR. So a hosted daemon's notices become INFO log records.
var notices atomic.Pointer[noticeSink]

type noticeSink struct{ w io.Writer }

// Notices is the writer for the daemon's human status lines.
func Notices() io.Writer {
	if n := notices.Load(); n != nil {
		return n.w
	}
	return os.Stdout
}

// lineLogger logs each non-blank line written to it as one INFO record.
type lineLogger struct{}

func (lineLogger) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			slog.Info("daemon notice", "notice", line)
		}
	}
	return len(p), nil
}
