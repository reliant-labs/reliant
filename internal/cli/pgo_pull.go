package cli

// `forge pgo pull`: one default.pgo per Go binary, from the env whose
// profiles that binary's PGO names (Contract F).
//
// Failing gracefully is the contract: a pull never damages a file. A binary
// is written (atomically: a sibling temp file renamed over the target) only
// once its profile was read, parsed and found heavy enough; anything else —
// disabled, no data, below min_weight, a source that cannot be read — is a
// note naming why and the fix, and the existing file is left as it was.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

type pgoPullOpts struct {
	env      string
	binaries []string
	since    time.Duration
	fromEnv  string
	dryRun   bool
	jsonOut  bool
}

func newPGOPullCmd() *cobra.Command {
	var o pgoPullOpts
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Write cmd/<bin>/default.pgo for each Go binary from an env's CPU profiles",
		Long: `Merge an env's recent CPU profiles into cmd/<bin>/default.pgo, one file per Go
binary the env builds. Commit the file: Go's default -pgo=auto reads it in every
` + "`go build ./cmd/<bin>`" + ` (the Dockerfile, host builds, air), so builds are optimized
for how the binary really runs and never fetch anything themselves.

One binary, one profile: a binary that runs as several workloads (a modular
monolith serving as server, worker and cron) gets the CPU of all of them,
merged. The workloads come from the rendered env; ` + "`pgo.services`" + ` overrides them.

Config is read from --env's KCL (default prod):
` + "`pgo = forge.PGO {from_env, since, min_weight, services, enabled}`" + ` on a GoBuild,
or DockerBuild.pgo keyed by binary.
A ShellBuild's binaries are pulled only when its ` + "`pgo`" + ` names them (forge.ShellPGO,
keyed by package relative to its cwd), which also says where the profile goes
(` + "`out`" + `, e.g. a sibling checkout's ../reliant/cmd/reliant/default.pgo) and whose
service.namespace to read.
--since and --from-env override it for this run. Profiles come from the local
ClickStack when the source env's telemetry is local, else from its control
plane (needs sign-in and obs:read); both produce the same bytes.

A binary is skipped with a note — its existing file untouched — when PGO is
off for it, the window holds no profile data, or its edge weight (sampled CPU
summed over call edges, not unique CPU time) is below min_weight. A source that cannot be read (ClickStack not running, not signed
in, a control plane without ExportPGOProfile, a ClickStack without the
profile_pgo_edges table) is a per-binary failure, also leaving the file
untouched.

Exit status: 0 when every binary was written or skipped with a reason; 2 when
a source could not be read; 1 for a usage error or a file that could not be
written.

--json prints one document (schema forge.pgo.pull/v1): per binary its package,
path, action (written, would_write with --dry-run, skipped, failed), reason,
edges, dropped, total_weight_ns (the edge weight), oldest, newest, services
and source (local|cloud).`,
		Example: `  forge pgo pull                          # every Go binary, config from prod's KCL
  git add cmd/*/default.pgo && git commit -m "pgo: refresh profiles"

  forge pgo pull --binary ./cmd/api       # one binary
  forge pgo pull --since 72h --dry-run    # what a 3-day window would write
  forge pgo pull --env staging --from-env prod --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := defaultPGODeps(cmd, "pgo pull")
			if err != nil {
				return err
			}
			return runPGOPull(cmd.Context(), d, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.env, "env", "", "the env whose KCL build config to read (default: prod)")
	f.StringArrayVar(&o.binaries, "binary", nil, "only this Go binary, e.g. ./cmd/api (repeatable; default: every binary the env builds)")
	obsDurationVar(f, &o.since, "since", 0, "the window to merge, up to 30d (default: the binary's pgo.since, 168h)")
	f.StringVar(&o.fromEnv, "from-env", "", "read profiles from this env (default: the binary's pgo.from_env, else --env)")
	f.BoolVar(&o.dryRun, "dry-run", false, "resolve and query, but write nothing")
	f.BoolVar(&o.jsonOut, "json", false, "print the result as a JSON document")
	return cmd
}

// pgoPullSchema names the --json document.
const pgoPullSchema = "forge.pgo.pull/v1"

// What happened to one binary.
const (
	pgoWritten    = "written"
	pgoWouldWrite = "would_write"
	pgoSkipped    = "skipped"
	pgoFailed     = "failed"
)

// pgoPullDoc is the --json document.
type pgoPullDoc struct {
	Schema   string          `json:"schema"`
	Env      string          `json:"env"`
	DryRun   bool            `json:"dry_run"`
	Binaries []pgoPullResult `json:"binaries"`
	ExitCode int             `json:"exit_code"`
}

// pgoPullResult is one binary's outcome.
type pgoPullResult struct {
	Package string `json:"package"`
	Path    string `json:"path"`
	Action  string `json:"action"`
	// Reason says why a binary was skipped or failed, and the fix.
	Reason  string `json:"reason,omitempty"`
	FromEnv string `json:"from_env,omitempty"`
	// Namespace is the OTEL service.namespace the profiles were read from.
	Namespace string   `json:"namespace,omitempty"`
	Source    string   `json:"source,omitempty"`
	Services  []string `json:"services"`
	// From and To are the window queried.
	From  *time.Time `json:"from,omitempty"`
	To    *time.Time `json:"to,omitempty"`
	Edges int        `json:"edges"`
	// Dropped counts edges left out because a function name cannot travel
	// through go tool preprofile (profilequery.PGOProfile.Dropped).
	Dropped int `json:"dropped,omitempty"`
	// TotalWeightNS is the edge weight: sampled CPU summed over call edges,
	// what min_weight is compared with (profilequery.PGOProfile.TotalWeight).
	TotalWeightNS int64 `json:"total_weight_ns"`
	MinWeightNS   int64 `json:"min_weight_ns"`
	// Oldest and Newest are the start of the oldest and newest hour that
	// contributed an edge.
	Oldest *time.Time `json:"oldest,omitempty"`
	Newest *time.Time `json:"newest,omitempty"`

	// exit is the status a failure asks for: 2 when the source could not
	// be read, 1 otherwise.
	exit int
}

func runPGOPull(ctx context.Context, d pgoDeps, o pgoPullOpts) error {
	if o.since < 0 {
		return fmt.Errorf("--since %s: want a positive window", o.since)
	}
	if o.since > pgoMaxSince {
		return fmt.Errorf("--since %s is longer than %s, how long forge keeps profile edges", pgoDuration(o.since), pgoDuration(pgoMaxSince))
	}
	env, err := pgoEnv(d, o.env)
	if err != nil {
		return err
	}
	bins, project, err := pgoBinaries(ctx, d, env, o.binaries)
	if err != nil {
		return err
	}
	p := &pgoPuller{d: d, o: o, env: env, project: project, now: d.Now(), sources: map[string]*pgoSource{}, renders: map[string]pgoRender{}}
	doc := pgoPullDoc{Schema: pgoPullSchema, Env: env, DryRun: o.dryRun, Binaries: []pgoPullResult{}}
	for _, b := range bins {
		doc.Binaries = append(doc.Binaries, p.pull(ctx, b))
	}
	doc.ExitCode = pgoPullExit(doc.Binaries)

	if o.jsonOut {
		enc := json.NewEncoder(d.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return err
		}
	} else {
		printPGOPull(d.Stdout, doc)
	}
	if doc.ExitCode == 0 {
		return nil
	}
	var failed []string
	for _, r := range doc.Binaries {
		if r.Action == pgoFailed {
			failed = append(failed, strings.TrimPrefix(r.Package, "./"))
		}
	}
	return exitCodeError{code: doc.ExitCode, msg: fmt.Sprintf("pgo pull: %d of %d binaries could not be pulled (%s); their existing profiles are untouched",
		len(failed), len(doc.Binaries), strings.Join(failed, ", "))}
}

// pgoPullExit is the run's status: 2 when any source could not be read, 1
// when any other failure happened, else 0.
func pgoPullExit(rs []pgoPullResult) int {
	code := 0
	for _, r := range rs {
		if r.Action == pgoFailed && r.exit > code {
			code = r.exit
		}
	}
	return code
}

// pgoPuller carries one run's state: the sources and the from_env renders
// already opened, shared by every binary that reads them.
type pgoPuller struct {
	d   pgoDeps
	o   pgoPullOpts
	env string
	// project is env's Bundle.project: the service.namespace its workloads
	// report, so a read never merges another project's same-named services.
	project string
	now     time.Time
	sources map[string]*pgoSource
	renders map[string]pgoRender
}

type pgoRender struct {
	bins    map[string]PGOBinary
	project string
	err     error
}

func (p *pgoPuller) pull(ctx context.Context, b PGOBinary) pgoPullResult {
	r := pgoPullResult{Package: b.Package, Path: b.ProfilePath(), Services: []string{}, MinWeightNS: b.PGO.MinWeight.Nanoseconds()}
	skip := func(format string, a ...any) pgoPullResult {
		r.Action, r.Reason = pgoSkipped, fmt.Sprintf(format, a...)
		return r
	}
	fail := func(exit int, err error) pgoPullResult {
		r.Action, r.Reason, r.exit = pgoFailed, err.Error(), exit
		return r
	}
	if !b.PGO.Enabled {
		return skip("PGO is off for %s in env %s (pgo.enabled = False): its builds pass -pgo=off", b.Package, p.env)
	}
	if b.PGO.Out == "" && !strings.HasPrefix(b.Package, "./") {
		return skip("%s is not a package of this project, so forge has nowhere to write its profile", b.Package)
	}
	target := resolveProjectPath(p.d.ProjectDir, filepath.FromSlash(r.Path))
	if b.PGO.Out != "" {
		// A ShellBuild's profile may live in a sibling checkout, but no
		// further: the same reach its inputs have (shellBuildInputScope).
		if !pgoWithinWorkspace(p.d.ProjectDir, target) {
			return skip("%s is outside the directory holding this project, so pull will not write it; name a path in this project or a sibling checkout with ShellPGO.out", r.Path)
		}
		if info, err := os.Stat(filepath.Dir(target)); err != nil || !info.IsDir() {
			return skip("%s has no directory on disk (is the checkout it belongs to cloned beside this project?), so there is nowhere to write its profile", path.Dir(r.Path))
		}
	} else if info, err := os.Stat(filepath.Dir(target)); err != nil || !info.IsDir() {
		return skip("%s has no directory in this checkout, so there is no main package to profile", b.Package)
	}
	if note, ok := pgoForeignFile(target); !ok {
		return skip("%s %s; pull only replaces a profile it wrote — delete it (or move it aside) to let pull manage it", r.Path, note)
	}

	r.FromEnv = b.PGO.FromEnv
	if p.o.fromEnv != "" {
		r.FromEnv = p.o.fromEnv
	}
	since := b.PGO.Since
	if p.o.since > 0 {
		since = p.o.since
	}
	from, to := p.now.Add(-since), p.now
	r.From, r.To = &from, &to

	services, namespace := b.Services, p.project
	if len(b.PGO.Services) == 0 && r.FromEnv != p.env {
		// b.Services are this env's workloads; the profiles are another
		// env's, whose workloads running the binary may be named differently.
		rd := p.render(ctx, r.FromEnv)
		if rd.err != nil {
			return fail(2, fmt.Errorf("cannot read env %s's declaration to find the workloads that run %s: %w", r.FromEnv, b.Package, rd.err))
		}
		other, ok := rd.bins[b.Package]
		if !ok || len(other.Workloads) == 0 {
			return skip("no workload of env %s runs %s, so it has no profiles there; name the OTEL services to merge with pgo.services", r.FromEnv, b.Package)
		}
		services = other.Workloads
		if rd.project != "" {
			namespace = rd.project
		}
	}
	if b.PGO.Namespace != "" {
		namespace = b.PGO.Namespace
	}
	r.Namespace = namespace
	if len(services) == 0 {
		return skip("no workload of env %s runs %s, so it has no profiles; name the OTEL services to merge with pgo.services", r.FromEnv, b.Package)
	}
	r.Services = append([]string(nil), services...)

	q := profilequery.PGOQuery{Services: r.Services, Namespace: namespace, Env: r.FromEnv, From: from, To: to}
	if err := q.Validate(); err != nil {
		return fail(1, err)
	}
	src := p.source(ctx, r.FromEnv)
	if src.err != nil {
		return fail(2, src.err)
	}
	r.Source = src.kind
	prof, raw, err := src.fetch(ctx, q)
	if errors.Is(err, profilequery.ErrTooManyEdges) {
		// The source answered; the window is too wide to merge.
		return fail(1, err)
	}
	if err != nil {
		return fail(2, err)
	}
	r.Edges, r.Dropped, r.TotalWeightNS = len(prof.Edges), prof.Dropped, prof.TotalWeight()
	r.Oldest, r.Newest = pgoTime(prof.Oldest), pgoTime(prof.Newest)
	if len(prof.Edges) == 0 {
		return skip("no CPU profile edges from %s in env %s over the last %s: profiles arrive every few minutes once a workload runs with an OTLP endpoint, "+
			"and only from a pkg/observe that records PGO start lines (see `forge skill load observability/profiles`)",
			strings.Join(r.Services, ", "), r.FromEnv, pgoDuration(since))
	}
	if r.TotalWeightNS < r.MinWeightNS {
		return skip("only %s of edge weight (sampled CPU summed over call edges) in %s from %s; needs %s (pgo.min_weight) — widen the window with --since, or lower min_weight",
			pgoDuration(time.Duration(r.TotalWeightNS)), pgoDuration(since), r.FromEnv, pgoDuration(b.PGO.MinWeight))
	}
	if raw == nil {
		var buf bytes.Buffer
		if err := profilequery.WritePGO(&buf, prof); err != nil {
			return fail(1, err)
		}
		raw = buf.Bytes()
	}
	if p.o.dryRun {
		r.Action = pgoWouldWrite
		return r
	}
	if err := atomicfile.WriteFile(target, raw, 0o644); err != nil {
		return fail(1, fmt.Errorf("write %s: %w", r.Path, err))
	}
	r.Action = pgoWritten
	return r
}

// pgoWithinWorkspace reports whether target lies under the directory holding
// the project: the project itself and its sibling checkouts. Both sides are
// compared after resolving symlinks, so a symlinked directory (or a symlink
// at target itself) that points outside the workspace is refused: a write
// through it would land wherever the link points.
func pgoWithinWorkspace(projectDir, target string) bool {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return false
	}
	t, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	t, err = resolveExisting(t)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, t)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolveExisting resolves symlinks in p's deepest existing ancestor (p
// itself when it exists) and appends the parts that do not exist yet, which
// a later write creates as plain directories and files.
func resolveExisting(p string) (string, error) {
	var rest []string
	for cur := p; ; {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// pgoForeignFile reports whether the file at target may be replaced: absent,
// or a profile `forge pgo pull` wrote. Otherwise note says what it is.
func pgoForeignFile(target string) (note string, ok bool) {
	f, err := os.Open(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", true
	}
	if err != nil {
		return "cannot be read (" + err.Error() + ")", false
	}
	defer func() { _ = f.Close() }()
	if _, err := profilequery.ReadPGO(f); err != nil {
		if errors.Is(err, profilequery.ErrNotForgePGO) {
			return "was not written by forge (a hand-captured profile?)", false
		}
		return "is not a readable profile (" + err.Error() + ")", false
	}
	return "", true
}

// render is env's binaries, rendered once per run.
func (p *pgoPuller) render(ctx context.Context, env string) pgoRender {
	if rd, ok := p.renders[env]; ok {
		return rd
	}
	rd := pgoRender{bins: map[string]PGOBinary{}}
	e, err := p.d.Render(ctx, p.d.ProjectDir, env)
	if err == nil {
		rd.project = e.Project
		var bins []PGOBinary
		if bins, err = ResolvePGOBinaries(e); err == nil {
			for _, b := range bins {
				rd.bins[b.Package] = b
			}
		}
	}
	rd.err = err
	p.renders[env] = rd
	return rd
}

// pgoSource is where one env's profiles are read: its local ClickStack or
// its control plane. err is why it cannot be read at all.
type pgoSource struct {
	env   string
	kind  string // obsbackend.BackendLocal or obsbackend.BackendCloud
	run   profileRunner
	cloud obsbackend.Cloud
	err   error
}

// source opens env's profile store once per run, the way every `forge obs`
// verb decides it (obsbackend.Resolve).
func (p *pgoPuller) source(ctx context.Context, env string) *pgoSource {
	if s, ok := p.sources[env]; ok {
		return s
	}
	s := &pgoSource{env: env}
	p.sources[env] = s
	facts, err := p.d.Facts(ctx, p.d.Stderr, env)
	if err != nil {
		s.err = err
		return s
	}
	dec, err := obsbackend.Resolve(facts, "")
	if err != nil {
		s.err = err
		return s
	}
	s.kind = dec.Backend
	if dec.Backend == obsbackend.BackendLocal {
		s.run = p.d.Local(env)
		return s
	}
	if rec := dec.HostedDev; rec != nil {
		s.cloud, s.err = p.d.HostedDev(ctx, env, *rec)
	} else {
		s.cloud, s.err = p.d.Cloud(ctx, env)
	}
	return s
}

// fetch reads q's edges. raw is the pprof the control plane wrote (nil for a
// local read, whose bytes WritePGO produces).
func (s *pgoSource) fetch(ctx context.Context, q profilequery.PGOQuery) (profilequery.PGOProfile, []byte, error) {
	if s.kind == obsbackend.BackendLocal {
		sql, params := q.SQL()
		out, err := s.run(ctx, sql, params)
		if err != nil {
			return profilequery.PGOProfile{}, nil, pgoLocalError(ctx, s.env, err)
		}
		prof, err := profilequery.ParsePGO(bytes.NewReader(out), q)
		if errors.Is(err, profilequery.ErrTooManyEdges) {
			return profilequery.PGOProfile{}, nil, err
		}
		if err != nil {
			return profilequery.PGOProfile{}, nil, fmt.Errorf("the local ClickStack's answer could not be read: %w", err)
		}
		return prof, nil, nil
	}
	return s.fetchCloud(ctx, q)
}

// pgoClientScript runs the statement with clickhouse-client inside the
// ClickStack container, with the credentials it already holds. The SQL and
// every parameter travel as positional arguments, never interpolated.
const pgoClientScript = `q="$1"; shift; exec clickhouse-client -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" ` +
	`--output_format_json_quote_64bit_integers=0 --session_timezone=UTC --query "$q" "$@"`

// pgoLocalRunner runs a statement against env's local ClickStack, in the
// compose project `forge env up <env>` started it in.
func pgoLocalRunner(env string) profileRunner {
	run := localClickstackExec(env)
	return func(ctx context.Context, sql string, params map[string]string) ([]byte, error) {
		return run(ctx, pgoClientScript, append([]string{sql}, obsbackend.ParamArgs(params)...))
	}
}

// pgoLocalError names a local read's failure and its fix.
func pgoLocalError(ctx context.Context, env string, err error) error {
	up := "forge env up " + env
	if ctx.Err() != nil {
		return obsbackend.Undetermined("the query was cancelled: %v", ctx.Err())
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "profile_pgo_edges") && (strings.Contains(msg, "UNKNOWN_TABLE") || strings.Contains(msg, "doesn't exist") || strings.Contains(msg, "does not exist")):
		return obsbackend.Undetermined("env %s's local ClickStack has no default.profile_pgo_edges table: this project's profiles schema predates PGO; "+
			"run `forge project upgrade` (see `forge skill load migrations/v1.8.0/profile-guided-builds`) then `%s`", env, up).With(up)
	}
	var be *obsbackend.Error
	if errors.As(err, &be) {
		return err
	}
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return obsbackend.Undetermined("docker is not installed or not on PATH, so env %s's local ClickStack cannot be reached", env).With(up)
	case strings.Contains(msg, "is not running"), strings.Contains(msg, "no such service"), strings.Contains(msg, "No such container"),
		strings.Contains(msg, "Cannot connect to the Docker daemon"), strings.Contains(msg, "no configuration file provided"):
		return obsbackend.Undetermined("env %s's local ClickStack is not running — start it with `%s`", env, up).With(up)
	}
	return obsbackend.Undetermined("env %s's local ClickStack did not answer: %v", env, err)
}

