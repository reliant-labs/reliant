package obsbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Column is one result column.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Result is a statement's output: its columns and rows. Cell values are what
// ClickHouse's JSONCompact format decodes to — string, json.Number, bool,
// nil, []any, map[string]any — read through the As* helpers.
type Result struct {
	Columns []Column
	Rows    [][]any
}

// Col is the index of the column called name, or -1.
func (r Result) Col(name string) int {
	for i, c := range r.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// Limits bound one statement. The runner always applies readonly=2.
type Limits struct {
	Timeout time.Duration
	// MaxRows is the result row cap; 0 = none.
	MaxRows int
	// Break ends the result at MaxRows (a partial result) instead of failing
	// the statement. Raw SQL uses it so a wide SELECT returns its first rows.
	Break bool
}

// DefaultLimits are a structured verb's: a generous row cap that throws, so a
// statement that returns more than its own LIMIT is a bug and not a silent cut.
func DefaultLimits() Limits {
	return Limits{Timeout: 30 * time.Second, MaxRows: obsquery.MaxSQLRows + 1}
}

func (l Limits) settings() map[string]any {
	s := alerting.QueryOptions{MaxExecutionTime: l.Timeout, MaxResultRows: l.MaxRows}.Settings()
	if l.Break {
		s["result_overflow_mode"] = "break"
	}
	return s
}

// Runner runs one compiled statement against a ClickHouse. A seam so verbs
// are testable without docker: the local backend is the only consumer.
type Runner interface {
	Run(ctx context.Context, st obsquery.Stmt, lim Limits) (Result, error)
}

// ExecFunc runs `sh -c script sh args...` inside the ClickHouse container and
// returns its stdout. The default is docker compose exec; tests substitute
// docker exec against a container of their own.
type ExecFunc func(ctx context.Context, script string, args []string) ([]byte, error)

// ComposeFunc is the project's `docker compose` runner (internal/doctor's
// ComposeRunner, which has this shape).
type ComposeFunc func(ctx context.Context, projectDir string, args ...string) ([]byte, error)

// ComposeExec runs the script in the local ClickStack's compose service.
// project is the `-p <name>` prefix selecting the compose project `forge env
// up` started the env's stack in (the bare default project is one it never
// starts); it is resolved per call, so a stack started after the command began
// is still found.
func ComposeExec(compose ComposeFunc, projectDir string, project func(context.Context) []string) ExecFunc {
	return func(ctx context.Context, script string, args []string) ([]byte, error) {
		var argv []string
		if project != nil {
			argv = append(argv, project(ctx)...)
		}
		argv = append(argv, "exec", "-T", clickstack.Service, "sh", "-c", script, "sh")
		return compose(ctx, projectDir, append(argv, args...)...)
	}
}

// DockerRunner runs statements through clickhouse-client inside the ClickStack
// container, with the credentials it already holds. The SQL and every
// parameter travel as positional arguments of `sh -c`, never interpolated into
// the script, and every value is bound server-side (--param_pN=).
type DockerRunner struct {
	Exec ExecFunc
	// Env names the env in the remedy commands of a "not running" error.
	Env string
}

const clientScript = `q="$1"; shift; exec clickhouse-client -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" ` +
	`--format JSONCompact --output_format_json_quote_64bit_integers=0 --session_timezone=UTC --query "$q" "$@"`

// Run implements Runner.
func (d DockerRunner) Run(ctx context.Context, st obsquery.Stmt, lim Limits) (Result, error) {
	args := []string{st.SQL}
	args = append(args, ParamArgs(st.Params)...)
	set := lim.settings()
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, fmt.Sprintf("--%s=%v", k, set[k]))
	}
	out, err := d.Exec(ctx, clientScript, args)
	if err != nil {
		return Result{}, d.classify(ctx, err)
	}
	return DecodeJSONCompact(out)
}

