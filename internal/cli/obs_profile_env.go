package cli

// `forge obs profile <env>`: the profile of the env named, read where its
// telemetry goes (obsbackend.Resolve, as every other obs verb decides it):
// a local env's ClickStack through the statements `forge obs profile` runs,
// or a hosted env's control plane through GetFlamegraph, the console's
// flamegraph. Either way it is written as pprof, speedscope or collapsed
// stacks by pkg/profilequery.
//
// --sql with an [env] prints the statement in `forge obs query`'s parameters
// (obsQueryProfileSQL), so the profile tables can be read against any env,
// hosted included, with the scope bound by obs query.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

// runObsProfileEnv is `forge obs profile <env>`. localRunner runs a statement
// against env's local ClickStack.
func runObsProfileEnv(ctx context.Context, d obsDeps, stdout, stderr io.Writer, env string, o obsProfileOpts, localRunner func(env string) profileRunner) error {
	if o.env != "" && o.env != env {
		return obsbackend.Wrong("--env %s and [env] %s disagree: [env] already reads %s's samples alone; drop --env", o.env, env, env)
	}
	o.env, o.queryEnv = env, env
	if err := checkProfileOpts(o); err != nil {
		return err
	}
	var c obsCommon
	c.inst.all = true // profiles keep no dev instance: read the whole env
	b, dec, err := c.open(ctx, d, env, "profile")
	if err != nil {
		return err
	}
	if dec.Backend == obsbackend.BackendLocal {
		return runObsProfile(ctx, stdout, stderr, o, localRunner(env))
	}
	return runObsProfileCloud(ctx, d.Now(), stdout, stderr, o, b)
}

// runObsProfileCloud reads a hosted env's profile: the services through
// `forge obs query`'s path (a sql panel over profile_samples_1m), the stacks
// through GetFlamegraph.
func runObsProfileCloud(ctx context.Context, now time.Time, stdout, stderr io.Writer, o obsProfileOpts, b obsbackend.Backend) error {
	fr, ok := b.(obsbackend.FlamegraphReader)
	if !ok {
		return obsbackend.Undetermined("env %s's backend does not serve profiles", o.queryEnv)
	}
	if o.span != "" {
		return obsbackend.Wrong("--span: a hosted env's profile cannot be narrowed to one span; drop --span to read trace %s", o.trace)
	}
	from, to, err := profileRange(o, now)
	if err != nil {
		return err
	}
	q := profilequery.Query{Service: o.service, SampleType: o.sampleType, Env: o.env, From: from, To: to, TraceID: o.trace}

	if o.listServices || q.Service == "" {
		svcs, err := profiledServices(ctx, b, from, to)
		if err != nil {
			return err
		}
		if o.listServices {
			for _, s := range svcs {
				line, _ := json.Marshal(map[string]any{"service": s.name, "rows": s.rows})
				fmt.Fprintln(stdout, string(line))
			}
			return nil
		}
		names := make([]string, len(svcs))
		for i, s := range svcs {
			names[i] = s.name
		}
		if q.Service, err = soleService(names, from); err != nil {
			return err
		}
	}
	if o.printSQL {
		return printObsQueryProfileSQL(stdout, stderr, o, q)
	}

	res, err := fr.Flamegraph(ctx, obsbackend.FlamegraphRequest{
		Service: q.Service, SampleType: q.SampleType, TraceID: q.TraceID, Env: q.Env,
		Range: obsbackend.Range{From: from, To: to},
	})
	if err != nil {
		return err
	}
	prof := res.Profile
	if len(prof.Stacks) == 0 {
		err := noProfileSamples(o, q)
		if len(res.AvailableTypes) > 0 && !slices.Contains(res.AvailableTypes, q.SampleType) {
			err = fmt.Errorf("%w. Service %s has these sample types in the range: %s (--type)", err, q.Service, strings.Join(res.AvailableTypes, ", "))
		}
		return err
	}
	if q.Env != "" && !res.EnvApplied {
		fmt.Fprintf(stderr, "note: env %s's control plane cannot narrow a profile to one environment yet: these are service %s's samples in every environment of the organization\n", o.queryEnv, q.Service)
	}
	if res.Truncated {
		fmt.Fprintf(stderr, "note: the control plane kept the heaviest %d stacks and cut the rest (total %d %s; these stacks hold %d)\n", len(prof.Stacks), res.TotalValue, prof.Unit, prof.Total())
	}
	return writeProfile(stdout, stderr, o, prof)
}

// profiledService is one row of the services statement.
type profiledService struct {
	name string
	rows any
}

// profiledServices lists the services that sent profiles in the range, read
// as `forge obs query` reads its SQL.
func profiledServices(ctx context.Context, b obsbackend.Backend, from, to time.Time) ([]profiledService, error) {
	sql, err := obsQueryProfileStatement(profilequery.ServicesSQL, profilequery.Query{})
	if err != nil {
		return nil, err
	}
	res, err := b.Query(ctx, obsbackend.QueryRequest{Narrow: obsbackend.Narrow{Range: obsbackend.Range{From: from, To: to}}, SQL: sql, Limit: 200})
	if err != nil {
		return nil, err
	}
	svc, n := -1, -1
	for i, c := range res.Columns {
		switch c.Name {
		case "service":
			svc = i
		case "rows":
			n = i
		}
	}
	if svc < 0 {
		return nil, obsbackend.Undetermined("the services query answered without a service column")
	}
	var out []profiledService
	for _, r := range res.Rows {
		if svc >= len(r) {
			continue
		}
		s := profiledService{name: fmt.Sprint(r[svc])}
		if n >= 0 && n < len(r) {
			s.rows = r[n]
		}
		out = append(out, s)
	}
	return out, nil
}

