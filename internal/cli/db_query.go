package cli

// `forge db query <env> "<sql>"` runs one read-only statement against an
// environment's app database and prints the rows.
//
// The env is a required positional argument, dev included: a query names the
// database it reads, so there is no implicit default (owner decision D3).
//
// Execution is forge/pkg/dbquery's, the same core the control plane runs for a
// hosted env: a READ ONLY transaction with statement, lock and idle timeouts,
// one statement over the extended protocol, row/cell/response caps, and a
// lexical guard in front for readable refusals. This file only chooses the
// connection (the transport) and renders the result.
//
// Transports, by where the workload's database lives:
//
//	OnHost / OnCompose   direct: the workload's resolved DATABASE_URL, exactly
//	                     what `forge env exec` would hand the process
//	OnCluster            a port-forward to the CNPG Cluster, as the read-only
//	                     forge_ro role, on a scratch env (db_query_cluster.go)
//	OnHosted             the control plane's AppDatabaseService.QueryDatabase,
//	                     which runs the same core inside the platform as
//	                     forge_ro and audits the call (db_query_cloud.go)
//
// Policy is the env's forge.DataAccess db_query: open (a local or ephemeral
// env's default), audited (any other env's default: the query needs
// --confirm-env <env> and a --reason, so nobody reads a real database by
// mistyping an env name) or deny. For a hosted env the control plane enforces
// it from the env's RECORDED shape and forge only passes the confirmation on;
// for the other transports this checkout's declaration is the rule.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/dbquery"
	"github.com/reliant-labs/forge/pkg/release"
)

// dbQuerySchema names the JSON document's shape.
const dbQuerySchema = "forge.db.query/v1"

// maxDBQueryReason bounds --reason, as the control plane does.
const maxDBQueryReason = 500

type dbQueryOptions struct {
	env        string
	sql        string
	file       string
	workload   string
	database   string
	maxRows    int
	timeout    time.Duration
	format     string
	jsonOut    bool
	confirmEnv string
	reason     string
}

func newDBQueryCommand() *cobra.Command {
	var o dbQueryOptions
	cmd := &cobra.Command{
		Use:   `query <env> ["<sql>" | -f FILE]`,
		Short: "Run one read-only SQL statement against an environment's app database",
		Long: `Run one read-only statement against an environment's app database and print
the rows. The env is required, dev included.

Every query runs in a READ ONLY transaction with a statement timeout, as one
statement, under row and size caps. Writes, nextval() and a second statement
are refused: by a lexical guard first (so the message is readable), and by
Postgres itself regardless.

The connection follows the workload (` + "`--workload`" + `, default "api"):
  OnHost / OnCompose  its resolved DATABASE_URL, the same value
                      ` + "`" + Name() + ` env exec` + "`" + ` gives the process
  OnCluster           a port-forward with your kube context to the env's
                      ManagedDatabase (` + "`--database`" + ` when it has several), as
                      the read-only forge_ro role; scratch envs only
                      (lifecycle local or ephemeral)
  OnHosted            the control plane runs it inside the platform
                      (AppDatabaseService.QueryDatabase), as forge_ro, and
                      records it in the audit log and the activity feed
The databases of real (not scratch) cluster envs are not reachable from forge
yet (exit 2).

The env's data_access policy (forge.DataAccess) decides who may query:
open (the default of a local or ephemeral env), audited (the default of any
other env: pass --confirm-env <env> and a --reason) or deny. A hosted env is
judged by the control plane from the policy its deployed release recorded, and
an audited one is open to people only (and to a db:query grant).

Output: a table, or --format json|csv. --json is --format json; the document
carries ok, exit_code and error like every other forge --json verb, plus
schema "` + dbQuerySchema + `", scope, columns[{name,pg_type}], rows, row_count,
truncated, truncated_by, cells_truncated, duration_ms and bytes; a hosted
query adds audit{id, sql_sha256, replica}.

Exit codes: 0 rows returned; 1 the statement or the request is wrong (refused
by the guard or the read-only transaction, a SQL error, a permission error, a
policy refusal); 2 forge could not look (no database reachable, timeout,
connection lost).

  ` + Name() + ` db query dev "select count(*) from jobs where crew_id is null"
  ` + Name() + ` db query dev -f report.sql --format csv > report.csv
  ` + Name() + ` db query staging "select * from jobs limit 5" --confirm-env staging --reason "ISS-7f3a"`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 {
				return fmt.Errorf("use: %s db query <env> \"<sql>\" | -f FILE", Name())
			}
			return nil
		},
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.env = args[0]
			if len(args) == 2 {
				o.sql = args[1]
			}
			if o.jsonOut {
				o.format = "json"
			}
			return runDBQuery(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), o)
		},
	}
	cmd.Flags().StringVarP(&o.file, "file", "f", "", "Read the statement from FILE (- for stdin)")
	cmd.Flags().StringVar(&o.workload, "workload", "api", "Workload whose database to query")
	cmd.Flags().StringVar(&o.database, "database", "", "ManagedDatabase to query, when a cluster env declares several")
	cmd.Flags().IntVar(&o.maxRows, "max-rows", dbquery.DefaultMaxRows, fmt.Sprintf("Row cap (at most %d)", dbquery.MaxRowsLimit))
	cmd.Flags().DurationVar(&o.timeout, "timeout", dbquery.DefaultTimeout, fmt.Sprintf("Statement timeout (at most %s)", dbquery.MaxTimeout))
	cmd.Flags().StringVar(&o.format, "format", "table", "Output format: table, json or csv")
	cmd.Flags().BoolVar(&o.jsonOut, "json", false, "Same as --format json")
	cmd.Flags().StringVar(&o.confirmEnv, "confirm-env", "", "Required on a real env: the env name again")
	cmd.Flags().StringVar(&o.reason, "reason", "", "Required on a real env: why you are reading it (at most 500 characters)")
	return cmd
}

