// File: internal/cli/lint/lint_fixture_drift.go
//
// fixture-drift — `forge lint --fixture-drift`.
//
// Scaffold-once CRUD lifecycle tests used to carry their fixtures INLINE:
// internal/handlers/<svc>/handlers_crud_test.go embedded a literal INSERT
// block for each entity's foreign-key parents, derived from the schema as it
// stood at birth. Forge never rewrites that file — it is the user's from line
// one — while the schema keeps moving, and the db skill itself tells authors
// to harden the birth migration right afterwards. Each such edit could turn a
// literal into a rejected statement:
//
//	seed parent rows: pq: cannot insert a non-DEFAULT value into column "total_cents" (428C9)
//	seed parent rows: pq: duplicate key value violates unique constraint "jobs_estimate_id_key"
//	seed parent rows: pq: insert or update on table "crews" violates foreign key constraint "crews_foreman_id_fkey"
//	seed parent rows: pq: new row for relation "estimates" violates check constraint "estimates_sent_has_stamp"
//
// Lifecycle tests forge scaffolds NOW carry no fixtures: they call the
// regenerated New<CreateRequest> factories in factories_gen_test.go, which
// track the schema on every generate. This lane exists for the files
// scaffolded before that — they are the user's, so forge reports rather than
// rewrites.
//
// ── Execution, not pattern-matching ──────────────────────────────────────
//
// This lane used to read migration text and pattern-match two failure
// classes (a GENERATED column named in a column list, a value repeated in a
// UNIQUE column), with a sibling lane (crud-fixtures) for a third (a foreign
// key value naming no seeded row). Each new constraint shape needed a new
// matcher, and a CHECK added by a later migration — the commonest hardening
// of all — matched none of them, so the lanes reported clean over a fixture
// postgres rejects.
//
// So the authority is asked instead. The project's migrations are applied to
// a shadow postgres (the same one `forge generate` introspects), and every
// literal fixture statement is EXECUTED against it, inside a transaction that
// is always rolled back. Whatever postgres says is the finding, verbatim, per
// statement: GENERATED columns, UNIQUE collisions, dangling foreign keys,
// CHECK violations, NOT NULL columns a later migration added — and the shapes
// no matcher anticipated.
//
// The statements run grouped the way the test runs them: every fixture
// literal in one test function shares one transaction, in source order (each
// lifecycle test starts from a freshly migrated database, so a parent seeded
// by one function is NOT visible to another). Each statement runs under a
// SAVEPOINT, so a rejected statement is reported and the rest of the block
// still runs — fixing one finding never just reveals the next.
//
// ── What it reads ────────────────────────────────────────────────────────
//
// Raw-string literals in handlers_crud_test.go that contain an INSERT. A
// statement carrying a bind placeholder ($1) is skipped: its values come from
// Go at runtime, and executing it without them would report the missing
// parameter rather than anything about the fixture. A file that does not
// parse is skipped too — the compiler is already reporting it.
//
// No literal fixtures, no database: a project whose lifecycle tests use the
// factories pays nothing for this lane. When the shadow cannot be reached the
// lane says it did not run, rather than reporting clean.
//
// ── Severity: warning, never gating ──────────────────────────────────────
//
// The fixture is genuinely broken and its test genuinely fails, but the
// remedy is an edit to a file forge does not own and may legitimately be
// mid-edit. Failing the whole lint run over it would be a generator holding a
// user's file hostage — and a noisy gating rule is how a check earns a blanket
// disable, which costs every future finding it would have reported.

package lint

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// fixtureDriftRule is the stable rule id for a fixture statement postgres
// rejects, reported in text mode and in `forge lint --json`.
const fixtureDriftRule = "forge-fixture-rejected"

// fixtureDriftUnverifiedRule reports that the lane found literal fixtures but
// could not execute them — so a consumer of the JSON report can tell "checked,
// clean" apart from "not checked".
const fixtureDriftUnverifiedRule = "forge-fixture-unverified"

