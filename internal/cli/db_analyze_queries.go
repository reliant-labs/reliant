// File: internal/cli/db_analyze_queries.go
//
// `forge db analyze queries` — the query pass of `forge db analyze`: the
// query shapes the tests recorded under FORGE_QUERY_CAPTURE_DIR
// (forge/pkg/querycapture), planned on the head schema, and whether an index
// serves each filter, join and ORDER BY … LIMIT (forge/pkg/dbanalyze). This
// file chooses WHICH shapes; runDBAnalyze (db_analyze.go) runs the pass.
// Design: docs/proposals/db-analyze.md, Phase 3.

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/migrationgit"
	"github.com/reliant-labs/forge/pkg/dbanalyze"
	"github.com/reliant-labs/forge/pkg/querycapture"
)

func newDBAnalyzeQueriesCommand() *cobra.Command {
	opts := dbAnalyzeOptions{queriesOnly: true}
	cmd := &cobra.Command{
		Use:   "queries",
		Short: "Plan the query shapes the tests captured and report filters, joins and sorts no index serves",
		Long: `Plan every query shape a test run recorded (FORGE_QUERY_CAPTURE_DIR) on the
head schema with EXPLAIN (GENERIC_PLAN) — as postgres would run it, and with
enable_seqscan off — and report, per table, whether an index serves the
statement's filter, join or ORDER BY … LIMIT. Needs postgres 16 or newer.

First capture, then analyze:
  FORGE_QUERY_CAPTURE_DIR=/tmp/qc task test:short
  forge db analyze queries --capture /tmp/qc

Which shapes: those with a caller (or the generated store method it went
through) in a file changed since --base (default: the merge-base with the
remote default branch); --all plans every one. Queries no test runs are not
captured, so they are not analyzed.

Where (--target, as forge db analyze):
  scratch (default)  an embedded postgres migrated to the head migrations. Its
                     tables are empty: whether an index CAN serve a statement is
                     exact; sizes come only from database.analyze.table_rows
  dsn                a disposable copy of a database you provide (an anonymized
                     snapshot of prod, marked ` + dbanalyze.MarkerSetting + ` = 'on'),
                     with its pending migrations applied as forge's migrator
                     applies them; the copy is dropped afterwards and the target
                     is never written

Rules (lint.rules sets their severity; each warns before it gates):
  ` + dbanalyze.RuleQueryNoUsableIndex + `  a filtered, joined or ORDER BY … LIMIT table no
      index serves. Error when the table's size is known (statistics on a dsn
      target, else table_rows) and at least small_table_rows; dropped when it is
      known and smaller; a warning when it is unknown
  ` + dbanalyze.RuleQueryLargeSeqScan + `    (dsn target with statistics) the plan
      sequentially scans a table of at least large_table_rows

A shape that cannot be planned (a table only its test created) is listed under
summary.skipped with its error. An empty selection is reported as scanned 0,
with the reason. Exit codes: 0 nothing gating; 1 a gating finding; 2 could
not analyze.

` + "`forge db analyze --capture DIR`" + ` runs the migration pass and then this one, on
the same copy.

Examples:
  forge db analyze queries --capture /tmp/qc
  forge db analyze queries --capture /tmp/qc --all --json
  FORGE_ANALYZE_DATABASE_URL=postgres://... forge db analyze queries --capture /tmp/qc --target dsn`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectDir := dbProjectRoot()
			cfg, err := loadProjectConfig()
			if err != nil && !errors.Is(err, ErrProjectConfigNotFound) {
				return err
			}
			opts.migDir = resolveMigrationsDir(opts.migDir)
			if opts.capture == "" {
				opts.capture = os.Getenv(querycapture.EnvDir)
			}
			// An interrupt cancels the running statement; the copy of a
			// dsn target is dropped on the way out regardless.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			doc := runDBAnalyze(ctx, projectDir, cfg, opts, defaultAnalyzeDeps())
			return reportDBAnalyze(cmd.OutOrStdout(), doc, opts.json)
		},
	}
	addDBAnalyzeFlags(cmd, &opts)
	return cmd
}

// addDBAnalyzeFlags registers the flags `forge db analyze` and its queries
// subcommand share.
func addDBAnalyzeFlags(cmd *cobra.Command, opts *dbAnalyzeOptions) {
	cmd.Flags().StringVar(&opts.target, "target", "", "Where to analyze: scratch (an embedded postgres) or dsn (default: database.analyze.target.kind, else scratch)")
	cmd.Flags().StringVar(&opts.dsnEnv, "dsn-env", "", "Environment variable holding a dsn target's connection string (default: database.analyze.target.dsn_env, else "+defaultAnalyzeDSNEnv+")")
	cmd.Flags().StringVar(&opts.base, "base", "", "Revision changes are measured from: a scratch target's migrations and the query shapes are selected by what changed since it (default: the merge-base with the remote default branch)")
	cmd.Flags().StringVar(&opts.migDir, "dir", "", migrationsDirFlagUsage)
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print the forge.db-analyze/v1 document")
	captureUsage := "Directory a test run recorded query shapes into (" + querycapture.EnvDir + "); runs the query pass"
	if opts.queriesOnly {
		captureUsage = "Directory a test run recorded query shapes into (default: $" + querycapture.EnvDir + ")"
	}
	cmd.Flags().StringVar(&opts.capture, "capture", "", captureUsage)
	cmd.Flags().BoolVar(&opts.all, "all", false, "Plan every captured query shape, not only those with a caller in a file changed since --base")
}