// dbQueryScope says what the query ran against.
type dbQueryScope struct {
	Env       string `json:"env"`
	Transport string `json:"transport"`
	Workload  string `json:"workload"`
	Database  string `json:"database,omitempty"`
	// Policy is the declared db_query policy forge applied. Absent on the
	// control-plane transport, which applies the env's recorded one.
	Policy string `json:"policy,omitempty"`
}

// dbQueryDocument is the --format json document.
type dbQueryDocument struct {
	jsonEnvelope
	Schema string        `json:"schema"`
	Scope  *dbQueryScope `json:"scope,omitempty"`
	// Reason is dbquery's machine-readable refusal subcode, when there is one.
	Reason string `json:"reason,omitempty"`
	// Release is the release live in the env when the query ran; absent when
	// nothing was promoted yet or the ledger could not be read.
	Release *liveRelease `json:"release,omitempty"`
	// Audit is what the control plane recorded about a hosted query.
	Audit *dbQueryAudit `json:"audit,omitempty"`
	*dbquery.Result
}

// dbQueryTarget is where a query goes: a DSN, or a connection config for a
// transport that dials itself (the port-forward), and the facts the output
// reports.
type dbQueryTarget struct {
	dsn        string
	connConfig *pgx.ConnConfig
	scope      dbQueryScope
	// close releases the transport, or is nil.
	close func()
	// remote runs the statement elsewhere (the control plane); set instead
	// of dsn.
	remote dbQueryRemote
}

func (t dbQueryTarget) connect(ctx context.Context) (*pgx.Conn, error) {
	if t.connConfig != nil {
		return pgx.ConnectConfig(ctx, t.connConfig)
	}
	return pgx.Connect(ctx, t.dsn)
}

