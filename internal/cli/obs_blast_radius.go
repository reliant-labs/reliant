package cli

// `forge obs blast-radius` — the ops a diff can change, and how much traffic
// each of them carries. The static half (which ops a change reaches, and
// which commits did it) is internal/opindex; the traffic is the same
// OpsReader query `forge obs ops` runs, restricted to the mapped op names.
//
// The mapping is the answer. Telemetry only weighs it, so a backend that
// cannot be opened or queried degrades the document (telemetry.status
// "unavailable", exit 0) instead of failing it: a reviewer tiering a PR's
// risk still gets which ops it touches when the env's stack is down.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/opindex"
)

const schemaObsBlastRadius = "forge.obs.blast_radius/v1"

var obsBlastRadiusSchema = obsout.Schema{
	Name: schemaObsBlastRadius,
	Fields: []string{
		"base", "base_ref", "head", "head_ref",
		"commits", "commits.sha", "commits.subject", "commits.pr", "total_commits",
		"order",
		"ops", "ops.name", "ops.kind", "ops.package", "ops.file", "ops.line", "ops.function",
		"ops.rpc_service", "ops.rpc_method", "ops.proto_file", "ops.proto_line", "ops.removed",
		"ops.reasons", "ops.reasons.via", "ops.reasons.function", "ops.reasons.file", "ops.reasons.line",
		"ops.commits", "ops.commits.sha", "ops.commits.subject", "ops.commits.pr",
		"ops.traffic", "ops.traffic.service", "ops.traffic.calls", "ops.traffic.errors", "ops.traffic.error_ratio",
		"ops.traffic.rate_per_second", "ops.traffic.p50_ms", "ops.traffic.p95_ms", "ops.traffic.p99_ms",
		"unattributed", "unattributed.file", "unattributed.reason",
		"unattributed.commits", "unattributed.commits.sha", "unattributed.commits.subject", "unattributed.commits.pr",
		"telemetry", "telemetry.env", "telemetry.backend", "telemetry.from", "telemetry.to",
		"telemetry.status", "telemetry.reason", "telemetry.reason_code",
		"summary", "summary.ops", "summary.components", "summary.rpcs", "summary.removed",
		"summary.with_traffic", "summary.without_traffic", "summary.calls", "summary.unattributed_files",
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services", "scope.instance", "scope.instance.id", "scope.instance.label", "scope.instance.user",
		"warnings",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

// Telemetry statuses: telemetry.status.
const (
	brTelemetryOK           = "ok"
	brTelemetryUnavailable  = "unavailable"
	brTelemetryNotRequested = "not_requested"
)

// Why telemetry is not "ok": telemetry.reason_code.
const (
	brReasonNoEnv              = "no_env"              // no [env] was given
	brReasonNoOps              = "no_ops"              // the diff maps to no ops; nothing to ask
	brReasonExplain            = "explain"             // --explain compiled the query and ran nothing
	brReasonBackendUnavailable = "backend_unavailable" // the env's backend could not be opened
	brReasonUnsupported        = "unsupported"         // the backend cannot rank ops
	brReasonQueryFailed        = "query_failed"        // the ops query failed
)

// How ops are ordered: order.
const (
	brOrderCalls = "calls" // total calls descending, then name
	brOrderName  = "name"
)

// brUncommitted labels the working tree's pseudo-commit in the table.
const brUncommitted = "uncommitted"

type obsBRCommitDoc struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	PR      int    `json:"pr"`
}

type obsBRReasonDoc struct {
	Via      string `json:"via"`
	Function string `json:"function,omitempty"`
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
}

type obsBRTrafficDoc struct {
	Service       string  `json:"service"`
	Calls         float64 `json:"calls"`
	Errors        float64 `json:"errors"`
	ErrorRatio    float64 `json:"error_ratio"`
	RatePerSecond float64 `json:"rate_per_second"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
}

type obsBROpDoc struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Package    string            `json:"package,omitempty"`
	File       string            `json:"file,omitempty"`
	Line       int               `json:"line,omitempty"`
	Function   string            `json:"function,omitempty"`
	RPCService string            `json:"rpc_service,omitempty"`
	RPCMethod  string            `json:"rpc_method,omitempty"`
	ProtoFile  string            `json:"proto_file,omitempty"`
	ProtoLine  int               `json:"proto_line,omitempty"`
	Removed    bool              `json:"removed"`
	Reasons    []obsBRReasonDoc  `json:"reasons"`
	Commits    []obsBRCommitDoc  `json:"commits"`
	Traffic    []obsBRTrafficDoc `json:"traffic"`
}

// calls is the op's calls across every service that runs it.
func (op obsBROpDoc) calls() float64 {
	var n float64
	for _, t := range op.Traffic {
		n += t.Calls
	}
	return n
}

type obsBRUnattributedDoc struct {
	File    string           `json:"file"`
	Reason  string           `json:"reason"`
	Commits []obsBRCommitDoc `json:"commits"`
}

type obsBRTelemetryDoc struct {
	Env        string    `json:"env,omitempty"`
	Backend    string    `json:"backend,omitempty"`
	From       time.Time `json:"from,omitzero"`
	To         time.Time `json:"to,omitzero"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	ReasonCode string    `json:"reason_code,omitempty"`
}

type obsBRSummaryDoc struct {
	Ops               int     `json:"ops"`
	Components        int     `json:"components"`
	RPCs              int     `json:"rpcs"`
	Removed           int     `json:"removed"`
	WithTraffic       int     `json:"with_traffic"`
	WithoutTraffic    int     `json:"without_traffic"`
	Calls             float64 `json:"calls"`
	UnattributedFiles int     `json:"unattributed_files"`
}

type obsBlastRadiusDoc struct {
	obsout.Head
	Base    string `json:"base"`
	BaseRef string `json:"base_ref"`
	// HeadSHA is the document's "head"; the name is taken by the embedded
	// obsout.Head.
	HeadSHA      string                 `json:"head"`
	HeadRef      string                 `json:"head_ref"`
	Commits      []obsBRCommitDoc       `json:"commits"`
	TotalCommits int                    `json:"total_commits"`
	Truncated    bool                   `json:"truncated"`
	Order        string                 `json:"order"`
	Ops          []obsBROpDoc           `json:"ops"`
	Unattributed []obsBRUnattributedDoc `json:"unattributed"`
	Telemetry    obsBRTelemetryDoc      `json:"telemetry"`
	Summary      obsBRSummaryDoc        `json:"summary"`
	Scope        *obsout.Scope          `json:"scope,omitempty"`
	Warnings     []string               `json:"warnings,omitempty"`
	Statements   []obsbackend.Statement `json:"statements,omitempty"`
	Suggested    []string               `json:"suggested,omitempty"`
}

type obsBlastRadiusOpts struct {
	obsCommon
	base, head string
	// dir is the project directory: the forge.yaml's, else -C or the
	// working directory. Set by the command; tests set it directly.
	dir string
}

func newObsBlastRadiusCmd() *cobra.Command {
	var o obsBlastRadiusOpts
	cmd := &cobra.Command{
		Use:   "blast-radius [env]",
		Short: "The ops a diff can change — rpcs and component methods — with the commits that changed each and its traffic",
		Long: `Map a diff to the ops it can change, and weigh each by its traffic in [env].

An op is a unit the running code names a span after:
  rpc        a Connect rpc, <proto pkg>.<Service>/<Method>
  component  a component method forge's generated decorator wraps, <pkg>.<Method>

The diff is base..head, read commit by commit, so each op names the commits
that changed it. --base defaults to the merge base of origin/main (main when
there is no origin/main) and the head. With --head, the diff is the commits
base..head, tree to tree: nothing uncommitted is read. Without it, the diff is
the commits base..HEAD plus the pseudo-commit with sha "" and subject
"uncommitted changes": HEAD against the working tree, so staged and unstaged
changes alike (where they disagree, the working tree's content wins), and
every untracked file .gitignore does not exclude, as a whole-file addition.
At most the newest 100 commits are mapped (truncated says when the range held
more).

A changed Go declaration reaches an op through its package's static
reference graph (via: method, handler, helper, or package when nothing in the
package reaches it, which names every op of the package); a changed rpc or
message in a .proto reaches its rpcs (via: proto). Every changed file is
either in some op's reasons or in unattributed with a reason. The graph never
crosses packages: a change to a package with no ops is unattributed even when
an op calls it.

With [env], each op gets its traffic over --since (default 7d): calls,
errors, p50/p95/p99, per service that runs it, from the same query as
` + "`" + Name() + ` obs ops` + "`" + `. Without [env] the answer is the static mapping alone.
The mapping is the answer and telemetry only weighs it: a backend that cannot
be opened or queried leaves telemetry.status "unavailable", notes it on
stderr, and still exits 0.

  ` + Name() + ` obs blast-radius                       # this branch vs origin/main, no telemetry
  ` + Name() + ` obs blast-radius prod --json            # weighted by prod's last 7 days
  ` + Name() + ` obs blast-radius prod --base v1.4.0 --head v1.5.0

Exit status: 0 with the mapping (whatever telemetry did); 1 when the request
is wrong (--base or --head names no commit, there is no origin/main or main
to default to, a bad flag); 2 when it cannot look (not a git checkout, git is
missing, the history cannot be read).

The --json document (fields are stable under the schema name):
  schema, ok, exit_code, error   the head every obs document leads with
  base          the resolved base commit
  base_ref      what base was resolved from: --base as given, or
                "origin/main" / "main" (the default, via their merge base)
  head          the resolved head commit; "" for the working tree
  head_ref      --head as given, or "working tree"
  commits       the commits mapped, newest first: {sha, subject, pr}; pr is
                the "(#123)" a squash merge's subject ends with, 0 for none.
                The working tree's pseudo-commit comes first, sha ""
  total_commits the non-merge commits in base..head (pseudo-commit excluded)
  truncated     only the newest 100 commits were mapped
  order         how ops are sorted: "calls" (total calls across services,
                descending, then name) when telemetry.status is "ok",
                else "name"
  ops           each op the diff reaches:
    name, kind          the span name, and "rpc" or "component"
    package             project-relative Go package that implements it
    file, line          the implementing method (the handler of an rpc),
                        else the interface method or proto rpc line
    function            the implementing method as Go spells it, (*service).Send
    rpc_service, rpc_method   an rpc's rpc.service / rpc.method attributes
    proto_file, proto_line    where an rpc is declared
    removed             the rpc is not in the working tree any more (read from
                        the diff's own proto); its traffic is still asked for
    reasons             each changed declaration that reaches it:
                        {via, function, file, line}; via is method, handler,
                        helper, package or proto
    commits             the commits that changed it, newest first
    traffic             per service that ran it over the window:
                        {service, calls, errors, error_ratio, rate_per_second,
                        p50_ms, p95_ms, p99_ms}, busiest first; [] when it
                        had none or telemetry was not "ok". Counts are
                        weighted for tail sampling
  unattributed  each changed file no op is mapped from: {file, reason, commits}
  telemetry     {env, backend, from, to, status, reason, reason_code}:
                status "ok", "unavailable" (reason_code backend_unavailable,
                unsupported or query_failed) or "not_requested" (no_env,
                no_ops, explain)
  summary       {ops, components, rpcs, removed, with_traffic,
                without_traffic, calls, unattributed_files}; with_traffic and
                without_traffic split ops only when telemetry.status is "ok"
                (else both 0); calls is the total over every op
  scope         the obs scope the traffic was read in, when it was read
  warnings      source files the op index could not read or parse (their
                ops are missing)
  statements    the compiled query, with --explain
  suggested     next commands: the busiest changed rpc's traces, the ops view

` + obsBlastRadiusSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.dir = blastRadiusProjectDir()
			env := ""
			if len(args) > 0 {
				env = args[0]
			}
			return runObsBlastRadius(cmd.Context(), defaultObsDeps(cmd), env, o)
		},
	}
	o.register(cmd, 7*24*time.Hour, 0)
	cmd.Flags().StringVar(&o.base, "base", "", "the commit the diff starts from (default: the merge base of origin/main, else main, with the head)")
	cmd.Flags().StringVar(&o.head, "head", "", "the commit the diff ends at, read tree to tree (default: the working tree — HEAD's commits plus staged, unstaged and untracked, unignored files)")
	_ = cmd.Flags().MarkHidden("limit")
	return cmd
}