// fetchCloud reads q through the control plane's ExportPGOProfile. The bytes
// it returns are the server's own WritePGO output, written as they are.
func (s *pgoSource) fetchCloud(ctx context.Context, q profilequery.PGOQuery) (profilequery.PGOProfile, []byte, error) {
	res, err := s.cloud.ExportPGOProfile(ctx, obsbackend.ExportPGORequest{
		Services: q.Services, Namespace: q.Namespace, Env: q.Env, Range: obsbackend.Range{From: q.From, To: q.To},
	})
	if err != nil {
		return profilequery.PGOProfile{}, nil, err
	}
	if res.EdgeCount == 0 && len(res.Pprof) == 0 {
		return profilequery.PGOProfile{Services: q.Services, Namespace: q.Namespace, Env: q.Env, From: q.From, To: q.To}, nil, nil
	}
	prof, err := profilequery.ReadPGO(bytes.NewReader(res.Pprof))
	if err != nil {
		return profilequery.PGOProfile{}, nil, obsbackend.Undetermined("env %s's control plane answered with a profile forge cannot read", s.env).Cause(err)
	}
	return prof, res.Pprof, nil
}

// printPGOPull is the human report: one block per binary, then the one
// reminder that matters.
func printPGOPull(w io.Writer, doc pgoPullDoc) {
	verb := "pulling"
	if doc.DryRun {
		verb = "dry run"
	}
	fmt.Fprintf(w, "pgo %s: config from env %s, %d Go binar%s\n", verb, doc.Env, len(doc.Binaries), plural(len(doc.Binaries), "y", "ies"))
	if len(doc.Binaries) == 0 {
		fmt.Fprintf(w, "  env %s builds no Go binary\n", doc.Env)
		return
	}
	var written []string
	for _, r := range doc.Binaries {
		switch r.Action {
		case pgoWritten, pgoWouldWrite:
			head := "wrote"
			if r.Action == pgoWouldWrite {
				head = "would write"
			}
			fmt.Fprintf(w, "  %s %s: %d edge%s, %s edge weight\n", head, r.Path, r.Edges, plural(r.Edges, "", "s"), pgoDuration(time.Duration(r.TotalWeightNS)))
			if r.Dropped > 0 {
				fmt.Fprintf(w, "      %d edge%s left out: a function name with a line break or over %d KiB, which go tool preprofile cannot carry\n",
					r.Dropped, plural(r.Dropped, "", "s"), (profilequery.MaxPGONameBytes+1)>>10)
			}
			fmt.Fprintf(w, "      from env %s (%s), services %s\n", r.FromEnv, r.Source, strings.Join(r.Services, ", "))
			if r.Oldest != nil && r.Newest != nil {
				fmt.Fprintf(w, "      data %s → %s\n", r.Oldest.UTC().Format("2006-01-02 15:04Z"), r.Newest.Add(time.Hour).UTC().Format("2006-01-02 15:04Z"))
			}
			if r.Action == pgoWritten {
				written = append(written, r.Path)
			}
		case pgoSkipped:
			fmt.Fprintf(w, "  skipped %s: %s\n", r.Package, r.Reason)
		default:
			fmt.Fprintf(w, "  FAILED %s: %s\n", r.Package, r.Reason)
		}
	}
	// A profile outside the project (a ShellBuild's sibling checkout) is
	// committed in its own repository, not with this project's git add.
	var here, elsewhere []string
	for _, p := range written {
		if p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
			elsewhere = append(elsewhere, p)
		} else {
			here = append(here, p)
		}
	}
	if len(here) > 0 {
		fmt.Fprintf(w, "\nCommit the profile%s so every build uses %s (Go's -pgo=auto reads it):\n  git add %s && git commit -m \"pgo: refresh profiles\"\n",
			plural(len(here), "", "s"), plural(len(here), "it", "them"), strings.Join(here, " "))
	}
	for _, p := range elsewhere {
		fmt.Fprintf(w, "\nCommit %s in the checkout it belongs to:\n  git -C %s add %s && git -C %s commit -m \"pgo: refresh profiles\"\n",
			p, path.Dir(p), path.Base(p), path.Dir(p))
	}
}
