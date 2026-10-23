package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/migrationver"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func newMigrateImportCmd() *cobra.Command {
	var (
		from    string
		srcDir  string
		destDir string
		dryRun  bool
		force   bool
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import migrations from another format (e.g. goose) into golang-migrate shape",
		Long: `Import SQL migrations from another tool's format into forge's
forward-only golang-migrate shape (one .up.sql per migration).

Currently supports:
  --from goose    One-file goose migrations with -- +goose Up / -- +goose Down

For each *.sql file in --src-dir, the importer:
  1. Keeps the -- +goose Up section and DROPS the -- +goose Down section.
     Forge rolls forward only and never runs down SQL, so the importer
     writes no .down.sql; each dropped Down section is listed so you can
     see what was discarded.
  2. Drops -- +goose StatementBegin / -- +goose StatementEnd markers.
  3. Splits a -- +goose NO TRANSACTION file into one migration per goose
     statement. goose ran each of those statements on its own, outside any
     transaction. golang-migrate runs a file as ONE multi-statement query,
     which postgres runs as one implicit transaction, so two
     CREATE INDEX CONCURRENTLY statements in one file fail with SQLSTATE
     25001 and leave the schema dirty. One statement per file runs exactly
     as goose ran it.
  4. Renumbers starting from the next-available index in --dest-dir, so
     pack-installed migrations (00001-0000N) keep their slots.

Files with no goose markers are skipped.

Importing renumbers the history, so it suits a project whose databases can be
rebuilt. A database goose ALREADY migrated would run every imported migration
again. For those, squash goose's history into one baseline whose header
declares it — "-- forge:adopts goose <every goose version>" — and the migrator
adopts each goose database on its next run without re-running anything. See
the forge skill db/deploy-migrations, "Adopting a database another tool
migrated".

Examples:
  forge project migrate import --from goose --src-dir ../old-project/migrations
  forge project migrate import --from goose --src-dir ./legacy --dry-run
  forge project migrate import --from goose --src-dir ./legacy --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateImport(migrateImportOptions{
				From:    from,
				SrcDir:  srcDir,
				DestDir: destDir,
				DryRun:  dryRun,
				Force:   force,
				Stdout:  cmd.OutOrStdout(),
			})
		},
	}

	cmd.Flags().StringVar(&from, "from", "", "Source format (currently only 'goose' supported)")
	cmd.Flags().StringVar(&srcDir, "src-dir", "", "Directory containing source migration files")
	cmd.Flags().StringVar(&destDir, "dest-dir", "", "Destination migrations directory (default: project's migrations dir)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print planned writes without touching disk")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing target files (default refuses)")

	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("src-dir")

	return cmd
}

type migrateImportOptions struct {
	From    string
	SrcDir  string
	DestDir string
	DryRun  bool
	Force   bool
	Stdout  interface{ Write(p []byte) (int, error) }
}

func runMigrateImport(opts migrateImportOptions) error {
	if opts.From != "goose" {
		return fmt.Errorf("--from %q is not supported (only 'goose' is supported today)", opts.From)
	}

	srcAbs, err := filepath.Abs(opts.SrcDir)
	if err != nil {
		return fmt.Errorf("resolve --src-dir: %w", err)
	}
	if info, err := os.Stat(srcAbs); err != nil {
		return fmt.Errorf("--src-dir %q: %w", opts.SrcDir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("--src-dir %q is not a directory", opts.SrcDir)
	}

	destDir := opts.DestDir
	if destDir == "" {
		destDir = resolveMigrationsDir("")
	}

	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("resolve --dest-dir: %w", err)
	}

	srcFiles, err := listGooseFiles(srcAbs)
	if err != nil {
		return err
	}
	if len(srcFiles) == 0 {
		_, _ = fmt.Fprintf(opts.Stdout, "No .sql files found in %s\n", srcAbs)
		return nil
	}

	plans, skipped, err := planGooseImport(srcFiles)
	if err != nil {
		return err
	}
	// One version per migration the import writes, allocated only once the
	// plan knows how many that is: a NO TRANSACTION file can become several.
	versions, err := migrationver.NextN(destAbs, len(plans))
	if err != nil {
		return err
	}
	for i := range plans {
		plans[i].Version = versions[i]
	}

	if !opts.Force && !opts.DryRun {
		existingStems, err := existingMigrationStems(destAbs)
		if err != nil {
			return err
		}
		for _, p := range plans {
			if _, err := os.Stat(p.UpPath(destAbs)); err == nil {
				return fmt.Errorf("target file already exists: %s (pass --force to overwrite)", p.UpPath(destAbs))
			}
			for _, stem := range p.stems() {
				if _, ok := existingStems[stem]; ok {
					return fmt.Errorf("a migration with stem %q already exists in %s (pass --force to overwrite, or rename the source file)", stem, destAbs)
				}
			}
		}
	}

	if opts.Force && !opts.DryRun {
		stemToPaths, err := existingMigrationFilesByStem(destAbs)
		if err != nil {
			return err
		}
		removed := map[string]bool{}
		for _, p := range plans {
			for _, stem := range p.stems() {
				for _, oldPath := range stemToPaths[stem] {
					if oldPath == p.UpPath(destAbs) || removed[oldPath] {
						continue
					}
					if err := os.Remove(oldPath); err != nil {
						return fmt.Errorf("--force: remove old %s: %w", oldPath, err)
					}
					removed[oldPath] = true
				}
			}
		}
	}

	if opts.DryRun {
		_, _ = fmt.Fprintf(opts.Stdout, "Dry run: would write %d migration(s) to %s\n\n", len(plans), destAbs)
		for _, p := range plans {
			_, _ = fmt.Fprintf(opts.Stdout, "  %s\n", p.UpPath(destAbs))
		}
		printNoTxSplits(opts.Stdout, plans)
		printDroppedDowns(opts.Stdout, plans)
		printImportSkips(opts.Stdout, skipped)
		printFKWarnings(opts.Stdout, plans)
		return nil
	}

	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return fmt.Errorf("create --dest-dir %q: %w", destDir, err)
	}

	for _, p := range plans {
		if err := atomicfile.WriteFile(p.UpPath(destAbs), []byte(p.UpBody), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p.UpPath(destAbs), err)
		}
		_, _ = fmt.Fprintf(opts.Stdout, "  Wrote %s\n", p.UpPath(destAbs))
	}

	printNoTxSplits(opts.Stdout, plans)
	printDroppedDowns(opts.Stdout, plans)
	printImportSkips(opts.Stdout, skipped)
	printFKWarnings(opts.Stdout, plans)

	_, _ = fmt.Fprintf(opts.Stdout, "\nImported %d migration(s).\n", len(plans))
	return nil
}

type importPlan struct {
	// Version is the allocated timestamp version prefix. The importer
	// allocates the whole batch at once (migrationver.NextN), one distinct
	// second each, so an import of forty goose files cannot collide with
	// itself.
	Version string
	Stem    string
	// SrcStem is the source file's stem. It differs from Stem only for one
	// statement of a split NO TRANSACTION file (Stem is then
	// `<SrcStem>_part<N>`), and is checked and replaced like Stem, so a
	// re-import meets a previous import of the same file under either name.
	SrcStem string
	UpBody  string
	// Part and Parts place one statement of a split NO TRANSACTION file:
	// statement Part of Parts. Both are zero for a file imported whole.
	Part, Parts int
	// DroppedDown reports that the source carried a goose Down section
	// with SQL in it, which the import discarded (forge rolls forward
	// only). Surfaced so the drop is visible rather than silent.
	DroppedDown bool
	SrcPath     string
}

func (p importPlan) UpPath(destDir string) string {
	return filepath.Join(destDir, fmt.Sprintf("%s_%s.up.sql", p.Version, p.Stem))
}

// stems is every migration stem this plan's file may already exist under.
func (p importPlan) stems() []string {
	if p.SrcStem == "" || p.SrcStem == p.Stem {
		return []string{p.Stem}
	}
	return []string{p.Stem, p.SrcStem}
}

type importSkip struct {
	Path   string
	Reason string
}

func listGooseFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read --src-dir %q: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".sql") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

var migrationFilePattern = regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)
var gooseFilePattern = regexp.MustCompile(`^(\d+)_(.+)\.sql$`)

func existingMigrationStems(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("read --dest-dir %q: %w", dir, err)
	}
	out := map[string]struct{}{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		out[m[2]] = struct{}{}
	}
	return out, nil
}

func existingMigrationFilesByStem(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("read --dest-dir %q: %w", dir, err)
	}
	out := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		stem := m[2]
		out[stem] = append(out[stem], filepath.Join(dir, e.Name()))
	}
	return out, nil
}

// planGooseImport converts every source file into the migrations it becomes,
// in order. Versions are left for the caller to allocate, once it knows how
// many migrations the plan holds.
func planGooseImport(srcFiles []string) ([]importPlan, []importSkip, error) {
	var plans []importPlan
	var skips []importSkip
	for _, src := range srcFiles {
		raw, err := os.ReadFile(src)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", src, err)
		}
		converted, ok, reason := convertGooseFile(string(raw))
		if !ok {
			skips = append(skips, importSkip{Path: src, Reason: reason})
			continue
		}
		stem := gooseSlug(src)
		if len(converted.Ups) == 1 {
			plans = append(plans, importPlan{
				Stem:        stem,
				SrcStem:     stem,
				UpBody:      converted.Ups[0],
				DroppedDown: converted.HadDown,
				SrcPath:     src,
			})
			continue
		}
		for i, body := range converted.Ups {
			plans = append(plans, importPlan{
				Stem:    fmt.Sprintf("%s_part%d", stem, i+1),
				SrcStem: stem,
				UpBody:  noTxPartHeader(filepath.Base(src), i+1, len(converted.Ups)) + body,
				Part:    i + 1,
				Parts:   len(converted.Ups),
				// The drop is a fact about the source file: report it once.
				DroppedDown: converted.HadDown && i == 0,
				SrcPath:     src,
			})
		}
	}
	return plans, skips, nil
}

// noTxPartHeader opens one statement of a split NO TRANSACTION file. It is a
// comment for the reader, not a directive: the split is what makes the
// statement run outside a transaction, and nothing needs to read it.
func noTxPartHeader(srcName string, part, parts int) string {
	return fmt.Sprintf("-- Statement %d of %d of goose migration %s, which declared NO TRANSACTION.\n"+
		"-- goose ran each of its statements on its own; golang-migrate runs a file as one\n"+
		"-- implicit transaction, so each statement is its own migration.\n\n", part, parts, srcName)
}

func gooseSlug(srcPath string) string {
	base := filepath.Base(srcPath)
	if m := gooseFilePattern.FindStringSubmatch(base); m != nil {
		return m[2]
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

type convertedGoose struct {
	// Ups is the body of every migration the file becomes, in order: one,
	// unless the file declared NO TRANSACTION and holds several statements
	// (see gooseStatements).
	Ups []string
	// HadDown reports a Down section carrying SQL — discarded, not written.
	HadDown bool
}

var (
	gooseUpMarker       = regexp.MustCompile(`^\s*--\s*\+goose\s+Up\b`)
	gooseDownMarker     = regexp.MustCompile(`^\s*--\s*\+goose\s+Down\b`)
	gooseStatementBegin = regexp.MustCompile(`^\s*--\s*\+goose\s+StatementBegin\b`)
	gooseStatementEnd   = regexp.MustCompile(`^\s*--\s*\+goose\s+StatementEnd\b`)
	gooseNoTransaction  = regexp.MustCompile(`^\s*--\s*\+goose\s+NO\s+TRANSACTION\b`)
)

// convertGooseFile turns one goose file into the forge migrations it becomes.
//
// A NO TRANSACTION file holding several statements becomes one migration per
// statement. goose runs such a file statement by statement, each on its own,
// outside any transaction — the reason it is declared at all is usually a
// statement that cannot run inside one (CREATE INDEX CONCURRENTLY) or one
// whose effect the next must see committed (ALTER TYPE … ADD VALUE, then a row
// using the value). golang-migrate has no such mode: it sends a file to
// postgres as ONE multi-statement query, which postgres runs as one implicit
// transaction. Kept in one file, those statements fail with SQLSTATE 25001
// (or 55P04) and leave the schema dirty. A file per statement runs each
// exactly as goose ran it, and records each as it lands, so a failure part-way
// leaves only the failed statement to repair.
//
// A tx-mode file needs no split: goose ran it in one transaction, and so does
// golang-migrate.
func convertGooseFile(content string) (convertedGoose, bool, string) {
	if !containsGooseMarker(content) {
		return convertedGoose{}, false, "no goose markers found, skipping"
	}

	upLines, downLines := splitAtGooseDown(content)
	hadDown := strings.TrimSpace(stripSQLLineComments(stripGooseMarkers(downLines))) != ""

	if containsGooseNoTransaction(content) {
		if stmts := gooseStatements(upLines); len(stmts) > 1 {
			return convertedGoose{Ups: stmts, HadDown: hadDown}, true, ""
		}
	}

	upBody := strings.TrimSpace(stripGooseMarkers(upLines)) + "\n"
	return convertedGoose{Ups: []string{upBody}, HadDown: hadDown}, true, ""
}

// gooseStatements splits an Up section into the statements goose executes,
// by goose's own rule (pressly/goose v3, internal/sqlparser): a statement ends
// at a line whose last word before any `--` comment ends in `;`, except
// between -- +goose StatementBegin and -- +goose StatementEnd, where it ends
// at StatementEnd. Comment and blank lines before a statement travel with it:
// goose skips them, but here they document the statement they precede.
//
// goose's rule and not a SQL lexer, because the point is to reproduce what
// goose RAN. Under NO TRANSACTION goose sent each of these to postgres as one
// query; golang-migrate sends a migration file as one query. So two
// statements goose sent together — on one line, or in one StatementBegin
// block — stay in one migration, exactly as atomic as they were, and a file
// goose split is split at the same places.
func gooseStatements(upLines []string) []string {
	var (
		stmts   []string
		pending strings.Builder // comment and blank lines before the next statement
		stmt    strings.Builder
		inBlock bool
	)
	emit := func() {
		if strings.TrimSpace(stmt.String()) == "" {
			return
		}
		stmts = append(stmts, strings.TrimSpace(pending.String()+stmt.String())+"\n")
		pending.Reset()
		stmt.Reset()
	}
	for _, line := range upLines {
		switch {
		case gooseStatementBegin.MatchString(line):
			inBlock = true
			continue
		case gooseStatementEnd.MatchString(line):
			inBlock = false
			emit()
			continue
		}
		trimmed := strings.TrimSpace(line)
		if stmt.Len() == 0 && (trimmed == "" || strings.HasPrefix(trimmed, "--")) {
			pending.WriteString(line + "\n")
			continue
		}
		stmt.WriteString(line + "\n")
		if !inBlock && gooseEndsStatement(line) {
			emit()
		}
	}
	// An unterminated tail (goose refuses one) is kept as a statement of its
	// own rather than dropped. Comments after the last statement stay with it.
	emit()
	if trailing := strings.TrimSpace(pending.String()); trailing != "" && len(stmts) > 0 {
		stmts[len(stmts)-1] += "\n" + trailing + "\n"
	}
	return stmts
}

// gooseEndsStatement is goose's endsWithSemicolon: the line's last
// whitespace-separated word before the first one opening a `--` comment ends
// in `;`.
func gooseEndsStatement(line string) bool {
	prev := ""
	for _, word := range strings.Fields(line) {
		if strings.HasPrefix(word, "--") {
			break
		}
		prev = word
	}
	return strings.HasSuffix(prev, ";")
}

func containsGooseMarker(content string) bool {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if gooseUpMarker.MatchString(line) || gooseDownMarker.MatchString(line) {
			return true
		}
	}
	return false
}

func containsGooseNoTransaction(content string) bool {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if gooseNoTransaction.MatchString(scanner.Text()) {
			return true
		}
	}
	return false
}

func splitAtGooseDown(content string) (upLines, downLines []string) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	inDown := false
	for scanner.Scan() {
		line := scanner.Text()
		if gooseDownMarker.MatchString(line) {
			inDown = true
			continue
		}
		// Drop +goose Up / +goose NO TRANSACTION markers — they're
		// goose-specific directives, not migration content. Lines before
		// any marker are treated as up-section by default.
		if gooseUpMarker.MatchString(line) || gooseNoTransaction.MatchString(line) {
			continue
		}
		if inDown {
			downLines = append(downLines, line)
		} else {
			upLines = append(upLines, line)
		}
	}
	return upLines, downLines
}

func stripGooseMarkers(lines []string) string {
	var out strings.Builder
	for _, line := range lines {
		if gooseStatementBegin.MatchString(line) || gooseStatementEnd.MatchString(line) {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

var fkReferencePattern = regexp.MustCompile(`(?i)\breferences\b`)

func printFKWarnings(w interface{ Write(p []byte) (int, error) }, plans []importPlan) {
	var fkFiles []string
	for _, p := range plans {
		if fkReferencePattern.MatchString(p.UpBody) {
			fkFiles = append(fkFiles, filepath.Base(p.UpPath("")))
		}
	}
	if len(fkFiles) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\nForeign-key check: the following imported files reference other tables.")
	_, _ = fmt.Fprintln(w, "Verify the referenced tables exist in earlier (lower-numbered) migrations:")
	for _, f := range fkFiles {
		_, _ = fmt.Fprintf(w, "  - %s\n", f)
	}
}

// stripSQLLineComments drops `--` comment lines, so a Down section holding
// only commentary does not count as down SQL.
func stripSQLLineComments(body string) string {
	var out strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// printNoTxSplits lists the NO TRANSACTION files the import split into one
// migration per statement, so the extra files are explained rather than
// surprising.
func printNoTxSplits(w interface{ Write(p []byte) (int, error) }, plans []importPlan) {
	var lines []string
	for _, p := range plans {
		if p.Part == 1 {
			lines = append(lines, fmt.Sprintf("%s → %d migrations, %s_part1 … %s_part%d", filepath.Base(p.SrcPath), p.Parts, p.SrcStem, p.SrcStem, p.Parts))
		}
	}
	if len(lines) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\nSplit -- +goose NO TRANSACTION files into one migration per statement (goose ran each")
	_, _ = fmt.Fprintln(w, "statement on its own; golang-migrate runs a file as one implicit transaction):")
	for _, l := range lines {
		_, _ = fmt.Fprintf(w, "  - %s\n", l)
	}
}

// printDroppedDowns lists the source files whose goose Down section was
// discarded. Dropping it is the policy — forge never runs down SQL — but a
// silent drop would read as data loss, so it is named.
func printDroppedDowns(w interface{ Write(p []byte) (int, error) }, plans []importPlan) {
	var dropped []string
	for _, p := range plans {
		if p.DroppedDown {
			dropped = append(dropped, filepath.Base(p.SrcPath))
		}
	}
	if len(dropped) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\nDropped -- +goose Down sections (forge rolls forward only and never runs down SQL;")
	_, _ = fmt.Fprintln(w, "recover from a bad migration with a new forward migration):")
	for _, f := range dropped {
		_, _ = fmt.Fprintf(w, "  - %s\n", f)
	}
}

func printImportSkips(w interface{ Write(p []byte) (int, error) }, skips []importSkip) {
	if len(skips) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\nSkipped:")
	for _, s := range skips {
		_, _ = fmt.Fprintf(w, "  - %s: %s\n", filepath.Base(s.Path), s.Reason)
	}
}