// blastRadiusProjectDir is the project the diff is mapped in: forge.yaml's
// directory, else -C or the working directory. Not projectDirForKCL, whose
// "." fallback ignores -C.
func blastRadiusProjectDir() string {
	if cfg, err := findProjectConfigFile(); err == nil {
		return filepath.Dir(cfg)
	}
	if root, err := cmdutil.ResolutionRoot(); err == nil {
		return root
	}
	return "."
}

func runObsBlastRadius(ctx context.Context, d obsDeps, env string, o obsBlastRadiusOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsBlastRadius, err) }
	fields, err := o.out.Parsed(obsBlastRadiusSchema)
	if err != nil {
		return fail(err)
	}
	// A bad flag is the caller's mistake even when telemetry would have
	// degraded anyway, so they are checked before anything is read.
	switch o.backend {
	case "", obsbackend.BackendLocal, obsbackend.BackendCloud:
	default:
		return fail(obsbackend.Wrong("--backend %q is not a backend; use %s or %s", o.backend, obsbackend.BackendLocal, obsbackend.BackendCloud))
	}
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}

	m, baseRef, warnings, err := blastRadiusMapFn(ctx, o.dir, o.base, o.head)
	if err != nil {
		return fail(err)
	}
	headRef := o.head
	if headRef == "" {
		headRef = "working tree"
	}
	doc := blastRadiusDoc(m, baseRef, headRef)
	doc.Warnings = warnings
	doc.Telemetry = obsBRTelemetryDoc{Env: env, Backend: o.backend, Status: brTelemetryNotRequested}
	if env != "" {
		doc.Telemetry.From, doc.Telemetry.To = n.Range.From, n.Range.To
	}

	switch {
	case env == "":
		doc.Telemetry.ReasonCode, doc.Telemetry.Reason = brReasonNoEnv, "no env was given; pass one to weigh the ops by their traffic"
	case len(doc.Ops) == 0:
		doc.Telemetry.ReasonCode, doc.Telemetry.Reason = brReasonNoOps, "the diff reaches no ops, so there was no traffic to ask for"
	default:
		res, b, code, terr := blastRadiusTraffic(ctx, d, &o, env, n, doc.opNames())
		if terr != nil && ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if b != nil {
			info := b.Info()
			doc.Telemetry.Backend = info.Backend
			scope := obsScope(info, n)
			doc.Scope = &scope
		}
		switch {
		case terr != nil:
			doc.Telemetry.Status, doc.Telemetry.ReasonCode, doc.Telemetry.Reason = brTelemetryUnavailable, code, terr.Error()
			fmt.Fprintf(d.Stderr, "note: telemetry for %s is unavailable (%v); the ops are the static mapping alone\n", env, terr)
		case n.Explain:
			doc.Statements = res.Statements
			doc.Telemetry.ReasonCode, doc.Telemetry.Reason = brReasonExplain, "--explain compiles the query without running it"
		default:
			doc.Telemetry.Status = brTelemetryOK
			doc.attachTraffic(res)
		}
	}
	doc.finish(env)
	doc.Stamp(schemaObsBlastRadius, nil)
	if done, err := o.out.Emit(d.Stdout, obsBlastRadiusSchema, doc, fields); done || err != nil {
		return err
	}
	printBlastRadius(d, doc)
	return nil
}

