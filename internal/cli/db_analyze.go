// File: internal/cli/db_analyze.go
//
// `forge db analyze` — what a migration measurably does on a real postgres:
// which locks it takes, whether it rewrites a table, whether a constraint
// validation scans one, how long it runs. The mechanism (the rolled-back
// session, the measurement, the rules, the document) is forge/pkg/dbanalyze;
// this file decides WHICH migrations, on WHICH database, and grades the
// findings like `forge lint`'s. Design: docs/proposals/db-analyze.md.
//
// It needs postgres, so it is never in a fast PR tier: it runs in a review
// lane, a full gate, or on demand.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/migrationgit"
	"github.com/reliant-labs/forge/pkg/dbanalyze"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/redact"
)

// defaultAnalyzeDSNEnv is the variable a dsn target's connection string is
// read from when neither --dsn-env nor database.analyze.target.dsn_env names
// another.
const defaultAnalyzeDSNEnv = "FORGE_ANALYZE_DATABASE_URL"

type dbAnalyzeOptions struct {
	target string
	dsnEnv string
	base   string
	migDir string
	json   bool
}

func newDBAnalyzeCommand() *cobra.Command {
	var opts dbAnalyzeOptions
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Measure what pending migrations do on a real postgres: locks, rewrites, scans, time",
		Long: `Apply migrations statement by statement, each file in its own transaction as
forge's migrator applies them, on a DISPOSABLE copy of the target, and measure
each statement: the locks it took (pg_locks), the tables and indexes it
rewrote (pg_class.relfilenode), the tables it scanned
(pg_stat_xact_user_tables), and how long it ran.

Which migrations:
  scratch (default)  the migrations added or changed since --base (default: the
                     merge-base with the remote default branch), on an embedded
                     postgres migrated with the unchanged ones
  dsn                every migration the target's schema_migrations lacks, on a
                     database you provide — typically an anonymized prod snapshot

A dsn target is only read, then copied (CREATE DATABASE … TEMPLATE) and the
copy dropped when the analysis ends: the target is never written. It must be
marked as one forge may copy, by whoever builds it:
  ALTER DATABASE <db> SET ` + dbanalyze.MarkerSetting + ` = 'on';
and the role needs CREATEDB and ownership of it, with nothing else connected to
it during the copy. A target that is a non-development environment's declared
database is refused too. The DSN is read from an environment variable
(database.analyze.target.dsn_env, default ` + defaultAnalyzeDSNEnv + `), never
from forge.yaml. A file whose one statement cannot run inside a transaction
(CREATE INDEX CONCURRENTLY) runs it on its own; its lock is reported
statically.

Rules (lint.rules sets their severity; each warns before it gates):
  ` + dbanalyze.RuleTableRewrite + `         a pre-existing table or its indexes rewritten
  ` + dbanalyze.RuleAccessExclusiveLock + ` ACCESS EXCLUSIVE held across a scan or rewrite
  ` + dbanalyze.RuleValidationScan + `        a full scan under a lock that blocks writes
  ` + dbanalyze.RuleBlockingIndexBuild + `   an index built without CONCURRENTLY
  ` + dbanalyze.RuleStatementTimeout + `     a statement exceeded statement_timeout

Exit codes: 0 nothing gating; 1 a gating finding; 2 could not analyze (no
postgres, target ahead of the tree, refused DSN, a statement that failed).
An empty analysis is reported as scanned 0, with the reason.

Load the db/analyze skill for configuration and how to read the document.

Examples:
  forge db analyze
  forge db analyze --json
  FORGE_ANALYZE_DATABASE_URL=postgres://... forge db analyze --target dsn`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectDir := dbProjectRoot()
			cfg, err := loadProjectConfig()
			if err != nil && !errors.Is(err, ErrProjectConfigNotFound) {
				return err
			}
			opts.migDir = resolveMigrationsDir(opts.migDir)
			// An interrupt cancels the running statement; the copy of a
			// dsn target is dropped on the way out regardless.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			doc := runDBAnalyze(ctx, projectDir, cfg, opts, defaultAnalyzeDeps())
			return reportDBAnalyze(cmd.OutOrStdout(), doc, opts.json)
		},
	}
	cmd.Flags().StringVar(&opts.target, "target", "", "Where to analyze: scratch (an embedded postgres) or dsn (default: database.analyze.target.kind, else scratch)")
	cmd.Flags().StringVar(&opts.dsnEnv, "dsn-env", "", "Environment variable holding a dsn target's connection string (default: database.analyze.target.dsn_env, else "+defaultAnalyzeDSNEnv+")")
	cmd.Flags().StringVar(&opts.base, "base", "", "Revision a scratch target's migrations are split at (default: the merge-base with the remote default branch)")
	cmd.Flags().StringVar(&opts.migDir, "dir", "", migrationsDirFlagUsage)
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print the forge.db-analyze/v1 document")
	return cmd
}