func runDBQuery(ctx context.Context, stdin io.Reader, out, errOut io.Writer, o dbQueryOptions) error {
	var (
		scope   *dbQueryScope
		ranAt   time.Time
		release *liveRelease
		audit   *dbQueryAudit
	)
	res, err := func() (*dbquery.Result, error) {
		if err := validateDBQueryFlags(o); err != nil {
			return nil, err
		}
		sql, err := dbQueryStatement(stdin, o)
		if err != nil {
			return nil, err
		}
		target, err := resolveDBQueryTarget(ctx, o)
		if err != nil {
			return nil, err
		}
		if target.close != nil {
			defer target.close()
		}
		scope = &target.scope
		if target.remote != nil {
			// The control plane was reached at resolve; the remote is the
			// call itself.
			ranAt = time.Now().UTC()
			res, a, database, err := target.remote(ctx, sql)
			if database != "" {
				scope.Database = database
			}
			audit = a
			return res, err
		}
		if err := checkDBQueryPolicy(o, target.scope.Policy); err != nil {
			return nil, err
		}
		conn, err := target.connect(ctx)
		if err != nil {
			// The DSN can carry a password; never echo it.
			return nil, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("cannot connect to %s's database (workload %q): %s; is it running? (%s env up %s)", o.env, target.scope.Workload, redactDSNError(err, target.dsn), Name(), o.env)}
		}
		defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
		ranAt = time.Now().UTC()
		res, err := dbquery.Run(ctx, conn, sql, dbquery.Options{MaxRows: o.maxRows, Timeout: o.timeout})
		if err != nil {
			return nil, dbQueryError(err)
		}
		return res, nil
	}()

	if !ranAt.IsZero() {
		release = stampRelease(ctx, liveReleaseAt, errOut, o.env, ranAt)
	}
	if o.format == "json" {
		doc := dbQueryDocument{Schema: dbQuerySchema, Scope: scope, Release: release, Audit: audit, Result: res}
		doc.stamp(err)
		doc.Reason = dbQueryErrorReason(err)
		if werr := writeJSONDocument(out, doc); werr != nil {
			return werr
		}
		return err
	}
	if err != nil {
		return err
	}
	if o.format == "csv" {
		return writeDBQueryCSV(out, res)
	}
	return writeDBQueryTable(out, res, release, audit)
}

func validateDBQueryFlags(o dbQueryOptions) error {
	switch o.format {
	case "table", "json", "csv":
	default:
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("--format %q: use table, json or csv", o.format)}
	}
	if o.sql != "" && o.file != "" {
		return exitCodeError{code: exitWrong, msg: "give the statement as an argument or with -f, not both"}
	}
	if o.sql == "" && o.file == "" {
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("no statement: %s db query %s \"<sql>\" | -f FILE", Name(), o.env)}
	}
	if utf8.RuneCountInString(o.reason) > maxDBQueryReason {
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("--reason is longer than %d characters", maxDBQueryReason)}
	}
	return nil
}

// dbQueryStatement reads the statement from the argument, a file, or stdin.
func dbQueryStatement(stdin io.Reader, o dbQueryOptions) (string, error) {
	if o.file == "" {
		return o.sql, nil
	}
	var (
		b   []byte
		err error
	)
	if o.file == "-" {
		b, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
	} else {
		b, err = os.ReadFile(o.file)
	}
	if err != nil {
		return "", exitCodeError{code: exitWrong, msg: fmt.Sprintf("read statement: %v", err)}
	}
	return string(b), nil
}

// resolveDBQueryTarget picks the transport for the env's workload and resolves
// its DATABASE_URL the way `forge env exec` does.
func resolveDBQueryTarget(ctx context.Context, o dbQueryOptions) (dbQueryTarget, error) {
	projectDir := projectDirForKCL()
	e, err := readEnvConfigEntities(ctx, projectDir, o.env)
	if err != nil {
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: err.Error()}
	}
	w, err := selectExecWorkload(e, o.workload)
	if err != nil {
		return dbQueryTarget{}, exitCodeError{code: exitWrong, msg: err.Error()}
	}
	scope := dbQueryScope{Env: o.env, Workload: w.Name, Policy: dbQueryPolicy(e)}
	switch w.Runtime.Type {
	case RuntimeHost, RuntimeCompose:
		scope.Transport = "direct"
	case RuntimeCluster:
		return resolveDBQueryCluster(ctx, o, e, w)
	case RuntimeHosted:
		scope.Policy = ""
		return resolveDBQueryHosted(ctx, o, scope)
	default:
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("workload %q in %s runs %s: its database is not reachable from forge yet", w.Name, o.env, w.Runtime.Type)}
	}
	report, err := resolvedEnvConfigReport(ctx, projectDir, o.env, e, w.Name, secretsRequired)
	if err != nil {
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: err.Error()}
	}
	if len(report.Workloads) != 1 {
		return dbQueryTarget{}, exitCodeError{code: exitWrong, msg: fmt.Sprintf("workload %q is ambiguous", w.Name)}
	}
	dsn := report.Workloads[0].Env["DATABASE_URL"]
	if dsn == "" {
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("workload %q in %s declares no DATABASE_URL; pick the workload that owns the database with --workload", w.Name, o.env)}
	}
	return dbQueryTarget{dsn: dsn, scope: scope}, nil
}