// blastRadiusTraffic asks env's backend for the traffic of names. On failure
// it returns the reason code the document carries; the backend, when one was
// opened, scopes the document either way.
func blastRadiusTraffic(ctx context.Context, d obsDeps, o *obsBlastRadiusOpts, env string, n obsbackend.Narrow, names []string) (obsbackend.OpsResult, obsbackend.Backend, string, error) {
	b, _, err := o.open(ctx, d, env, "blast-radius")
	if err != nil {
		return obsbackend.OpsResult{}, nil, brReasonBackendUnavailable, err
	}
	reader, ok := b.(obsbackend.OpsReader)
	if !ok {
		return obsbackend.OpsResult{}, b, brReasonUnsupported, fmt.Errorf("the %s backend cannot rank ops", b.Info().Backend)
	}
	res, err := reader.Ops(ctx, obsbackend.OpsRequest{Narrow: n, Names: names})
	if err != nil {
		return res, b, brReasonQueryFailed, err
	}
	return res, b, "", nil
}

// blastRadiusMapFn is the static half's seam: tests map a synthetic diff
// without a git repository.
var blastRadiusMapFn = blastRadiusMapping

// blastRadiusMapping maps base..head of the project at dir. base "" is the
// merge base of origin/main (main when there is none) with the head; head ""
// is the working tree. It returns the ref base was resolved from and the op
// index's warnings.
//
// Errors are classed for the exit status: a rev that names no commit is the
// request's fault (1); a directory that is not a git checkout, or history
// git cannot read, is a failure to look (2).
func blastRadiusMapping(ctx context.Context, dir, base, head string) (opindex.Mapping, string, []string, error) {
	if _, err := brGit(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		if ctx.Err() != nil {
			return opindex.Mapping{}, "", nil, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return opindex.Mapping{}, "", nil, obsbackend.Undetermined("git is not installed; blast-radius reads the diff with it").Cause(err)
		}
		return opindex.Mapping{}, "", nil, obsbackend.Undetermined("%s is not in a git checkout; blast-radius maps a git diff", dir).Cause(err)
	}
	verify := func(flag, rev string) error {
		if _, err := brRevParse(ctx, dir, rev); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return obsbackend.Wrong("%s %q names no commit in %s", flag, rev, dir).Cause(err)
		}
		return nil
	}
	if head != "" {
		if err := verify("--head", head); err != nil {
			return opindex.Mapping{}, "", nil, err
		}
	} else if _, err := brRevParse(ctx, dir, "HEAD"); err != nil {
		if ctx.Err() != nil {
			return opindex.Mapping{}, "", nil, ctx.Err()
		}
		return opindex.Mapping{}, "", nil, obsbackend.Undetermined("the checkout at %s has no commits yet", dir).Cause(err)
	}

	baseRef, baseRev := base, base
	if base != "" {
		if err := verify("--base", base); err != nil {
			return opindex.Mapping{}, "", nil, err
		}
	} else {
		var err error
		if baseRef, baseRev, err = brDefaultBase(ctx, dir, head); err != nil {
			return opindex.Mapping{}, "", nil, err
		}
	}

	ix, err := opindex.Build(dir)
	if err != nil {
		return opindex.Mapping{}, "", nil, obsbackend.Undetermined("cannot index the ops of %s", dir).Cause(err)
	}
	m, err := ix.MapRange(ctx, baseRev, head)
	if err != nil {
		if ctx.Err() != nil {
			return opindex.Mapping{}, "", nil, ctx.Err()
		}
		return opindex.Mapping{}, "", nil, obsbackend.Undetermined("cannot read the diff %s..%s", baseRef, emptyAs(head, "the working tree")).Cause(err)
	}
	return m, baseRef, ix.Warnings(), nil
}