// fixtureDriftFinding is one fixture statement the current schema rejects.
// File is root-relative and Line is 1-indexed, pointing at the statement.
type fixtureDriftFinding struct {
	File string
	Line int
	// Table is the INSERT's target, "" for a statement that is not an INSERT.
	Table string
	// Code, Message and Constraint are postgres's own: the SQLSTATE, the
	// error text, and the constraint it names (when it names one).
	Code       string
	Message    string
	Constraint string
	// DeclaredIn is the root-relative migration that last mentions what
	// postgres complained about, "" when it could not be attributed. It turns
	// "this fixture is wrong" into "this fixture predates this migration".
	DeclaredIn string
}

// message is the one-line summary, shared by text mode and JSON.
func (f fixtureDriftFinding) message() string {
	target := "this statement"
	if f.Table != "" {
		target = "this INSERT INTO " + f.Table
	}
	code := ""
	if f.Code != "" {
		code = fmt.Sprintf(" (SQLSTATE %s)", f.Code)
	}
	return fmt.Sprintf("postgres rejects %s against the current schema: %s%s", target, f.Message, code)
}

// fixtureDriftFixHint renders the remediation.
//
// It states the ownership fact explicitly. The author was told forge would
// never touch this file again, so a finding about it has to say plainly that
// editing it is theirs to do and will not be reverted — otherwise the obvious
// next move is to re-run `forge generate` and conclude the lint is wrong when
// nothing changes.
func fixtureDriftFixHint(f fixtureDriftFinding) string {
	origin := ""
	switch {
	case f.Constraint != "" && f.DeclaredIn != "":
		origin = fmt.Sprintf(" Constraint %s is declared in %s.", f.Constraint, f.DeclaredIn)
	case f.Constraint != "":
		origin = fmt.Sprintf(" Constraint: %s.", f.Constraint)
	case f.DeclaredIn != "":
		origin = fmt.Sprintf(" The column changed in %s.", f.DeclaredIn)
	}
	return "This fixture was scaffolded from the schema as it stood then, and handlers_crud_test.go is yours — " +
		"written once, never regenerated — so `forge generate` cannot repair it." + origin + " " +
		"Lifecycle tests forge scaffolds now carry no literal fixtures: they call New<CreateRequest>(t, db, variant) " +
		"from the regenerated factories_gen_test.go beside this file, which seeds the parents and fills the request " +
		"from the current schema on every generate. Replace this seed block and the literal create requests with " +
		"those calls (see `forge skill load testing`), or edit the statement until postgres accepts it."
}

// fixtureDriftReport is everything one run found. Unverified is non-empty
// when literal fixtures exist but could not be executed.
type fixtureDriftReport struct {
	Findings   []fixtureDriftFinding
	Statements int // fixture statements executed
	Unverified string
	// UnverifiedFile is a root-relative test file the unverified statements
	// live in, so the JSON finding has somewhere to point.
	UnverifiedFile string
}

// collectFixtureDriftFindings is the shared engine behind text mode and
// `forge lint --json`.
//
// A project with no migrations, or no literal fixtures, yields an empty
// report rather than an error: both are ordinary states for a project this
// lane does not apply to.
func collectFixtureDriftFindings(root, migrationsDir string) (fixtureDriftReport, error) {
	var rep fixtureDriftReport
	if !filepath.IsAbs(migrationsDir) {
		migrationsDir = filepath.Join(root, migrationsDir)
	}
	testFiles, err := crudTestFiles(root)
	if err != nil {
		return rep, err
	}
	var blocks []fixtureBlock
	for _, path := range testFiles {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rep, fmt.Errorf("read %s: %w", path, rerr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		blocks = append(blocks, fixtureBlocks(filepath.ToSlash(rel), string(data))...)
	}
	if len(blocks) == 0 {
		return rep, nil
	}
	if _, statErr := os.Stat(migrationsDir); statErr != nil {
		return rep, nil // no schema to execute against: nothing this lane can say
	}

	_, shadow, err := schemadef.ApplyAndIntrospectShadowAt(migrationsDir, shadowdb.Resolve(root))
	if err != nil {
		shadow.Close()
		rep.Unverified = fmt.Sprintf("could not apply %s to a shadow postgres to execute the fixtures against: %v",
			relOrSelf(root, migrationsDir), err)
		rep.UnverifiedFile = blocks[0].file
		return rep, nil
	}
	defer shadow.Close()

	ctx := context.Background()
	for _, b := range blocks {
		found, ran, xerr := executeFixtureBlock(ctx, shadow.DB(), b)
		if xerr != nil {
			rep.Unverified = fmt.Sprintf("could not execute the fixtures in %s against the shadow postgres: %v", b.file, xerr)
			rep.UnverifiedFile = b.file
			rep.Findings = nil
			return rep, nil
		}
		rep.Statements += ran
		rep.Findings = append(rep.Findings, found...)
	}
	attributeFixtureFindings(root, migrationsDir, rep.Findings)
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		if rep.Findings[i].File != rep.Findings[j].File {
			return rep.Findings[i].File < rep.Findings[j].File
		}
		return rep.Findings[i].Line < rep.Findings[j].Line
	})
	return rep, nil
}

