package cli

// Shared plumbing of the `forge obs` agent verbs (logs, traces, trace,
// endpoints, metrics list|query, query): the env -> backend resolution
// (DESIGN §4.2), the time/scope flags every verb takes, and how a verb renders
// a document or a failure. The output contract itself lives in
// internal/obsout; the queries in internal/obsbackend.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// obsDeps are the seams a verb reaches the world through. The zero value is
// the real thing (see defaultObsDeps); tests replace one field.
type obsDeps struct {
	// Facts reads what env's resolved KCL says about where its telemetry goes.
	Facts func(ctx context.Context, errOut io.Writer, env string) (obsbackend.Facts, error)
	// Local builds the backend over the env's local ClickStack.
	Local func(env string) obsbackend.Backend
	// Cloud builds the backend over the env's control plane.
	Cloud func(ctx context.Context, env string) (obsbackend.Cloud, error)
	// Release looks up the release live in env at a moment.
	Release func(ctx context.Context, env string, at time.Time) (*liveRelease, error)
	// Now is the clock.
	Now func() time.Time
	// Stdout and Stderr are the verb's writers.
	Stdout, Stderr io.Writer
}

var obsDepsOverride *obsDeps

func defaultObsDeps(cmd *cobra.Command) obsDeps {
	d := obsDeps{
		Facts: obsFactsFromKCL,
		Local: func(env string) obsbackend.Backend {
			return obsbackend.Local{Env: env, Runner: obsbackend.DockerRunner{Exec: localClickstackExec(env), Env: env}, Ledger: openObsLedger, Declared: declaredMetricSchemas}
		},
		Cloud:   openObsCloud,
		Release: liveReleaseAt,
		Now:     time.Now,
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
	}
	if o := obsDepsOverride; o != nil {
		if o.Facts != nil {
			d.Facts = o.Facts
		}
		if o.Local != nil {
			d.Local = o.Local
		}
		if o.Cloud != nil {
			d.Cloud = o.Cloud
		}
		if o.Release != nil {
			d.Release = o.Release
		}
		if o.Now != nil {
			d.Now = o.Now
		}
		if o.Stdout != nil {
			d.Stdout = o.Stdout
		}
		if o.Stderr != nil {
			d.Stderr = o.Stderr
		}
	}
	// Every verb's output carries telemetry written by whoever holds the public
	// ingest key; this is the one place it is made safe for a terminal.
	d.Stdout = obsout.NewSanitizingWriter(d.Stdout)
	d.Stderr = obsout.NewSanitizingWriter(d.Stderr)
	return d
}

// localClickstackExec runs a script inside env's ClickStack container, in the
// compose project `forge env up` started it in.
func localClickstackExec(env string) obsbackend.ExecFunc {
	return func(ctx context.Context, script string, args []string) ([]byte, error) {
		dir, err := requireProjectDir("obs")
		if err != nil {
			return nil, obsbackend.Undetermined("%v", err)
		}
		project := func(ctx context.Context) []string { return localClickstackComposeArgs(ctx, dir, env) }
		return obsbackend.ComposeExec(obsbackend.ComposeFunc(doctorCompose), dir, project)(ctx, script, args)
	}
}

// obsFactsFromKCL reads what env's declaration says about where its telemetry
// goes. It renders the declaration only when the stamped copy of the last read
// no longer matches (obs_target.go). An env that does not exist is an error that
// lists the ones that do.
func obsFactsFromKCL(ctx context.Context, errOut io.Writer, env string) (obsbackend.Facts, error) {
	t, err := resolveObsTarget(ctx, env)
	if err != nil {
		return obsbackend.Facts{}, obsbackend.Undetermined("cannot read env %q's declaration: %v", env, err).
			With(obsbackend.Cmd("forge", "env", "list"))
	}
	return t.Facts, nil
}

// obsCommon are the flags and state every telemetry verb shares.
type obsCommon struct {
	out      obsout.Flags
	backend  string
	since    time.Duration
	from, to string
	services []string
	filters  []string

	// explain compiles the statements without running them (--explain);
	// printSQL is its alias for the SQL-first habit (--print-sql).
	explain, printSQL bool
}