// brDefaultBase picks the base when --base is absent: the merge base of the
// head with origin/main, else main. It returns the ref it chose and that
// merge base.
func brDefaultBase(ctx context.Context, dir, head string) (ref, rev string, err error) {
	for _, r := range []string{"origin/main", "main"} {
		if _, err := brRevParse(ctx, dir, r); err == nil {
			ref = r
			break
		}
	}
	if ref == "" {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", obsbackend.Wrong("no --base, and neither origin/main nor main exists in %s; pass --base <rev>", dir)
	}
	mb, err := opindex.MergeBase(ctx, dir, ref, head)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", obsbackend.Wrong("%s and the head share no history; pass --base <rev>", ref).Cause(err)
	}
	return ref, mb, nil
}

// brRevParse resolves rev to the commit it names.
func brRevParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := brGit(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	return strings.TrimSpace(out), err
}

// brGit runs a read-only git in dir. GIT_OPTIONAL_LOCKS=0 keeps a read from
// leaving another git in the same checkout facing index.lock.
func brGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// blastRadiusDoc is the static half of the document: m's commits, ops and
// unattributed files, every list non-nil and the ops in name order.
func blastRadiusDoc(m opindex.Mapping, baseRef, headRef string) obsBlastRadiusDoc {
	doc := obsBlastRadiusDoc{
		Base: m.Base, BaseRef: baseRef, HeadSHA: m.Head, HeadRef: headRef,
		Commits: brCommits(m.Commits), TotalCommits: m.Total, Truncated: m.Truncated,
		Ops: make([]obsBROpDoc, 0, len(m.Ops)), Unattributed: make([]obsBRUnattributedDoc, 0, len(m.Unattributed)),
	}
	for _, mo := range m.Ops {
		op := obsBROpDoc{
			Name: mo.Name, Kind: mo.Kind, Package: mo.Package, File: mo.File, Line: mo.Line, Function: mo.Function,
			RPCService: mo.RPCService, RPCMethod: mo.RPCMethod, ProtoFile: mo.ProtoFile, ProtoLine: mo.ProtoLine,
			Removed: mo.Removed, Reasons: make([]obsBRReasonDoc, 0, len(mo.Reasons)), Commits: brCommits(mo.Commits),
			Traffic: []obsBRTrafficDoc{},
		}
		for _, r := range mo.Reasons {
			op.Reasons = append(op.Reasons, obsBRReasonDoc{Via: r.Via, Function: r.Function, File: r.File, Line: r.Line})
		}
		doc.Ops = append(doc.Ops, op)
	}
	sort.SliceStable(doc.Ops, func(i, j int) bool { return doc.Ops[i].Name < doc.Ops[j].Name })
	for _, u := range m.Unattributed {
		doc.Unattributed = append(doc.Unattributed, obsBRUnattributedDoc{File: u.File, Reason: u.Reason, Commits: brCommits(u.Commits)})
	}
	return doc
}