// analyzeDeps are the seams a test replaces: the environment, and what a
// deploy env declares as its database (which renders KCL).
type analyzeDeps struct {
	getenv      func(string) string
	declaredDSN func(ctx context.Context, projectDir, env string) string
}

func defaultAnalyzeDeps() analyzeDeps {
	return analyzeDeps{getenv: os.Getenv, declaredDSN: declaredEnvDSN}
}

// runDBAnalyze builds the document. It never fails on its own: a failure is
// the document's Error, exit 2.
func runDBAnalyze(ctx context.Context, projectDir string, cfg *config.ProjectConfig, opts dbAnalyzeOptions, deps analyzeDeps) *dbanalyze.Document {
	doc := dbanalyze.NewDocument()
	fail := func(format string, args ...any) *dbanalyze.Document {
		doc.Error = fmt.Sprintf(format, args...)
		doc.Finalize()
		return doc
	}
	acfg := analyzeConfigOf(cfg)
	kind, err := analyzeTargetKind(opts, acfg)
	if err != nil {
		return fail("%v", err)
	}
	doc.Target.Kind = dbanalyze.TargetKind(kind)
	stmtTimeout, lockTimeout, err := acfg.Target.Timeouts()
	if err != nil {
		return fail("%v", err)
	}

	ms, files, err := analyzeMigrationFiles(projectDir, opts.migDir)
	if err != nil {
		return fail("%v", err)
	}

	var (
		analyzed []dbanalyze.MigrationFile
		dsn      string
	)
	switch kind {
	case config.AnalyzeTargetScratch:
		setup, changed, base, err := splitAtBase(projectDir, opts.migDir, opts.base, files)
		if err != nil {
			return fail("%v", err)
		}
		doc.Base, analyzed = base, changed
		if len(analyzed) == 0 {
			doc.Summary.Reason = fmt.Sprintf("no migration in %s was added or changed since %s", analyzeRelPath(projectDir, opts.migDir), base)
			doc.Finalize()
			return doc
		}
		pinScratchPostgresVersion(cfg, deps)
		var cleanup func()
		dsn, cleanup, err = dbanalyze.PrepareScratch(ctx, setup)
		if err != nil {
			return fail("could not start a scratch postgres migrated to the base: %v", err)
		}
		defer cleanup()
	case config.AnalyzeTargetDSN:
		if dsn, err = analyzeTargetDSN(ctx, projectDir, opts, acfg, deps); err != nil {
			return fail("%v", err)
		}
	}
	doc.Target.Identity = databaseIdentity(dsn)

	var info dbanalyze.TargetInfo
	work := dsn
	if kind == config.AnalyzeTargetDSN {
		var st dbanalyze.MigrationState
		info, st, err = inspectAnalyzeTarget(ctx, dsn, doc.Target.Identity)
		if err != nil {
			return fail("%v", err)
		}
		doc.Target = info
		analyzed, err = pendingOnTarget(ms, files, st)
		if err != nil {
			return fail("%v", err)
		}
		if len(analyzed) == 0 {
			doc.Summary.Reason = fmt.Sprintf("the target has applied every migration in %s (version %d)", analyzeRelPath(projectDir, opts.migDir), st.Version)
			doc.Finalize()
			return doc
		}
		// The migrations run on a copy, never on the target: they commit.
		clone, err := dbanalyze.CloneDatabase(ctx, dsn)
		if err != nil {
			return fail("%v", err)
		}
		defer func() {
			if err := clone.Close(); err != nil && doc.Error == "" {
				doc.Error = fmt.Sprintf("%v: drop it by hand (DROP DATABASE %s)", err, clone.Info().Database)
				doc.Finalize()
			}
		}()
		ci := clone.Info()
		info.Clone = &ci
		doc.Target = info
		work = clone.DSN()
	}

	s, err := dbanalyze.Open(ctx, work, dbanalyze.SessionOptions{StatementTimeout: stmtTimeout, LockTimeout: lockTimeout})
	if err != nil {
		return fail("connect to the %s target %s: %v", kind, doc.Target.Identity, err)
	}
	defer func() { _ = s.Close() }()
	if kind == config.AnalyzeTargetScratch {
		if info, err = s.Info(ctx, doc.Target.Kind, doc.Target.Identity); err != nil {
			return fail("%v", err)
		}
		doc.Target = info
	}

	migs, findings, err := dbanalyze.AnalyzeMigrations(ctx, s, info, analyzed, dbanalyze.Config{
		TableRows: acfg.TableRows, SmallTableRows: acfg.SmallTableRows, LargeTableRows: acfg.LargeTableRows,
	})
	doc.Migrations = append(doc.Migrations, migs...)
	doc.Findings = gradeAnalyzeFindings(cfg, findings)
	if err != nil {
		return fail("could not analyze past %v", err)
	}
	doc.Finalize()
	return doc
}