// register adds the flags. sinceDefault is the verb's own window (logs 15m,
// traces 1h, ...); the page size default is the verb's.
func (c *obsCommon) register(cmd *cobra.Command, sinceDefault time.Duration, limitDefault int) {
	f := cmd.Flags()
	c.out.Register(f, limitDefault)
	f.StringVar(&c.backend, "backend", "", "local | cloud (default: from the env's declaration)")
	f.DurationVar(&c.since, "since", sinceDefault, "how far back to read (ignored with --from)")
	f.StringVar(&c.from, "from", "", "range start, RFC 3339")
	f.StringVar(&c.to, "to", "", "range end, RFC 3339 (default now)")
	f.StringArrayVar(&c.services, "service", nil, "only this service.name (repeatable)")
	f.StringArrayVar(&c.filters, "filter", nil, "attribute predicate key=value or key!=value (repeatable): an allowlisted key, or any attribute as span.attr.<k> (traces), log.attr.<k> (logs) or resource.attr.<k>")
	f.BoolVar(&c.explain, "explain", false, "print the compiled SQL and its parameters instead of running it (a starting point for obs query --sql)")
	f.BoolVar(&c.printSQL, "print-sql", false, "alias of --explain")
}

func (c *obsCommon) wantExplain() bool { return c.explain || c.printSQL }

// narrow resolves the time range, services and filters.
func (c *obsCommon) narrow(now time.Time) (obsbackend.Narrow, error) {
	n := obsbackend.Narrow{Services: c.services, Explain: c.wantExplain()}
	rng, err := resolveRange(now, c.since, c.from, c.to)
	if err != nil {
		return n, err
	}
	n.Range = rng
	for _, s := range c.filters {
		f, err := obsbackend.ParseFilter(s)
		if err != nil {
			return n, err
		}
		n.Filters = append(n.Filters, f)
	}
	return n, nil
}

func resolveRange(now time.Time, since time.Duration, from, to string) (obsbackend.Range, error) {
	end := now.UTC()
	if to != "" {
		t, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return obsbackend.Range{}, obsbackend.Wrong("--to %q is not an RFC 3339 time (2026-10-22T10:00:00Z)", to)
		}
		end = t.UTC()
	}
	start := end.Add(-since)
	if from != "" {
		t, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return obsbackend.Range{}, obsbackend.Wrong("--from %q is not an RFC 3339 time (2026-10-22T10:00:00Z)", from)
		}
		start = t.UTC()
	} else if since <= 0 {
		return obsbackend.Range{}, obsbackend.Wrong("--since must be positive")
	}
	if !end.After(start) {
		return obsbackend.Range{}, obsbackend.Wrong("the range is empty: --from %s is not before the end %s", start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	return obsbackend.Range{From: start, To: end}, nil
}

// obsEnvArg is the optional [env] positional; "dev" when absent, matching
// `forge env up`.
func obsEnvArg(args []string, at int) string {
	if len(args) > at && args[at] != "" {
		return args[at]
	}
	return "dev"
}

// open resolves env's backend. A verb calls it once, then queries.
func (c *obsCommon) open(ctx context.Context, d obsDeps, env, _ string) (obsbackend.Backend, obsbackend.Decision, error) {
	facts, err := d.Facts(ctx, d.Stderr, env)
	if err != nil {
		return nil, obsbackend.Decision{}, err
	}
	dec, err := obsbackend.Resolve(facts, c.backend)
	if err != nil {
		return nil, dec, err
	}
	if dec.Backend == obsbackend.BackendLocal {
		return d.Local(env), dec, nil
	}
	cb, err := d.Cloud(ctx, env)
	if err != nil {
		return nil, dec, err
	}
	return cb, dec, nil
}

// ── rendering ───────────────────────────────────────────────────────────

// obsScope builds the scope echo of a document.
func obsScope(info obsbackend.Info, n obsbackend.Narrow) obsout.Scope {
	return obsout.Scope{Env: info.Env, Backend: info.Backend, EnvID: info.EnvID, From: n.Range.From, To: n.Range.To, Services: n.Services}
}

// obsFail renders a verb's failure for --json (a failure document on stdout)
// and returns the error either way, so the process status is the document's.
func obsFail(d obsDeps, jsonOut bool, schema string, err error) error {
	if jsonOut {
		f := obsout.Failed(schema, err, obsbackend.SuggestedOf(err)...)
		if werr := (obsout.Schema{Name: schema}).Write(d.Stdout, f, nil); werr != nil {
			return errors.Join(err, werr)
		}
		// The failure is already on stdout as the document; main() prints the
		// error to stderr as usual.
	}
	return err
}

// ── structured-vs-raw usage counter (DESIGN §14 D2, condition 2) ────────

const (
	// QueryModeStructured marks a statement the compiler built from typed flags.
	QueryModeStructured = "structured"
	// QueryModeRaw marks a statement the caller wrote (--sql, or a panel's sql).
	QueryModeRaw = "raw"
)

// obsLog is where the raw-query shape is logged. It writes to stderr at DEBUG,
// so it is silent unless FORGE_OBS_DEBUG is set; the CLI has no global verbose
// flag, and this is the one place a debug line is useful to the people
// measuring whether the structured vocabulary is enough.
func obsLog(w io.Writer) *slog.Logger {
	level := slog.LevelError + 1
	if os.Getenv("FORGE_OBS_DEBUG") != "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// logRawQuery records that a raw query ran and its SHAPE: the statement with
// every literal removed. If raw queries dominate, the structured vocabulary
// is not flexible enough, and the shapes say what to add.
func logRawQuery(w io.Writer, env, surface, sql string) {
	obsLog(w).Debug("obs raw query", "surface", surface, "env", env, "query_mode", QueryModeRaw, "shape", obsbackend.SQLShape(sql))
}

// explainText prints compiled statements: the human form of --explain.
func explainText(w io.Writer, stmts []obsbackend.Statement) {
	for i, s := range stmts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "-- %s\n%s\n", s.Purpose, strings.TrimRight(s.SQL, "\n"))
		for _, k := range paramNames(s.Params) {
			fmt.Fprintf(w, "--param_%s=%s\n", k, s.Params[k])
		}
	}
}

