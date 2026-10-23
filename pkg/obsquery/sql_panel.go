package obsquery

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// MaxSQLRows is the most rows a sql panel may return. Like an alert rule's
// cap it throws: a longer result is an error, never a silently cut one.
const MaxSQLRows = 10_000

// noEnv is the env_ids value bound when the scope is restricted to no
// environment at all, so `has({env_ids:Array(String)}, …)` matches nothing and
// `empty(…)` is false: never a real reliant.env.id (and never "", which rows
// without an environment would match).
const noEnv = "(none)"

// validateSQL checks a sql panel: its statement is the alert-rule guard's (one
// read-only SELECT), and it carries nothing a statement would ignore.
func (p Panel) validateSQL() error {
	if p.Signal != "" {
		if _, err := PanelMaxRange(p.Signal); err != nil {
			return err
		}
	}
	if len(p.Series) > 0 || p.Ratio || p.Formula != "" || p.Limit != 0 || len(p.Where) > 0 || len(p.GroupBy) > 0 || p.Funnel != nil || p.HasError {
		return invalid("a sql panel takes only its sql: no series, ratio, formula, limit, where, group_by, funnel or has_error")
	}
	if strings.TrimSpace(p.SQL) == "" {
		return invalid("a sql panel needs sql: one read-only SELECT")
	}
	if err := dashspec.CheckSQL(p.SQL); err != nil {
		return invalid("sql: %v", err)
	}
	return nil
}

// SQLPanel is a sql panel's statement bound to a VERIFIED scope (the backend's
// own resolved scope, e.g. the control plane's org gate; never a request
// field): the dashboard's range, step,
// environments and services become the forge dashspec.SQLParam* ClickHouse
// parameters. Nothing is spliced into the statement, which runs as the caller
// wrote it; the parameters are values the server parses by the type the
// statement declares.
type SQLPanel struct {
	Scope       Scope
	SQL         string
	StepSeconds int64
}

// Compile checks the statement with the alert-rule guard and binds the scope.
//
//	from, to   the range as Unix seconds with nine decimals, which a
//	           DateTime64(9) parameter parses the same in every session
//	           timezone (a "YYYY-MM-DD hh:mm:ss" text would be read in the
//	           session's)
//	step       seconds
//	env_ids    the environments in scope; [] when unrestricted, ["(none)"]
//	           when restricted to none
//	envs       their names, in env_ids order
//	services   the services in scope; [] = all
func (q SQLPanel) Compile() (Stmt, error) {
	if err := dashspec.CheckSQL(q.SQL); err != nil {
		return Stmt{}, invalid("sql: %v", err)
	}
	if err := q.Scope.checkDatabase(); err != nil {
		return Stmt{}, err
	}
	if q.StepSeconds < 1 {
		return Stmt{}, invalid("step must be at least 1 second")
	}
	names := make(map[string]string, len(q.Scope.Envs))
	for _, e := range q.Scope.Envs {
		names[e.ID] = e.Name
	}
	var ids, envs []string
	if q.Scope.EnvRestricted {
		ids = q.Scope.EnvIDs
		if len(ids) == 0 {
			ids = []string{noEnv}
		}
		for _, id := range ids {
			envs = append(envs, names[id])
		}
	}
	return Stmt{SQL: q.SQL, Params: map[string]string{
		dashspec.SQLParamFrom:     unixNanoText(q.Scope.Start.UnixNano()),
		dashspec.SQLParamTo:       unixNanoText(q.Scope.End.UnixNano()),
		dashspec.SQLParamStep:     strconv.FormatInt(q.StepSeconds, 10),
		dashspec.SQLParamEnvIDs:   stringArrayText(ids),
		dashspec.SQLParamEnvs:     stringArrayText(envs),
		dashspec.SQLParamServices: stringArrayText(q.Scope.Services),
	}}, nil
}

// unixNanoText renders a Unix-nanosecond time as "<seconds>.<9 digits>".
func unixNanoText(ns int64) string {
	sec, frac := ns/1e9, ns%1e9
	if frac < 0 {
		sec, frac = sec-1, frac+1e9
	}
	return fmt.Sprintf("%d.%09d", sec, frac)
}

// sqlStringEscaper escapes a value for a single-quoted string in ClickHouse's
// text format, which is how an Array(String) parameter is parsed.
var sqlStringEscaper = strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "\x00", `\0`)

// stringArrayText renders vals as an Array(String) parameter: ['a', 'b'].
func stringArrayText(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = "'" + sqlStringEscaper.Replace(v) + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