func brCommits(cs []opindex.Commit) []obsBRCommitDoc {
	out := make([]obsBRCommitDoc, 0, len(cs))
	for _, c := range cs {
		out = append(out, obsBRCommitDoc{SHA: c.SHA, Subject: c.Subject, PR: c.PR})
	}
	return out
}

// opNames are the span names the traffic query asks for: every mapped op,
// removed ones included (a removed rpc still serving traffic is the riskiest
// change there is).
func (doc *obsBlastRadiusDoc) opNames() []string {
	names := make([]string, 0, len(doc.Ops))
	for _, op := range doc.Ops {
		names = append(names, op.Name)
	}
	return names
}

// attachTraffic hands each op its rows of res, by span name: an op name can
// run in several services, so an op gets one row per service, busiest first.
func (doc *obsBlastRadiusDoc) attachTraffic(res obsbackend.OpsResult) {
	byName := map[string][]obsBRTrafficDoc{}
	for _, s := range res.Ops {
		byName[s.Name] = append(byName[s.Name], obsBRTrafficDoc{
			Service: s.Service, Calls: s.Calls, Errors: s.Errors, ErrorRatio: s.ErrorRatio, RatePerSecond: s.RatePerSecond,
			P50Ms: s.P50Ms, P95Ms: s.P95Ms, P99Ms: s.P99Ms,
		})
	}
	for i := range doc.Ops {
		t := byName[doc.Ops[i].Name]
		if t == nil {
			continue
		}
		sort.SliceStable(t, func(a, b int) bool {
			if t[a].Calls != t[b].Calls {
				return t[a].Calls > t[b].Calls
			}
			return t[a].Service < t[b].Service
		})
		doc.Ops[i].Traffic = t
	}
}