func paramNames(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

// openObsLedger opens this machine's release ledger for the change feed.
func openObsLedger() (obsbackend.LedgerReader, error) {
	return openMachineLedger(projectDirForKCL())
}

// declaredMetricSchemas reads the metrics schema `forge build` wrote
// (bin/observability/metrics-schema.json) for the catalog's declared join. A
// project that has not built, or whose build could not run its binary, has none:
// the catalog is then what was observed, which is the honest answer.
func declaredMetricSchemas() []metrics.Schema {
	raw, err := os.ReadFile(filepath.Join(projectDirForKCL(), "bin", filepath.FromSlash(MetricsSchemaArtifact)))
	if err != nil {
		return nil
	}
	s, err := metrics.ParseSchema(raw)
	if err != nil {
		return nil
	}
	return []metrics.Schema{s}
}

// pageCursorPrefix marks a next_page_token forge wrapped with the window it
// was issued for (pageCursor).
const pageCursorPrefix = "fp1."

// pageCursor is what forge hands out as next_page_token: the backend's own
// token plus the exact window it belongs to.
//
// WHY WRAP IT. A backend's token is bound to the request that produced it,
// window included, and the default window is RELATIVE (--since 72h): re-running
// the same command with --page-token computes a new "now", so the next page was
// refused "page_token belongs to a different request" and the only way through
// was copying scope.from/scope.to into --from/--to by hand. Carrying the window
// in the token makes `--page-token <next_page_token>` resume exactly the
// listing it came from, whatever --since says. The inner token stays opaque.
type pageCursor struct {
	From  time.Time `json:"f"`
	To    time.Time `json:"t"`
	Token string    `json:"k"`
}

// wrapPageToken is next_page_token for a backend token issued over rng; empty
// stays empty.
func wrapPageToken(rng obsbackend.Range, token string) string {
	if token == "" {
		return ""
	}
	b, err := json.Marshal(pageCursor{From: rng.From.UTC(), To: rng.To.UTC(), Token: token})
	if err != nil {
		return token
	}
	return pageCursorPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// resumePage unwraps a --page-token: the backend's token, with n's range
// pinned to the window the token was issued for. A token forge did not wrap
// (an older forge's, or one copied from elsewhere) passes through unchanged.
func resumePage(n *obsbackend.Narrow, token string) (string, error) {
	raw, ok := strings.CutPrefix(token, pageCursorPrefix)
	if !ok {
		return token, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	var c pageCursor
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil || c.Token == "" || !c.To.After(c.From) {
		return "", obsbackend.Wrong("--page-token is not a next_page_token forge issued (it is damaged or truncated)")
	}
	n.Range = obsbackend.Range{From: c.From, To: c.To}
	return c.Token, nil
}