// dbQueryPolicy is the env's declared db_query policy (forge.DataAccess,
// resolved by the render: what the env declared, else open for a local or
// ephemeral env and audited for any other). Absent means audited, never open.
func dbQueryPolicy(e *KCLEntities) string {
	if e == nil {
		return string(release.DBQueryAudited)
	}
	return string(e.DataAccess.Resolved().DBQuery)
}

func checkDBQueryPolicy(o dbQueryOptions, policy string) error {
	switch release.DBQueryPolicy(policy) {
	case release.DBQueryOpen:
		return nil
	case release.DBQueryAudited:
		if o.confirmEnv != o.env || strings.TrimSpace(o.reason) == "" {
			return exitCodeError{code: exitWrong, msg: fmt.Sprintf("%s is a real environment (data_access db_query = audited): pass --confirm-env %s and --reason \"<why>\" to read its database", o.env, o.env)}
		}
		return nil
	default:
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("%s refuses database queries (data_access db_query = %s)", o.env, policy)}
	}
}

// dbQueryError maps a dbquery failure onto the exit table: the request was
// wrong (1), or forge could not look (2).
func dbQueryError(err error) error {
	var de *dbquery.Error
	if !errors.As(err, &de) {
		return err
	}
	code := exitWrong
	switch de.Kind {
	case dbquery.KindTimeout, dbquery.KindCanceled, dbquery.KindUnavailable, dbquery.KindAudit:
		code = exitUndetermined
	}
	return dbQueryExitError{code: code, err: de}
}

// dbQueryExitError carries a dbquery failure with its exit code, keeping the
// *dbquery.Error reachable for the JSON reason field.
type dbQueryExitError struct {
	code int
	err  *dbquery.Error
}

func (e dbQueryExitError) Error() string { return e.err.Error() }
func (e dbQueryExitError) Unwrap() error { return e.err }
func (e dbQueryExitError) ExitCode() int { return e.code }

// redactDSNError removes the DSN, which may carry a password, from a
// connection error.
func redactDSNError(err error, dsn string) string {
	msg := err.Error()
	if dsn != "" {
		msg = strings.ReplaceAll(msg, dsn, "<DATABASE_URL>")
	}
	return msg
}

// cellText renders a cell for the table and csv formats: NULL as the empty
// string in csv and "NULL" in the table, json values as their text.
func cellText(v any, null string) string {
	switch c := v.(type) {
	case nil:
		return null
	case string:
		return c
	case json.Number:
		return c.String()
	case json.RawMessage:
		return string(c)
	case bool:
		if c {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(c)
	}
}

func writeDBQueryCSV(out io.Writer, res *dbquery.Result) error {
	w := csv.NewWriter(out)
	header := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		header[i] = c.Name
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, row := range res.Rows {
		rec := make([]string, len(row))
		for i, v := range row {
			rec[i] = cellText(v, "")
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// maxTableCell bounds one cell's width in the table format; --format json
// carries the whole value.
const maxTableCell = 80

func writeDBQueryTable(out io.Writer, res *dbquery.Result, rel *liveRelease, audit *dbQueryAudit) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	fmt.Fprintln(tw, strings.Join(names, "\t"))
	for _, row := range res.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			s := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(cellText(v, "NULL"))
			if utf8.RuneCountInString(s) > maxTableCell {
				s = string([]rune(s)[:maxTableCell-1]) + "…"
			}
			cells[i] = s
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	noun := "rows"
	if res.RowCount == 1 {
		noun = "row"
	}
	footer := fmt.Sprintf("(%d %s, %d ms", res.RowCount, noun, res.DurationMS)
	if rel != nil {
		footer += fmt.Sprintf("; release %s", rel.Version)
	}
	if audit != nil && audit.ID != "" {
		footer += fmt.Sprintf("; audit %s", audit.ID)
	}
	if res.Truncated {
		footer += fmt.Sprintf("; truncated by the %s cap, raise --max-rows or narrow the query", res.TruncatedBy)
	}
	if res.CellsTruncated > 0 {
		footer += fmt.Sprintf("; %d cells cut to %d bytes", res.CellsTruncated, dbquery.MaxCellBytes)
	}
	_, err := fmt.Fprintln(out, footer+")")
	return err
}