// finish orders the ops, counts the summary and suggests next commands.
func (doc *obsBlastRadiusDoc) finish(env string) {
	ok := doc.Telemetry.Status == brTelemetryOK
	doc.Order = brOrderName
	if ok {
		doc.Order = brOrderCalls
		sort.SliceStable(doc.Ops, func(i, j int) bool {
			ci, cj := doc.Ops[i].calls(), doc.Ops[j].calls()
			if ci != cj {
				return ci > cj
			}
			return doc.Ops[i].Name < doc.Ops[j].Name
		})
	}
	s := obsBRSummaryDoc{Ops: len(doc.Ops), UnattributedFiles: len(doc.Unattributed)}
	for _, op := range doc.Ops {
		switch op.Kind {
		case opindex.KindComponent:
			s.Components++
		case opindex.KindRPC:
			s.RPCs++
		}
		if op.Removed {
			s.Removed++
		}
		s.Calls += op.calls()
		if ok {
			if len(op.Traffic) > 0 {
				s.WithTraffic++
			} else {
				s.WithoutTraffic++
			}
		}
	}
	doc.Summary = s
	doc.Suggested = blastRadiusSuggestions(env, doc.Ops)
}

// blastRadiusSuggestions are the traces of the busiest changed rpc and the
// env's component ops. Without an env there is nothing to look at.
func blastRadiusSuggestions(env string, ops []obsBROpDoc) []string {
	if env == "" {
		return nil
	}
	var cmds []string
	var component bool
	for _, op := range ops {
		component = component || op.Kind == opindex.KindComponent
	}
	// ops are busiest first when there is traffic, so the first rpc is the
	// busiest changed one.
	for _, op := range ops {
		if op.Kind != opindex.KindRPC {
			continue
		}
		argv := []string{"forge", "obs", "traces", env}
		if len(op.Traffic) > 0 {
			argv = append(argv, "--service", op.Traffic[0].Service)
		}
		cmds = append(cmds, obsout.Command(append(argv, "--route", op.Name, "--slow")...))
		break
	}
	if component {
		cmds = append(cmds, obsout.Command("forge", "obs", "ops", env, "--kind", "component"))
	}
	return obsout.Suggest(cmds...)
}