// relOrSelf renders path relative to root when it can.
func relOrSelf(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

// crudTestFiles returns every scaffolded lifecycle test under
// internal/handlers, sorted.
func crudTestFiles(root string) ([]string, error) {
	handlersDir := filepath.Join(root, "internal", "handlers")
	if _, err := os.Stat(handlersDir); os.IsNotExist(err) {
		return nil, nil
	}
	var files []string
	if err := filepath.WalkDir(handlersDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "handlers_crud_test.go" {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk %s: %w", handlersDir, err)
	}
	sort.Strings(files)
	return files, nil
}

// fixtureStatement is one SQL statement out of a fixture literal.
type fixtureStatement struct {
	sql   string
	line  int
	table string // the INSERT target, "" for any other statement
}

// fixtureBlock is the fixture SQL one test function executes, in order —
// the unit that shares a database in the test, and so a transaction here.
type fixtureBlock struct {
	file  string
	stmts []fixtureStatement
}

var (
	// fixtureInsertRe recognizes a literal that carries fixture SQL, and
	// captures an INSERT statement's target table.
	fixtureInsertRe = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+("?[\w.]+"?)`)
	// bindParamRe matches a positional bind placeholder.
	bindParamRe = regexp.MustCompile(`\$\d+`)
)

// fixtureBlocks reads the fixture blocks out of one lifecycle test: every
// raw-string literal carrying an INSERT, grouped by the function declaring
// it, in source order. A literal outside any function is its own block.
func fixtureBlocks(relPath, content string) []fixtureBlock {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, content, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var blocks []fixtureBlock
	collect := func(n ast.Node) []fixtureStatement {
		var stmts []fixtureStatement
		ast.Inspect(n, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
				return true
			}
			stmts = append(stmts, literalStatements(fset, lit)...)
			return true
		})
		return stmts
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if stmts := collect(fn); len(stmts) > 0 {
				blocks = append(blocks, fixtureBlock{file: relPath, stmts: stmts})
			}
			continue
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
				return true
			}
			if stmts := literalStatements(fset, lit); len(stmts) > 0 {
				blocks = append(blocks, fixtureBlock{file: relPath, stmts: stmts})
			}
			return true
		})
	}
	return blocks
}

// literalStatements splits one raw-string literal into its executable
// statements, or returns nil when the literal carries no INSERT (it is then
// not fixture SQL — a SELECT under assertion, a message).
func literalStatements(fset *token.FileSet, lit *ast.BasicLit) []fixtureStatement {
	body := lit.Value[1 : len(lit.Value)-1]
	parts := splitSQLStatements(body)
	hasInsert := false
	for _, p := range parts {
		if fixtureInsertRe.MatchString(p.text) {
			hasInsert = true
			break
		}
	}
	if !hasInsert {
		return nil
	}
	startLine := fset.Position(lit.Pos()).Line
	var out []fixtureStatement
	for _, p := range parts {
		text := strings.TrimSpace(p.text)
		if text == "" || bindParamRe.MatchString(text) {
			continue
		}
		lead := len(p.text) - len(strings.TrimLeft(p.text, " \t\r\n"))
		st := fixtureStatement{
			sql:  text,
			line: startLine + strings.Count(body[:p.offset+lead], "\n"),
		}
		if m := fixtureInsertRe.FindStringSubmatch(text); m != nil {
			st.table = m[1]
		}
		out = append(out, st)
	}
	return out
}

// sqlPart is one statement's text and its offset in the literal.
type sqlPart struct {
	text   string
	offset int
}

// splitSQLStatements splits SQL on the semicolons that end statements —
// never one inside a string literal, a quoted identifier, or a comment. A
// comment-only part is kept as text and dropped by the caller as empty.
func splitSQLStatements(body string) []sqlPart {
	var (
		out   []sqlPart
		start int
	)
	blanked := blankSQLComments(body)
	inSingle, inDouble := false, false
	for i := 0; i < len(blanked); i++ {
		c := blanked[i]
		switch {
		case inSingle:
			if c == '\'' {
				if i+1 < len(blanked) && blanked[i+1] == '\'' {
					i++ // '' escape
					continue
				}
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == ';':
			out = append(out, sqlPart{text: blanked[start:i], offset: start})
			start = i + 1
		}
	}
	if strings.TrimSpace(blanked[start:]) != "" {
		out = append(out, sqlPart{text: blanked[start:], offset: start})
	}
	return out
}

// executeFixtureBlock runs one block's statements in order inside a
// transaction that is always rolled back, each under its own SAVEPOINT so a
// rejected statement is recorded and the rest still run. It returns the
// rejections and the number of statements executed; err is reserved for the
// harness itself failing (the shadow could not open a transaction), which is
// not a verdict about any fixture.
func executeFixtureBlock(ctx context.Context, db *sql.DB, b fixtureBlock) (findings []fixtureDriftFinding, ran int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for i, st := range b.stmts {
		sp := fmt.Sprintf("forge_fixture_%d", i)
		if _, err := tx.ExecContext(ctx, "SAVEPOINT "+sp); err != nil {
			return nil, ran, err
		}
		ran++
		if _, xerr := tx.ExecContext(ctx, st.sql); xerr != nil {
			findings = append(findings, rejectedFixture(b.file, st, xerr))
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+sp); err != nil {
				return nil, ran, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+sp); err != nil {
			return nil, ran, err
		}
	}
	return findings, ran, nil
}

// rejectedFixture turns postgres's error into a finding, keeping its SQLSTATE,
// message and constraint name verbatim.
func rejectedFixture(file string, st fixtureStatement, err error) fixtureDriftFinding {
	f := fixtureDriftFinding{File: file, Line: st.line, Table: st.table, Message: err.Error()}
	// pgtest opens pgx (*pgconn.PgError); a lib/pq handle gives *pq.Error.
	var pgErr *pgconn.PgError
	var pqErr *pq.Error
	switch {
	case errors.As(err, &pgErr):
		f.Code = pgErr.Code
		f.Message = pgErr.Message
		f.Constraint = pgErr.ConstraintName
	case errors.As(err, &pqErr):
		f.Code = string(pqErr.Code)
		f.Message = pqErr.Message
		f.Constraint = pqErr.Constraint
	}
	return f
}

// generatedColumnMsgRe pulls the column out of postgres's 428C9 message.
var generatedColumnMsgRe = regexp.MustCompile(`column "([^"]+)"`)

// attributeFixtureFindings fills DeclaredIn: the LAST migration (in apply
// order) that mentions what postgres complained about — the named constraint,
// or for a write into a generated column, that column's GENERATED clause.
// Best-effort; an unattributed finding is still a correct one.
func attributeFixtureFindings(root, migrationsDir string, findings []fixtureDriftFinding) {
	if len(findings) == 0 {
		return
	}
	type migration struct{ rel, content string }
	var migrations []migration
	_ = eachMigration(root, migrationsDir, func(relPath, content string) {
		migrations = append(migrations, migration{relPath, content})
	})
	lastMatching := func(re *regexp.Regexp) string {
		for i := len(migrations) - 1; i >= 0; i-- {
			if re.MatchString(migrations[i].content) {
				return migrations[i].rel
			}
		}
		return ""
	}
	for i := range findings {
		f := &findings[i]
		switch {
		case f.Constraint != "":
			f.DeclaredIn = lastMatching(regexp.MustCompile(`\b` + regexp.QuoteMeta(f.Constraint) + `\b`))
		case f.Code == "428C9":
			if m := generatedColumnMsgRe.FindStringSubmatch(f.Message); m != nil {
				f.DeclaredIn = lastMatching(regexp.MustCompile(
					`(?is)\b` + regexp.QuoteMeta(m[1]) + `\b[^,;]*\bGENERATED\s+ALWAYS\b`))
			}
		}
	}
}