// ParamArgs renders a statement's bound parameters as clickhouse-client flags,
// sorted so the same statement always has the same argv.
func ParamArgs(params map[string]string) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys))
	for _, k := range keys {
		args = append(args, "--param_"+k+"="+params[k])
	}
	return args
}

// classify turns a failed exec into the status it deserves: the stack being
// down (or docker missing) is "could not look"; a ClickHouse exception is the
// statement's fault.
func (d DockerRunner) classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return Undetermined("the query was cancelled: %v", ctx.Err())
	}
	msg := err.Error()
	up := "forge env up"
	if d.Env != "" {
		up += " " + d.Env
	}
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return Undetermined("docker is not installed or not on PATH, so the local ClickStack cannot be reached").With(up).Cause(err)
	case strings.Contains(msg, "is not running"), strings.Contains(msg, "no such service"), strings.Contains(msg, "No such container"),
		strings.Contains(msg, "Cannot connect to the Docker daemon"), strings.Contains(msg, "no configuration file provided"):
		return Undetermined("the local ClickStack is not running — start it with `%s`", up).With(up).Cause(err)
	}
	if ex := clickhouseException(msg); ex != "" {
		return Wrong("%s", ex).Cause(nil)
	}
	return Undetermined("the local ClickStack did not answer").Cause(err)
}

// clickhouseException extracts the server's message from clickhouse-client's
// stderr ("Code: 62. DB::Exception: Syntax error ... (SYNTAX_ERROR)").
func clickhouseException(msg string) string {
	i := strings.LastIndex(msg, "DB::Exception")
	if i < 0 {
		return ""
	}
	line := msg[i:]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	line = strings.TrimPrefix(line, "DB::Exception: ")
	if k := strings.Index(line, "DB::Exception: "); k >= 0 {
		line = line[k+len("DB::Exception: "):]
	}
	return strings.TrimSpace(line)
}

// DecodeJSONCompact parses clickhouse-client's JSONCompact output.
func DecodeJSONCompact(out []byte) (Result, error) {
	var doc struct {
		Meta []Column `json:"meta"`
		Data [][]any  `json:"data"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return Result{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return Result{}, Undetermined("the local ClickStack returned output forge cannot read").Cause(err)
	}
	return Result{Columns: doc.Meta, Rows: doc.Data}, nil
}

// ── cell readers ────────────────────────────────────────────────────────

// AsString reads a String cell.
func AsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

// AsFloat reads a numeric cell; NaN and Inf arrive as null and read as 0.
func AsFloat(v any) float64 {
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		return f
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

// AsInt reads an integer cell.
func AsInt(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return int64(f)
	case float64:
		return int64(t)
	case string:
		i, _ := strconv.ParseInt(t, 10, 64)
		return i
	}
	return 0
}

// AsUint reads an unsigned integer cell.
func AsUint(v any) uint64 {
	if n, ok := v.(json.Number); ok {
		if u, err := strconv.ParseUint(n.String(), 10, 64); err == nil {
			return u
		}
	}
	if s, ok := v.(string); ok {
		u, _ := strconv.ParseUint(s, 10, 64)
		return u
	}
	return uint64(AsInt(v))
}

// AsTime reads a DateTime / DateTime64 cell (rendered in UTC: the client runs
// with session_timezone=UTC).
func AsTime(v any) time.Time {
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}

// AsStringMap reads a Map(String, String) cell.
func AsStringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, x := range m {
		out[k] = AsString(x)
	}
	return out
}

// AsStrings reads an Array(String) cell.
func AsStrings(v any) []string {
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = AsString(x)
	}
	return out
}

// AsFloats reads an Array(Float64) cell.
func AsFloats(v any) []float64 {
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, len(a))
	for i, x := range a {
		out[i] = AsFloat(x)
	}
	return out
}

// AsAny returns the elements of an Array cell.
func AsAny(v any) []any {
	a, _ := v.([]any)
	return a
}