// analyzeConfigOf is forge.yaml's database.analyze, zero without a forge.yaml.
func analyzeConfigOf(cfg *config.ProjectConfig) config.AnalyzeConfig {
	if cfg == nil {
		return config.AnalyzeConfig{}
	}
	return cfg.Database.Analyze
}

// pinScratchPostgresVersion points pgtest at the project's
// database.postgres_version: pgtest reads it from the forge.yaml above the CWD,
// and -C may name another project. An explicit $FORGE_TEST_POSTGRES_VERSION
// (pgtest.EnvVersion) wins.
func pinScratchPostgresVersion(cfg *config.ProjectConfig, deps analyzeDeps) {
	if v := cfgPostgresVersion(cfg); v != "" && deps.getenv(pgtest.EnvVersion) == "" {
		_ = os.Setenv(pgtest.EnvVersion, v)
	}
}

// analyzeTargetKind is --target, else database.analyze.target's kind: scratch
// or dsn.
func analyzeTargetKind(opts dbAnalyzeOptions, acfg config.AnalyzeConfig) (string, error) {
	kind := strings.TrimSpace(opts.target)
	if kind == "" {
		kind = acfg.Target.EffectiveKind()
	}
	switch kind {
	case config.AnalyzeTargetScratch, config.AnalyzeTargetDSN:
		return kind, nil
	}
	return "", fmt.Errorf("--target %q is not scratch or dsn", kind)
}

// analyzeTargetDSN is a dsn target's connection string, read from the env var
// --dsn-env (else database.analyze.target.dsn_env) names, refusing one a
// deploy env declares as its database.
func analyzeTargetDSN(ctx context.Context, projectDir string, opts dbAnalyzeOptions, acfg config.AnalyzeConfig, deps analyzeDeps) (string, error) {
	env := firstNonEmptyStr(opts.dsnEnv, acfg.Target.DSNEnv, defaultAnalyzeDSNEnv)
	dsn := strings.TrimSpace(deps.getenv(env))
	if dsn == "" {
		return "", fmt.Errorf("a dsn target reads its connection string from $%s, which is unset: export it (a snapshot of production, never production itself)", env)
	}
	if err := refuseDeployedAnalyzeDSN(ctx, projectDir, dsn, deps.declaredDSN); err != nil {
		return "", err
	}
	return dsn, nil
}

// analyzeMigrationFiles reads the forge-migrator migrations under migDir, and
// their contents for analysis, refusing a project whose migrations another
// tool applies.
func analyzeMigrationFiles(projectDir, migDir string) ([]compat.Migration, []dbanalyze.MigrationFile, error) {
	h, ok, herr := projectMigrationHook(migDir)
	if herr != nil {
		return nil, nil, herr
	}
	if ok {
		return nil, nil, fmt.Errorf("this project's migrations are applied by %s (database.migrations.tool): forge db analyze reads forge-migrator migrations (<version>_<name>.up.sql) only", h.Tool)
	}
	if info, err := os.Stat(migDir); err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("migrations directory %s does not exist", migDir)
	}
	ms, err := projectMigrations(migDir)
	if err != nil {
		return nil, nil, fmt.Errorf("read migrations: %v", err)
	}
	files := make([]dbanalyze.MigrationFile, 0, len(ms))
	for _, m := range ms {
		path := filepath.Join(migDir, m.Name)
		data, err := os.ReadFile(path) // #nosec G304 -- the project's own migration
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %v", path, err)
		}
		files = append(files, dbanalyze.MigrationFile{File: analyzeRelPath(projectDir, path), Version: uint64(m.Version), SQL: string(data)})
	}
	return ms, files, nil
}