// printBlastRadius is the human form: the ops table, the unattributed files
// and a one-line summary.
func printBlastRadius(d obsDeps, doc obsBlastRadiusDoc) {
	w := d.Stdout
	traffic := doc.Telemetry.Status == brTelemetryOK
	rows := make([][]string, len(doc.Ops))
	for i, op := range doc.Ops {
		calls, errPct, p95 := "-", "-", "-"
		if traffic && len(op.Traffic) > 0 {
			var n, errs, worst float64
			for _, t := range op.Traffic {
				n, errs = n+t.Calls, errs+t.Errors
				worst = max(worst, t.P95Ms)
			}
			calls = fmt.Sprintf("%.0f", n)
			if n > 0 {
				errPct = strconv.FormatFloat(errs/n*100, 'f', 1, 64) + "%"
			}
			p95 = ms(worst)
		} else if traffic {
			calls = "0"
		}
		name := op.Name
		if op.Removed {
			name += " (removed)"
		}
		rows[i] = []string{name, op.Kind, brVias(op.Reasons), calls, errPct, p95, brChangedBy(op.Commits)}
	}
	if len(rows) > 0 {
		obsout.Table(w, []string{"OP", "KIND", "VIA", "CALLS", "ERR%", "P95", "CHANGED BY"}, rows, 60)
	} else {
		fmt.Fprintln(w, "The diff reaches no ops.")
	}
	if len(doc.Unattributed) > 0 {
		fmt.Fprintln(w, "\nunattributed:")
		urows := make([][]string, len(doc.Unattributed))
		for i, u := range doc.Unattributed {
			urows[i] = []string{u.File, u.Reason}
		}
		obsout.Table(w, []string{"FILE", "REASON"}, urows, 100)
	}
	for _, warn := range doc.Warnings {
		fmt.Fprintf(w, "\nwarning: %s\n", warn)
	}
	if len(doc.Statements) > 0 {
		fmt.Fprintln(w)
		explainText(w, doc.Statements)
	}
	s := doc.Summary
	base := doc.BaseRef
	if len(doc.Base) >= 7 {
		base += " (" + doc.Base[:7] + ")"
	}
	line := fmt.Sprintf("\n%d ops (%d rpcs, %d components, %d removed) from %d commits of %s..%s; %d files unattributed",
		s.Ops, s.RPCs, s.Components, s.Removed, brCommitsShown(doc), base, doc.HeadRef, s.UnattributedFiles)
	if doc.Truncated {
		line += fmt.Sprintf("; only the newest of %d commits mapped", doc.TotalCommits)
	}
	switch doc.Telemetry.Status {
	case brTelemetryOK:
		line += fmt.Sprintf("; %s: %d with traffic, %.0f calls since %s", doc.Telemetry.Env, s.WithTraffic, s.Calls, doc.Telemetry.From.Format(time.RFC3339))
	case brTelemetryUnavailable:
		line += fmt.Sprintf("; telemetry unavailable (%s)", doc.Telemetry.ReasonCode)
	}
	fmt.Fprintln(w, line)
}