// printObsQueryProfileSQL prints q's statement for `forge obs query`, and on
// stderr the command that runs it.
func printObsQueryProfileSQL(stdout, stderr io.Writer, o obsProfileOpts, q profilequery.Query) error {
	sql, err := obsQueryProfileSQL(q)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, sql)
	argv := []string{"forge", "obs", "query", o.queryEnv, "--sql-file", "profile.sql"}
	if q.Service != "" {
		argv = append(argv, "--service", q.Service)
	}
	if o.from != "" {
		argv = append(argv, "--from", o.from)
	} else {
		argv = append(argv, "--since", o.since.String())
	}
	if o.to != "" {
		argv = append(argv, "--to", o.to)
	}
	fmt.Fprintf(stderr, "-- save the statement as profile.sql, then run it: %s\n", obsbackend.Cmd(argv...))
	return nil
}

var (
	// profileTypeRe is a sample type: what profilequery and the control
	// plane name them (cpu, inuse_space, alloc_objects, goroutine, ...).
	profileTypeRe = regexp.MustCompile(`^[a-z][a-z_]{0,63}$`)
	// profileSpanIDRe is a span id, hex.
	profileSpanIDRe = regexp.MustCompile(`^[0-9a-fA-F]{1,32}$`)
	// profileParamEq is a comparison with the service or env parameter:
	// `a.ServiceName = {svc:String}`, `ResourceAttributes['deployment.environment.name'] = {env:String}`.
	profileParamEq = regexp.MustCompile(`([A-Za-z_][\w.]*(?:\['[\w.]+'\])?) = \{(svc|env):String\}`)
)

// obsQueryProfileSQL is q's statement in `forge obs query`'s parameters.
func obsQueryProfileSQL(q profilequery.Query) (string, error) {
	sql, _ := q.SQL()
	return obsQueryProfileStatement(sql, q)
}

// obsQueryProfileStatement rewrites one of pkg/profilequery's statements (the
// ones the local export runs) into `forge obs query`'s vocabulary, so it runs
// against any env, hosted included:
//
//   - the tables are named unqualified (otel_profiles, profile_samples_1m,
//     profile_stacks): obs query reads an env's own database, where a hosted
//     env's `default.` names a database it cannot read;
//   - FORMAT goes (obs query refuses it; the client picks the format);
//   - the range is obs query's {from} / {to}, and the snapshot lookback and
//     width are derived from them;
//   - the service is obs query's {services} (--service), the env its {envs}
//     (the env being read);
//   - the sample type, trace and span, which obs query has no parameter for,
//     are literals, each checked against its alphabet first.
func obsQueryProfileStatement(sql string, q profilequery.Query) (string, error) {
	lit := func(s string) string { return "'" + s + "'" }
	typ, trace := "", ""
	if q.SampleType != "" {
		if !profileTypeRe.MatchString(q.SampleType) {
			return "", obsbackend.Wrong("--type %q is not a sample type (cpu, inuse_space, alloc_objects, goroutine, ...)", q.SampleType)
		}
		typ = q.SampleType
	}
	if q.TraceID != "" {
		id, err := obsquery.NormalizeTraceID(q.TraceID)
		if err != nil {
			return "", obsbackend.Wrong("--trace %q: %v", q.TraceID, err)
		}
		trace = id
	}
	if q.SpanID != "" && !profileSpanIDRe.MatchString(q.SpanID) {
		return "", obsbackend.Wrong("--span %q is not a span id (hex)", q.SpanID)
	}
	sql = strings.NewReplacer(
		"default.", "",
		"FORMAT JSONEachRow", "",
		"{from:UInt32}", "{from:DateTime64(9)}",
		"{to:UInt32}", "{to:DateTime64(9)}",
		"{since:UInt32}", fmt.Sprintf("{to:DateTime64(9)} - INTERVAL %d SECOND", int64(profilequery.SnapshotLookback/time.Second)),
		"{width:UInt32}", "dateDiff('second', {from:DateTime64(9)}, {to:DateTime64(9)})",
		"{svc:String} = ''", "empty({services:Array(String)})",
		"{env:String} = ''", "empty({envs:Array(String)})",
		"{type:String}", lit(typ),
		"{trace:String}", lit(trace),
		"{span:String}", lit(q.SpanID),
	).Replace(sql)
	sql = profileParamEq.ReplaceAllStringFunc(sql, func(m string) string {
		sub := profileParamEq.FindStringSubmatch(m)
		param := "services"
		if sub[2] == "env" {
			param = "envs"
		}
		return "has({" + param + ":Array(String)}, " + sub[1] + ")"
	})
	sql = strings.TrimSpace(sql)
	// Every parameter left must be one obs query binds: a statement that
	// still names another would fail there with an unknown parameter.
	for _, u := range dashspec.UsedSQLParams(sql) {
		switch u.Name {
		case dashspec.SQLParamFrom, dashspec.SQLParamTo, dashspec.SQLParamServices, dashspec.SQLParamEnvs:
		default:
			return "", fmt.Errorf("internal: the profile statement still names {%s:%s}, which forge obs query does not bind", u.Name, u.Type)
		}
	}
	if strings.Contains(sql, "default.") || strings.Contains(sql, "FORMAT") {
		return "", fmt.Errorf("internal: the profile statement was not rewritten for forge obs query")
	}
	return sql, nil
}