// splitAtBase splits a scratch target's migrations at the base revision:
// the files unchanged since it are setup, the ones added or changed are
// analyzed. A changed file is analyzed after every unchanged one, in version
// order (an edited merged migration is itself a defect; see db/versioning).
func splitAtBase(projectDir, migDir, base string, files []dbanalyze.MigrationFile) (setup, changed []dbanalyze.MigrationFile, label string, err error) {
	root := migrationgit.RepoRoot(projectDir)
	var sha string
	if base != "" {
		s, ok := migrationgit.ResolveCommit(root, base)
		if !ok {
			return nil, nil, "", fmt.Errorf("--base %q is not a commit in this repository", base)
		}
		sha, label = s, base
	} else {
		s, ref, ok := migrationgit.MergeBase(root)
		if !ok {
			return nil, nil, "", fmt.Errorf("cannot find the merge-base with the remote default branch (no git repository, no default branch, or a shallow clone): pass --base <rev>")
		}
		sha, label = s, strings.TrimPrefix(ref, "refs/remotes/")
	}
	label += "@" + shortSHA(sha)
	atBase, ok := migrationgit.FilesAt(root, migDir, sha)
	if !ok {
		return nil, nil, "", fmt.Errorf("cannot read %s at %s", migDir, label)
	}
	diff, ok := migrationgit.ChangedSince(root, migDir, sha)
	if !ok {
		return nil, nil, "", fmt.Errorf("cannot diff %s against %s", migDir, label)
	}
	present := map[string]bool{}
	for _, n := range atBase {
		present[n] = true
	}
	for _, f := range files {
		name := filepath.Base(f.File)
		if present[name] && !diff[name] {
			setup = append(setup, f)
		} else {
			changed = append(changed, f)
		}
	}
	return setup, changed, label, nil
}

// pendingOnTarget is every migration a dsn target has not applied, by the
// version its schema_migrations records. A target ahead of the tree, dirty,
// or at a version no migration carries cannot be analyzed.
func pendingOnTarget(ms []compat.Migration, files []dbanalyze.MigrationFile, st dbanalyze.MigrationState) ([]dbanalyze.MigrationFile, error) {
	switch {
	case st.Dirty:
		return nil, fmt.Errorf("the target's schema is dirty at version %d (a migration failed part-way there): repair it before analyzing", st.Version)
	case !st.Applied:
		return files, nil
	case len(ms) == 0:
		return nil, fmt.Errorf("the target is at version %d and the tree holds no migrations: the target is ahead of the tree", st.Version)
	}
	latest := uint64(ms[len(ms)-1].Version)
	if st.Version > latest {
		return nil, fmt.Errorf("the target is at version %d, ahead of the tree (latest %d): analyze it from a checkout that has its migrations", st.Version, latest)
	}
	carried := false
	for _, m := range ms {
		carried = carried || uint64(m.Version) == st.Version
	}
	if !carried {
		if squashedBy(ms[0], uint(st.Version)) {
			return files[1:], nil
		}
		return nil, fmt.Errorf("the target is at version %d, which no migration in the tree carries", st.Version)
	}
	var out []dbanalyze.MigrationFile
	for _, f := range files {
		if f.Version > st.Version {
			out = append(out, f)
		}
	}
	return out, nil
}

