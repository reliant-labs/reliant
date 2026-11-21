package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/opindex"
)

const schemaObsOps = "forge.obs.ops/v1"

var obsOpsSchema = obsout.Schema{
	Name: schemaObsOps,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services", "scope.instance", "scope.instance.id", "scope.instance.label", "scope.instance.user",
		"query_mode", "sort", "names",
		"ops", "ops.service", "ops.name", "ops.kind", "ops.found", "ops.calls", "ops.errors", "ops.error_ratio",
		"ops.rate_per_second", "ops.p50_ms", "ops.p95_ms", "ops.p99_ms",
		"ops.code_file_path", "ops.code_function", "ops.code_line", "ops.code_source",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

// Where an op's code location came from.
const (
	opCodeTelemetry = "telemetry" // the span's code.file.path
	opCodeStatic    = "static"    // this checkout's source, by op name
)

type obsOpDoc struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	// Found is false only for an op --name asked for that had no spans in
	// the window and scope: no traffic, as opposed to not asked.
	Found         bool    `json:"found"`
	Calls         float64 `json:"calls"`
	Errors        float64 `json:"errors,omitempty"`
	ErrorRatio    float64 `json:"error_ratio"`
	RatePerSecond float64 `json:"rate_per_second"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
	CodeFilePath  string  `json:"code_file_path,omitempty"`
	CodeFunction  string  `json:"code_function,omitempty"`
	CodeLine      int     `json:"code_line,omitempty"`
	// CodeSource is where the code fields came from: "telemetry", "static",
	// or absent when neither knew.
	CodeSource string `json:"code_source,omitempty"`
}

type obsOpsDoc struct {
	obsout.Head
	Scope     obsout.Scope `json:"scope"`
	QueryMode string       `json:"query_mode"`
	Sort      string       `json:"sort"`
	// Names are the ops --name asked for, deduplicated, in the order given.
	Names      []string               `json:"names,omitempty"`
	Ops        []obsOpDoc             `json:"ops"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

// opLocator finds an op's implementation in the project's source by its
// name. ok is false when the op is not this project's, or the source cannot
// be read: a location is a convenience, never a reason to fail.
type opLocator func(name string) (file string, line int, fn string, ok bool)

// staticOpLocator locates ops in the project at projectDir with
// internal/opindex. It reads the source once, on the first lookup, so a run
// whose every op carries its location in telemetry never reads it; a tree
// it cannot index locates nothing.
func staticOpLocator(projectDir string) opLocator {
	var once sync.Once
	var ix *opindex.Index
	return func(name string) (string, int, string, bool) {
		once.Do(func() { ix, _ = opindex.Build(projectDir) })
		if ix == nil {
			return "", 0, "", false
		}
		op, ok := ix.Lookup(name)
		if !ok || op.File == "" {
			return "", 0, "", false
		}
		return op.File, op.Line, op.Function, true
	}
}

type obsOpsOpts struct {
	obsCommon
	sort  string
	top   int
	kinds []string
	// names restricts the answer to these ops (--name); the backend asks
	// only for them, so a named request never scans every op.
	names   []string
	nameSet bool // --name was given, possibly as ""
	// locate resolves the ops whose spans carry no code location; nil skips
	// that (the hosted MCP tools have no checkout).
	locate opLocator
}

func newObsOpsCmd() *cobra.Command {
	return newObsOpsCmdWith(defaultObsDeps, func() opLocator { return staticOpLocator(projectDirForKCL()) })
}

// newObsOpsCmdWith is newObsOpsCmd reading the world through deps, and this
// checkout's source through locator (called once per run); a nil locator
// locates nothing. The CLI passes defaultObsDeps and the checkout, the hosted
// MCP tools their own deps and nil: they have no checkout (mcp_hosted.go).
func newObsOpsCmdWith(deps obsDepsFunc, locator func() opLocator) *cobra.Command {
	var o obsOpsOpts
	cmd := &cobra.Command{
		Use:   "ops [env]",
		Short: "Every op the project runs — rpcs, endpoints, component methods — by traffic, latency and errors",
		Long: `Rank the env's ops by traffic, p95 latency, error ratio or rate: calls,
errors, p50/p95/p99, and where the op's code is. [env] defaults to dev.
Counts are weighted for tail sampling.

An op is one of:
  rpc        a Connect rpc's server span, <proto pkg>.<Service>/<Method>
  endpoint   any other server span (an HTTP route)
  component  a component method, the span forge's generated decorator opens,
             <pkg>.<Method>

CODE is the op's source file: from the span's code.file.path when the build
reports it (code_source "telemetry"), else found in this checkout by the op's
name (code_source "static").

  ` + Name() + ` obs ops dev --sort p95 --top 5
  ` + Name() + ` obs ops prod --kind component --sort errors --since 6h
  ` + Name() + ` obs ops prod --name billing.Charge --name todo.v1.TodoService/CreateTodo --since 7d

Each kind's table is the busiest 200 ops; --sort re-ranks within them.

--name asks for these ops only, by span name (repeatable or comma-separated,
at most ` + strconv.Itoa(maxOpsNames) + ` names of at most ` + strconv.Itoa(maxOpsNameLen) + ` characters each). The query is narrowed to
them, so it is cheap over any window. Every requested op is listed: one with
no spans in the window and scope has "found": false and zero figures, which
tells "no traffic" apart from "not asked". With --name, --top defaults to
every requested op.

` + obsOpsSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if locator != nil {
				o.locate = locator()
			}
			o.nameSet = cmd.Flags().Changed("name")
			if len(o.names) > 0 && !cmd.Flags().Changed("top") {
				o.top = 0 // every requested op
			}
			return runObsOps(cmd.Context(), deps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, time.Hour, 0)
	cmd.Flags().StringVar(&o.sort, "sort", "calls", "calls | p95 | errors | rate")
	cmd.Flags().IntVar(&o.top, "top", 20, "most ops to return (with --name, default every requested op)")
	cmd.Flags().StringSliceVar(&o.kinds, "kind", nil, "only these kinds, comma-separated: rpc, endpoint, component (default all)")
	cmd.Flags().StringSliceVar(&o.names, "name", nil, "only these ops, by span name; repeatable or comma-separated (an op with no spans is listed with found false)")
	_ = cmd.Flags().MarkHidden("limit")
	return cmd
}

func runObsOps(ctx context.Context, d obsDeps, env string, o obsOpsOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsOpsSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsOps, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsOps, err) }
	names, err := opsNames(o.names)
	if err == nil && o.nameSet && len(names) == 0 {
		// --name "" parses to no names; an unnarrowed scan is not what
		// was asked for.
		err = obsbackend.Wrong("--name: an empty op name")
	}
	if err != nil {
		return fail(err)
	}
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "ops")
	if err != nil {
		return fail(err)
	}
	reader, ok := b.(obsbackend.OpsReader)
	if !ok {
		return fail(obsbackend.Undetermined("the %s backend cannot rank ops", b.Info().Backend))
	}
	req := obsbackend.OpsRequest{Narrow: n, Sort: o.sort, Top: o.top, Kinds: o.kinds, Names: names}
	if len(names) > 0 {
		// Uncapped, so an op missing from the answer had no spans rather
		// than lost its place to --top; the cap applies below.
		req.Top = 0
	}
	res, err := reader.Ops(ctx, req)
	if err != nil {
		return fail(err)
	}
	doc := obsOpsDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Sort: o.sort, Names: names, Ops: []obsOpDoc{}}
	omitted := res.Omitted
	if n.Explain {
		// --explain is the compiled statements alone: the backend only
		// compiled them (Narrow.Explain), and no row, found or missing, is
		// a measurement.
		doc.Statements, omitted = res.Statements, 0
	} else {
		for _, op := range res.Ops {
			od := opDoc(op, o.locate)
			od.Found = true
			doc.Ops = append(doc.Ops, od)
		}
	}
	if len(names) > 0 && !n.Explain {
		doc.Ops = append(doc.Ops, missingOps(names, res.Ops, o.locate)...)
		if o.top > 0 && len(doc.Ops) > o.top {
			omitted = len(doc.Ops) - o.top
			doc.Ops = doc.Ops[:o.top]
		}
	}
	doc.Page = obsout.Page{Truncated: omitted > 0, Omitted: omitted}
	if len(doc.Ops) > 0 && doc.Ops[0].Found {
		doc.Suggested = o.inst.carrySuggested(opSuggestions(env, doc.Ops[0]))
	}
	doc.Stamp(schemaObsOps, nil)
	if done, err := o.out.Emit(d.Stdout, obsOpsSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	rows := make([][]string, len(doc.Ops))
	notFound := 0
	for i, op := range doc.Ops {
		service := op.Service
		if !op.Found {
			service = "(no spans)"
			notFound++
		}
		rows[i] = []string{service, op.Kind, op.Name, fmt.Sprintf("%.0f", op.Calls), strconv.FormatFloat(op.ErrorRatio*100, 'f', 1, 64) + "%",
			strconv.FormatFloat(op.RatePerSecond, 'f', 2, 64) + "/s", ms(op.P50Ms), ms(op.P95Ms), ms(op.P99Ms), opCode(op)}
	}
	obsout.Table(d.Stdout, []string{"SERVICE", "KIND", "OP", "CALLS", "ERRORS", "RATE", "P50", "P95", "P99", "CODE"}, rows, 60)
	if notFound > 0 {
		fmt.Fprintf(d.Stdout, "\n(%d requested op(s) had no spans in the window)\n", notFound)
	}
	if omitted > 0 {
		fmt.Fprintf(d.Stdout, "\n(%d more; --top to show them)\n", omitted)
	}
	return nil
}