// brCommitsShown is the commit count the summary line names: the range's
// commits, plus the working tree when it changed anything.
func brCommitsShown(doc obsBlastRadiusDoc) int {
	n := doc.TotalCommits
	if len(doc.Commits) > 0 && doc.Commits[0].SHA == "" {
		n++
	}
	return n
}

// brVias is the VIA column: the distinct ways the op was reached.
func brVias(rs []obsBRReasonDoc) string {
	var vias []string
	for _, r := range rs {
		if !slices.Contains(vias, r.Via) {
			vias = append(vias, r.Via)
		}
	}
	sort.Strings(vias)
	return emptyAs(strings.Join(vias, ","), "-")
}

// brChangedBy is the CHANGED BY column: each commit as its PR number, else
// its short sha, the newest three and a count of the rest.
func brChangedBy(cs []obsBRCommitDoc) string {
	var out []string
	for i, c := range cs {
		if i == 3 {
			out = append(out, fmt.Sprintf("+%d", len(cs)-3))
			break
		}
		switch {
		case c.SHA == "":
			out = append(out, brUncommitted)
		case c.PR > 0:
			out = append(out, "#"+strconv.Itoa(c.PR))
		case len(c.SHA) > 7:
			out = append(out, c.SHA[:7])
		default:
			out = append(out, c.SHA)
		}
	}
	return emptyAs(strings.Join(out, " "), "-")
}
