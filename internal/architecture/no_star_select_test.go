package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ─── no star selects over a relation ───────────────────────────────────────
//
// A star (`SELECT *`, `t.*`, `RETURNING *`, ColumnExpr("t.*"),
// Returning("*")) is expanded when a statement is PREPARED, and prepared
// statements outlive migrations: pgx caches them per connection, and
// pgbouncer keeps them prepared on its server connections across clients.
// Once a migration adds a column to the relation, every statement prepared
// before it fails with "cached plan must not change result type" until its
// connection recycles. That broke ListArchivedChats in prod on 2026-10-22,
// when chats_with_activity gained working_threads. Name the columns from the
// scan model instead (internal/store/columns.go): ColumnExpr("?TableColumns")
// or Returning("?TableColumns") on a builder query, columnsOf[T](alias) in
// raw SQL.
//
// A bare star over a derived table or a function — `SELECT * FROM (…)`,
// `SELECT * FROM unnest(…)` — is allowed: the statement itself fixes that
// shape.

var (
	// sqlLiteral marks a string literal as SQL worth checking.
	sqlLiteral = regexp.MustCompile(`(?i)\b(select|returning)\b`)
	// starColumn is a star in a select or returning list: after SELECT
	// [DISTINCT], RETURNING or a comma; group 1 is its qualifier, if any.
	starColumn = regexp.MustCompile(`(?is)(?:\bselect\s+(?:distinct\s+)?|\breturning\s+|,\s*)((?:"?[a-z_][a-z0-9_]*"?\.)?)\*`)
	// fixedShapeSource is what may follow a bare star: a derived table or a
	// set-returning function, whose columns the statement itself names.
	fixedShapeSource = regexp.MustCompile(`(?is)^\s*from\s+(?:\(|[a-z_][a-z0-9_]*\s*\()`)
	// starArg is a builder argument that is only a star: "*", "t.*".
	starArg = regexp.MustCompile(`^\s*(?:"?[A-Za-z_][A-Za-z0-9_]*"?\.)?\*\s*$`)
)

// builderStarMethods take a column list as their first argument.
var builderStarMethods = map[string]bool{"Column": true, "ColumnExpr": true, "Returning": true}

// starInSQL reports the first star column in sql, or "".
func starInSQL(sql string) string {
	if !sqlLiteral.MatchString(sql) {
		return ""
	}
	for _, m := range starColumn.FindAllStringSubmatchIndex(sql, -1) {
		qualified := m[3] > m[2]
		if !qualified && strings.HasPrefix(strings.ToLower(strings.TrimSpace(sql[m[0]:m[1]])), "select") &&
			fixedShapeSource.MatchString(sql[m[1]:]) {
			continue
		}
		return strings.TrimSpace(sql[m[0]:m[1]])
	}
	return ""
}

func TestStarInSQL(t *testing.T) {
	t.Parallel()
	flagged := []string{
		"SELECT * FROM chats",
		"select * from chats_with_activity c",
		"SELECT\n    c.*,\n    w.name FROM chats_with_activity c",
		"SELECT DISTINCT t.* FROM threads t",
		"SELECT id, * FROM chats",
		"SELECT e.id, e.* FROM trigger_events e",
		`SELECT "c".* FROM chats c`,
		"UPDATE oauth_flows SET consumed_at = now() RETURNING *",
		"DELETE FROM x WHERE id = 1 RETURNING x.*",
	}
	for _, sql := range flagged {
		if starInSQL(sql) == "" {
			t.Errorf("starInSQL(%q) = \"\", want the star flagged", sql)
		}
	}
	allowed := []string{
		"SELECT * FROM (SELECT 1 AS a) s",
		"SELECT * FROM unnest($1::text[], $2::bigint[])",
		"SELECT count(*) FROM chats",
		"SELECT COUNT(*), max(a) FROM chats",
		"SELECT a * 2, b FROM t",
		"SELECT 1 FROM pending_input_gates g",
		`SELECT c."id", c."title" FROM chats_with_activity c`,
		"RETURNING id, title",
		"issues.*",
		"nodes.<id>.message.*",
	}
	for _, sql := range allowed {
		if got := starInSQL(sql); got != "" {
			t.Errorf("starInSQL(%q) = %q, want it allowed", sql, got)
		}
	}
}

func TestNoStarSelects(t *testing.T) {
	t.Parallel()

	repo := root(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "testdata": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true,
		"gen": true, "web": true, "electron": true, "proto-vendor": true,
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repo && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		report := func(pos token.Pos, what string) {
			t.Errorf("%s:%d selects %q. A star is expanded when the statement is prepared, so a migration that "+
				"adds a column breaks every statement prepared before it (\"cached plan must not change result "+
				"type\"). Name the model's columns: ColumnExpr/Returning(\"?TableColumns\") on a builder query, "+
				"columnsOf[T](alias) in raw SQL (internal/store/columns.go).", rel, fset.Position(pos).Line, what)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || !builderStarMethods[sel.Sel.Name] || len(n.Args) == 0 {
					return true
				}
				if arg, ok := stringLit(n.Args[0]); ok && starArg.MatchString(arg) {
					report(n.Pos(), sel.Sel.Name+"("+strconv.Quote(arg)+")")
				}
			case *ast.BasicLit:
				if lit, ok := stringLit(n); ok {
					if star := starInSQL(lit); star != "" {
						report(n.Pos(), star)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repo, err)
	}
}

// stringLit is the value of a string literal expression.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
