// Package cmdkit is the paved path for forge's non-server binary shapes:
// CLI subcommands, one-shot admin tools, and standalone long-running
// binaries that ship in the same image as the Connect server but are not
// themselves Connect services.
//
// serverkit owns the server lifecycle (listener, readiness flip, worker
// supervision, graceful shutdown). Everything else — a `report`
// subcommand, a queue drainer, a backfill job, a reverse proxy — got
// nothing, so each one re-invented the same things by hand and
// inconsistently: a timeout literal, a freshly-built slog.Logger pointed
// at the wrong stream, hand-rolled DB open/ping, and fmt.Println for
// output. cmdkit centralizes exactly those four.
//
// It deliberately centralizes NO input resolution. A command's inputs come
// from the generated typed config (config.Load) and its own flags; a helper
// here that took a variable NAME and read the process environment would let
// a command configure itself from a value the app declares nowhere — the
// exact thing the typed loader exists to prevent — while satisfying every
// lint, because the read would live in this package instead of the
// command's.
//
// Design constraints:
//
//   - Config-agnostic. The typed config struct (config.Config) is
//     generated per-project in the *project's* module, so cmdkit cannot
//     import it. Helpers take plain values (a DSN, a duration) or small
//     option structs. A command loads config.Load(cmd) itself and passes
//     the fields it needs — config stays the single typed source, cmdkit
//     stays reusable across every project.
//   - Driver-agnostic. OpenDB takes the database/sql driver name; cmdkit
//     never imports a driver, so a project picks pgx / sqlite / etc. and
//     blank-imports it as it already does.
//   - stdout is for data, stderr is for diagnostics. Logger writes to
//     stderr; PrintJSON writes machine-consumable output to stdout. A
//     command's structured result can be piped to jq while its log lines
//     stay out of the pipe.
package cmdkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// LoggerOptions configures Logger. The zero value is a usable default:
// info level, JSON format, writing to stderr.
type LoggerOptions struct {
	// Name, when non-empty, is attached as a "cmd" attribute on every
	// record so log lines from different subcommands are distinguishable
	// in aggregate.
	Name string
	// Level is the minimum slog level. Zero value is slog.LevelInfo.
	Level slog.Level
	// Format selects the handler: "text" emits the text handler, any
	// other value (including "") emits JSON. Mirrors serverkit.Config's
	// LogFormat semantics so a server and its CLI tools log alike.
	Format string
	// Out is the destination. Zero value is os.Stderr — diagnostics
	// belong on stderr so stdout stays clean for piped data.
	Out io.Writer
}

// Logger builds a *slog.Logger for a CLI command or standalone binary.
//
// It is the one-line replacement for the ad-hoc
// slog.New(slog.NewJSONHandler(os.Stderr, nil)) that every command was
// rebuilding — and it fixes the recurring bug of logging to os.Stdout,
// which corrupts piped data output. Pass the binary/subcommand name so
// records carry a "cmd" attribute.
//
// To honour a project's config, source Level/Format from config.Load:
//
//	cfg, _ := config.Load(cmd)
//	log := cmdkit.Logger(cmdkit.LoggerOptions{
//	    Name:   "pnl-report",
//	    Format: cfg.LogFormat,
//	    Level:  cmdkit.ParseLevel(cfg.LogLevel),
//	})
func Logger(opts LoggerOptions) *slog.Logger {
	out := opts.Out
	if out == nil {
		out = os.Stderr
	}
	handlerOpts := &slog.HandlerOptions{Level: opts.Level}
	var handler slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		handler = slog.NewTextHandler(out, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(out, handlerOpts)
	}
	logger := slog.New(handler)
	if opts.Name != "" {
		logger = logger.With("cmd", opts.Name)
	}
	return logger
}

// ParseLevel converts a config log-level string ("debug", "info",
// "warn", "error", case-insensitive) to a slog.Level. Unrecognized or
// empty values map to slog.LevelInfo — a CLI tool should stay loud
// enough to be useful rather than silently dropping to a stricter level
// on a typo.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ContextWithLogger builds a Logger from opts and stores it on ctx via
// observe.WithLogger, returning both. Downstream code recovers it with
// observe.FromContext(ctx) (re-exported here as LoggerFromContext) — no
// extra parameter threaded through call signatures.
func ContextWithLogger(ctx context.Context, opts LoggerOptions) (context.Context, *slog.Logger) {
	logger := Logger(opts)
	return observe.WithLogger(ctx, logger), logger
}

// LoggerFromContext returns the request/command-scoped logger stored on
// ctx, or slog.Default() if none. It is a thin re-export of
// observe.FromContext so a command package depends on cmdkit alone.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	return observe.FromContext(ctx)
}

// ContextWithTimeout derives a timeout context from parent. When d is
// non-positive the fallback is used instead — so a command can pass a
// config-sourced duration that defaults to zero and still get a sane
// bound without an inline `if d == 0` at every call site. It replaces
// the scattered context.WithTimeout(cmd.Context(), 60*time.Second)
// literals that disagreed across commands.
//
// parent may be nil; context.Background() is substituted.
func ContextWithTimeout(parent context.Context, d, fallback time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if d <= 0 {
		d = fallback
	}
	if d <= 0 {
		// Both zero: no deadline, but still return a cancel func so the
		// call site's `defer cancel()` is always valid.
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}

// PrintJSON writes v as indented JSON followed by a newline to w. It is
// the replacement for hand-rolled json.NewEncoder(os.Stdout) +
// SetIndent blocks and for fmt.Println-based output. Write structured
// results to os.Stdout so they pipe cleanly to jq; keep human-facing
// chatter on the Logger (stderr).
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}