// inspectAnalyzeTarget reads what is read from a dsn target itself — its
// marker, server, statistics and schema_migrations — over a read-only
// connection it closes before returning, since the target is then copied and
// postgres copies a database only while nobody is connected to it.
//
// The marker is the guard: a database is analyzed only when whoever made it
// said it may be, with ALTER DATABASE … SET forge.analyze_target = 'on'.
// Production carries no such setting, so a DSN that reaches production — by
// a pasted runbook line, a stale variable, an env forge could not resolve —
// is refused here whatever its spelling.
func inspectAnalyzeTarget(ctx context.Context, dsn, identity string) (dbanalyze.TargetInfo, dbanalyze.MigrationState, error) {
	var st dbanalyze.MigrationState
	src, err := dbanalyze.OpenSource(ctx, dsn)
	if err != nil {
		return dbanalyze.TargetInfo{}, st, fmt.Errorf("the dsn target %s: %w", identity, err)
	}
	defer func() { _ = src.Close() }()
	marked, err := src.Marked(ctx)
	if err != nil {
		return dbanalyze.TargetInfo{}, st, fmt.Errorf("the dsn target %s: %w", identity, err)
	}
	if !marked {
		name := identity[strings.LastIndex(identity, "/")+1:]
		//nolint:revive,staticcheck // terminal operator-facing prose; see reconcileClaimedDSN
		return dbanalyze.TargetInfo{}, st, fmt.Errorf(`refusing the dsn target %s: it is not marked as a database forge db analyze may copy and run migrations on.

Whoever builds the snapshot marks it, once, as its owner:

  ALTER DATABASE %s SET %s = 'on';

Never mark production. The analysis copies the target (CREATE DATABASE … TEMPLATE), so the role also needs CREATEDB and ownership of it, and nothing else may be connected to it while the copy is made.`,
			identity, pgx.Identifier{name}.Sanitize(), dbanalyze.MarkerSetting)
	}
	info, err := src.Info(ctx, dbanalyze.TargetDSN, identity)
	if err != nil {
		return info, st, err
	}
	st, err = src.MigrationState(ctx)
	return info, st, err
}

// refuseDeployedAnalyzeDSN refuses a dsn target that is the database a
// non-development deploy env declares. The analysis runs on a copy, but
// making the copy reads every block of the target, and a target that is
// production should not be a snapshot's stand-in at all.
//
// It is the SECOND check: the first is the marker (inspectAnalyzeTarget),
// which a production database does not carry. An env whose DSN forge cannot
// resolve (a secret it cannot read, KCL that does not render) is therefore
// not compared, rather than refusing every analysis in the project: the
// comparison was never what kept production out on its own — it failed open
// exactly there — and the marker does not depend on resolving anything.
//
// Both sides are compared as pgx resolves them (databaseIdentity): a DSN
// whose host, port or dbname query parameters route it somewhere other than
// its URL is judged by where it connects. A DSN that does not resolve to
// exactly one server and database — none named, several hosts, or not
// parseable — is refused: forge cannot tell which database it is. A
// declared DSN with several hosts matches when any of them is the target.
func refuseDeployedAnalyzeDSN(ctx context.Context, projectDir, dsn string, declared func(context.Context, string, string) string) error {
	id := databaseIdentity(dsn)
	if id == "" {
		return fmt.Errorf("refusing the dsn target %s: it does not resolve to exactly one server and database (it names no database, lists several hosts, or does not parse), so forge cannot check that it is not a deployed environment's database", redact.DSN(dsn))
	}
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return err
	}
	for _, env := range envs {
		if seedEnvIsDevIn(projectDir, env) {
			continue
		}
		if d := declared(ctx, projectDir, env); d != "" && slices.Contains(databaseEndpoints(d), id) {
			return fmt.Errorf("refusing the dsn target %s: it is environment %q's database. forge db analyze copies its target and runs every pending migration on the copy, which reads every block of it: point it at a snapshot, not at %s", id, env, env)
		}
	}
	return nil
}

// gradeAnalyzeFindings grades the findings as forge lint grades its own: the
// warn-first rollout for the project's pin, then lint.rules.
func gradeAnalyzeFindings(cfg *config.ProjectConfig, fs []dbanalyze.Finding) []dbanalyze.Finding {
	out := make([]dbanalyze.Finding, 0, len(fs))
	for _, f := range fs {
		sev, keep, note := lint.GradeFinding(cfg, f.Rule, f.Severity)
		if !keep {
			continue
		}
		f.Severity, f.Note = sev, note
		out = append(out, f)
	}
	return out
}

