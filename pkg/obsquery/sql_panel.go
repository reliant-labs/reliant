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

// EnvIDParam is the ClickHouse parameter an alert rule's expr names the id of
// the env it is declared for by: {reliant_env_id:String}, forge's
// EnvIDPlaceholder. An alert evaluator binds it to that env's id. A sql panel
// binds it to the ONE environment its scope is restricted to, so a rule's query
// runs as written in a dashboard, the metrics explorer and `forge obs query`.
// A scope over several environments, every environment, or none leaves it
// unbound and Compile refuses a statement that uses it: it names one
// environment, and guessing which would read the wrong data.
const EnvIDParam = "reliant_env_id"

// errEnvIDUnbound is the refusal for a statement that names EnvIDParam over a
// scope that is not exactly one environment.
const errEnvIDUnbound = "the statement names {" + EnvIDParam + ":String}, the id of one environment, and is bound only when exactly one environment is in scope; choose a single environment"

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
//	reliant_env_id
//	           the one environment in scope (EnvIDParam); absent otherwise,
//	           and a statement that uses it is refused
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
	params := map[string]string{
		dashspec.SQLParamFrom:     unixNanoText(q.Scope.Start.UnixNano()),
		dashspec.SQLParamTo:       unixNanoText(q.Scope.End.UnixNano()),
		dashspec.SQLParamStep:     strconv.FormatInt(q.StepSeconds, 10),
		dashspec.SQLParamEnvIDs:   stringArrayText(ids),
		dashspec.SQLParamEnvs:     stringArrayText(envs),
		dashspec.SQLParamServices: stringArrayText(q.Scope.Services),
	}
	switch {
	case q.Scope.EnvRestricted && len(q.Scope.EnvIDs) == 1:
		// A String parameter is parsed in ClickHouse's escaped text format.
		params[EnvIDParam] = sqlStringEscaper.Replace(q.Scope.EnvIDs[0])
	case usesParam(q.SQL, EnvIDParam):
		return Stmt{}, invalid("%s", errEnvIDUnbound)
	}
	return Stmt{SQL: q.SQL, Params: params}, nil
}

// usesParam reports whether sql uses the ClickHouse parameter name ({name:Type},
// spaces allowed inside the braces). One in a comment, a string literal or a
// quoted identifier is not a use: ClickHouse never binds it. The statement has
// already passed the guard, so every comment and quote is terminated.
func usesParam(sql, name string) bool {
	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-', c == '#':
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return false
			}
			i += 2 + end + 2
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(sql, i)
		case c == '{':
			end := strings.IndexByte(sql[i:], '}')
			if end < 0 {
				return false
			}
			if key, _, ok := strings.Cut(sql[i+1:i+end], ":"); ok && strings.TrimSpace(key) == name {
				return true
			}
			i++
		default:
			i++
		}
	}
	return false
}

// skipQuoted is the index after the quoted run starting at s[i] (the end of s
// when it is unterminated). A quote is closed by its own character; backslash
// escapes and doubled quotes both work, as in the alert-rule guard.
func skipQuoted(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
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