// --name bounds: a request names a release's or a pipeline's ops, not every
// op there is, and each name is a span name.
const (
	maxOpsNames   = 200
	maxOpsNameLen = 200
)

// opsNames validates --name: each non-empty after trimming and at most
// maxOpsNameLen characters, at most maxOpsNames of them once deduplicated.
// The order given is kept.
func opsNames(raw []string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for _, r := range raw {
		name := strings.TrimSpace(r)
		switch {
		case name == "":
			return nil, obsbackend.Wrong("--name: an empty op name")
		case utf8.RuneCountInString(name) > maxOpsNameLen:
			return nil, obsbackend.Wrong("--name %q…: an op name is at most %d characters", string([]rune(name)[:40]), maxOpsNameLen)
		case seen[name]:
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) > maxOpsNames {
		return nil, obsbackend.Wrong("--name: %d ops; at most %d per request", len(names), maxOpsNames)
	}
	return names, nil
}

// missingOps are the requested names the answer has no row for, in the
// order requested: each with zero figures, Found false, and its code
// location from this checkout when it has one.
func missingOps(names []string, got []obsbackend.OpStats, locate opLocator) []obsOpDoc {
	have := map[string]bool{}
	for _, op := range got {
		have[op.Name] = true
	}
	var out []obsOpDoc
	for _, name := range names {
		if !have[name] {
			out = append(out, opDoc(obsbackend.OpStats{Name: name, Kind: obsbackend.OpKindOf(name)}, locate))
		}
	}
	return out
}

// opDoc is op's document, its code location filled from this checkout when
// the telemetry has none. A telemetry location takes its line from the
// checkout when both name the same file.
func opDoc(op obsbackend.OpStats, locate opLocator) obsOpDoc {
	doc := obsOpDoc{
		Service: op.Service, Name: op.Name, Kind: op.Kind, Calls: op.Calls, Errors: op.Errors, ErrorRatio: op.ErrorRatio,
		RatePerSecond: op.RatePerSecond, P50Ms: op.P50Ms, P95Ms: op.P95Ms, P99Ms: op.P99Ms,
		CodeFilePath: op.CodeFilePath, CodeFunction: op.CodeFunction,
	}
	if doc.CodeFilePath != "" {
		doc.CodeSource = opCodeTelemetry
	}
	if locate == nil || op.Kind == obsbackend.OpKindEndpoint {
		return doc
	}
	file, line, fn, ok := locate(op.Name)
	switch {
	case !ok:
	case doc.CodeSource == "":
		doc.CodeFilePath, doc.CodeLine, doc.CodeFunction, doc.CodeSource = file, line, fn, opCodeStatic
	case file == doc.CodeFilePath:
		doc.CodeLine = line
	}
	return doc
}

// opCode is the CODE column: file:line, the file alone, or "-".
func opCode(op obsOpDoc) string {
	switch {
	case op.CodeFilePath == "":
		return "-"
	case op.CodeLine > 0:
		return op.CodeFilePath + ":" + strconv.Itoa(op.CodeLine)
	}
	return op.CodeFilePath
}

// opSuggestions are the traces of op, slowest and failing. `traces --route`
// matches a trace's ROOT span, which a server span is and a component span
// is not; a component span is found by its forge.op attribute, which a build
// that reports code.file.path also stamps. Without it, the service's traces
// are the nearest.
func opSuggestions(env string, op obsOpDoc) []string {
	narrow := []string{"--service", op.Service}
	switch {
	case op.Kind != obsbackend.OpKindComponent:
		narrow = append(narrow, "--route", op.Name)
	case op.CodeSource == opCodeTelemetry:
		narrow = append(narrow, "--filter", "span.attr.forge.op="+op.Name)
	}
	cmd := func(flag string) string {
		return obsout.Command(append(append([]string{"forge", "obs", "traces", env}, narrow...), flag)...)
	}
	return obsout.Suggest(cmd("--slow"), cmd("--errors"))
}