// reportDBAnalyze prints the document and returns the error carrying its
// exit code.
func reportDBAnalyze(w io.Writer, doc *dbanalyze.Document, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return err
		}
	} else {
		writeDBAnalyzeText(w, doc)
	}
	switch doc.ExitCode {
	case dbanalyze.ExitOK:
		return nil
	case dbanalyze.ExitFindings:
		n := 0
		for _, f := range doc.Findings {
			if f.Severity == dbanalyze.SeverityError {
				n++
			}
		}
		return exitCodeError{code: dbanalyze.ExitFindings, msg: fmt.Sprintf("db analyze: %d finding(s) gate", n)}
	default:
		return exitCodeError{code: doc.ExitCode, msg: "db analyze could not analyze: " + doc.Error}
	}
}

func writeDBAnalyzeText(w io.Writer, doc *dbanalyze.Document) {
	t := doc.Target
	fmt.Fprintf(w, "forge db analyze — %s target", t.Kind)
	if t.Identity != "" {
		fmt.Fprintf(w, " %s", t.Identity)
	}
	if t.ServerVersion != "" {
		fmt.Fprintf(w, " (postgres %s)", t.ServerVersion)
	}
	if doc.Base != "" {
		fmt.Fprintf(w, ", base %s", doc.Base)
	}
	fmt.Fprintln(w)
	if c := t.Clone; c != nil {
		fmt.Fprintf(w, "analyzed on a copy, %s (%.1f MB, copied in %.0fms), dropped afterwards\n", c.Database, float64(c.SizeBytes)/1e6, c.DurationMS)
	}
	for _, m := range doc.Migrations {
		fmt.Fprintf(w, "\n%s\n", m.File)
		if !m.Analyzed {
			fmt.Fprintf(w, "  not analyzed: %s\n", m.Reason)
			continue
		}
		for _, st := range m.Statements {
			fmt.Fprintf(w, "  line %d: %s\n", st.Line, oneLine(st.SQL, 72))
			switch {
			case st.Error != "":
				fmt.Fprintf(w, "    failed: %s\n", st.Error)
			case !st.Executed:
				fmt.Fprintf(w, "    not executed: %s\n", st.Reason)
			default:
				fmt.Fprintf(w, "    %.1fms\n", st.DurationMS)
			}
			var locks, held []string
			for _, l := range st.Locks {
				switch {
				case l.New:
				case l.HeldBefore:
					held = append(held, l.Relation+" "+l.Mode)
				case l.Static:
					locks = append(locks, l.Relation+" "+l.Mode+" (known from the statement's kind)")
				default:
					locks = append(locks, l.Relation+" "+l.Mode)
				}
			}
			if len(locks) > 0 {
				fmt.Fprintf(w, "    locks:    %s\n", strings.Join(locks, ", "))
			}
			if len(held) > 0 {
				fmt.Fprintf(w, "    held:     %s (taken by an earlier statement of the file)\n", strings.Join(held, ", "))
			}
			for _, rw := range st.Rewrites {
				if !rw.New {
					fmt.Fprintf(w, "    rewrote:  %s (%s)\n", rw.Relation, rw.Kind)
				}
			}
			for _, sc := range st.Scans {
				if !sc.New {
					fmt.Fprintf(w, "    scanned:  %s (%d seq scan(s), %d rows)\n", sc.Relation, sc.SeqScan, sc.SeqTupRead)
				}
			}
		}
	}
	if len(doc.Findings) > 0 {
		fmt.Fprintln(w, "\nfindings:")
		for _, f := range doc.Findings {
			fmt.Fprintf(w, "  %s %s:%d [%s] %s\n", f.Severity, f.File, f.Line, f.Rule, f.Message)
			if f.Fix != "" {
				fmt.Fprintf(w, "      fix: %s\n", f.Fix)
			}
			if f.Note != "" {
				fmt.Fprintf(w, "      %s\n", f.Note)
			}
		}
	}
	s := doc.Summary
	fmt.Fprintf(w, "\n%d migration(s) analyzed, %d statement(s) executed", s.MigrationsScanned, s.StatementsExecuted)
	if s.Reason != "" {
		fmt.Fprintf(w, " — %s", s.Reason)
	}
	fmt.Fprintln(w)
}

// analyzeRelPath names path relative to the project, slash-separated.
func analyzeRelPath(projectDir, path string) string {
	if rel, err := filepath.Rel(projectDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func cfgPostgresVersion(cfg *config.ProjectConfig) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Database.PostgresVersion)
}