// queryPass is the query pass's input: the shapes chosen to plan.
type queryPass struct {
	shapes []querycapture.Shape
	// captured is how many shapes the capture held.
	captured int
	// base is the revision the shapes were selected at ("" under --all).
	base string
	// reason says why no shape was selected, when none was.
	reason  string
	skipped []dbanalyze.Skipped
}

// selectQueryShapes reads the capture directory and selects the shapes to
// plan: under --all every one, else those with a caller — or the generated
// frame it went through — in a file changed since the base. A selected
// shape's callers are reordered so the changed ones come first: a finding
// names the first.
func selectQueryShapes(projectDir string, cfg *config.ProjectConfig, opts dbAnalyzeOptions) (*queryPass, error) {
	c, err := querycapture.Read(opts.capture)
	if err != nil {
		return nil, fmt.Errorf("read the query capture: %w (run the tests with %s=%s first)", err, querycapture.EnvDir, opts.capture)
	}
	qp := &queryPass{captured: len(c.Shapes)}
	if c.Skipped > 0 {
		qp.skipped = append(qp.skipped, dbanalyze.Skipped{What: opts.capture,
			Reason: fmt.Sprintf("%d capture line(s) are not a record this forge reads (cut short by a killed process, or a newer format)", c.Skipped)})
	}
	if len(c.Shapes) == 0 {
		qp.reason = fmt.Sprintf("%s holds no query shapes (%d capture file(s)): run the tests with %s=%s first; a query no test runs is never captured",
			opts.capture, c.Files, querycapture.EnvDir, opts.capture)
		return qp, nil
	}
	if opts.all {
		qp.shapes = c.Shapes
		return qp, nil
	}
	root, sha, label, err := resolveBase(projectDir, opts.base)
	if err != nil {
		return nil, fmt.Errorf("%v, or --all to plan every captured shape", err)
	}
	qp.base = label
	changed, ok := migrationgit.ChangedPathsSince(root, sha)
	if !ok {
		return nil, fmt.Errorf("cannot diff the repository against %s", label)
	}
	module := goModulePath(projectDir)
	if module == "" && cfg != nil {
		module = cfg.ModulePath
	}
	frames := frameResolver{repoRoot: realPath(root), projectDir: realPath(projectDir), module: module}
	isChanged := func(cs querycapture.CallSite) bool {
		if p, ok := frames.repoPath(cs.Caller); ok && changed[p] {
			return true
		}
		if cs.Generated != nil {
			if p, ok := frames.repoPath(*cs.Generated); ok && changed[p] {
				return true
			}
		}
		return false
	}
	for _, sh := range c.Shapes {
		var first, rest []querycapture.CallSite
		for _, cs := range sh.Callers {
			if isChanged(cs) {
				first = append(first, cs)
			} else {
				rest = append(rest, cs)
			}
		}
		if len(first) == 0 {
			continue
		}
		first = append(first, rest...)
		sh.Callers = first
		qp.shapes = append(qp.shapes, sh)
	}
	if len(qp.shapes) == 0 {
		qp.reason = fmt.Sprintf("none of the %d captured query shapes has a caller in a file changed since %s (--all plans every one)", len(c.Shapes), label)
	}
	return qp, nil
}

// frameResolver maps a captured frame to its repository-relative path.
type frameResolver struct {
	repoRoot, projectDir, module string
}

// repoPath is f's file relative to the repository, slash-separated. A frame
// carries its absolute path when the test binary recorded one; otherwise its
// path is relative to its module's root, which is known for the project's
// own module only — a frame of another module (a dependency) is not this
// change's.
func (r frameResolver) repoPath(f querycapture.Frame) (string, bool) {
	var abs string
	switch {
	case f.AbsFile != "":
		if !filepath.IsAbs(f.AbsFile) {
			return "", false // not an absolute path on this host
		}
		abs = f.AbsFile
	case f.File == "":
		return "", false
	case filepath.IsAbs(f.File):
		abs = f.File
	case os.IsPathSeparator(f.File[0]) || filepath.VolumeName(f.File) != "":
		return "", false // never reinterpret a rooted or drive-relative path under the module
	case f.Module == "" || f.Module == r.module:
		abs = filepath.Join(r.projectDir, filepath.FromSlash(f.File))
	default:
		return "", false
	}
	rel, err := filepath.Rel(r.repoRoot, realPath(abs))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// realPath resolves the longest existing prefix, including symlinks and
// Windows short names. A captured file and several of its parent directories
// may have been deleted after capture.
func realPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	var suffix []string
	for prefix := abs; ; prefix = filepath.Dir(prefix) {
		resolved, err := filepath.EvalSymlinks(prefix)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved
		}
		// Do not walk above an inaccessible or otherwise invalid path.
		if !os.IsNotExist(err) || filepath.Dir(prefix) == prefix {
			return abs
		}
		suffix = append(suffix, filepath.Base(prefix))
	}
}

// queryPassRequested reports whether the document carries a query pass's
// verdict, empty or not.
func queryPassRequested(doc *dbanalyze.Document) bool {
	s := doc.Summary
	return len(doc.Queries) > 0 || s.QueriesCaptured > 0 || s.QueriesReason != ""
}
