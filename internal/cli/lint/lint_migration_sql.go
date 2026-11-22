// File: internal/cli/lint/lint_migration_sql.go
//
// Shared text-level readers over db/migrations and embedded SQL, used by the
// lanes that must work on a half-migrated project where no postgres is
// available (read-only-fields, time-bucketing). Purely textual: comments are
// blanked, CREATE/ALTER statements are replayed in the order the migrator
// applies them.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/sqlident"
)

// matchParen returns the offset of the ')' closing the '(' at open, ignoring
// parens inside string literals.
func matchParen(s string, open int) (int, bool) {
	depth := 0
	inString := false
	for i := open; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
		case c == '\'':
			inString = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// normIdent strips schema qualification and quoting from an identifier the
// way postgres resolves it: an unquoted name folds to lower case, a quoted
// one is kept verbatim (see sqlident).
func normIdent(s string) string {
	return sqlident.Last(s)
}

// blankSQLComments replaces SQL comments with spaces, preserving both the
// total length and every newline so byte offsets and line numbers still
// address the original text.
//
// This is load-bearing rather than tidy: forge's own birth migration writes
// the foreign key it CANNOT yet apply as a commented-out suggestion for the
// author to uncomment later. Reading comments would make the check treat
// every such suggestion as an applied constraint and flag fixtures against a
// schema that does not exist.
func blankSQLComments(s string) string {
	out := []byte(s)
	inString := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(out) && out[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
		case c == '\'':
			inString = true
		case c == '-' && i+1 < len(out) && out[i+1] == '-':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			for i < len(out) {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					out[i] = ' '
					out[i+1] = ' '
					i++
					break
				}
				if out[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
		}
	}
	return string(out)
}

// ── CREATE TABLE / DROP TABLE structure ────────────────────────────────

var (
	dropTableRe = regexp.MustCompile(
		`(?is)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(` + sqlident.Qualified + `)`)
	createTableRe = regexp.MustCompile(
		`(?is)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + sqlident.Qualified + `)\s*\(`)
)

// isConstraintKeyword reports whether tok opens a table-level constraint
// clause rather than naming a column.
func isConstraintKeyword(tok string) bool {
	switch tok {
	case "primary", "unique", "check", "foreign", "constraint", "exclude", "like":
		return true
	}
	return false
}

// splitTopLevel splits a CREATE TABLE body on commas at paren depth zero,
// keeping each column definition and table constraint whole.
func splitTopLevel(body string) []string {
	var out []string
	depth := 0
	inString := false
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(body) && body[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
		case c == '\'':
			inString = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, body[start:i])
			start = i + 1
		}
	}
	return append(out, body[start:])
}

// eachMigration walks the project's .up.sql files in lexical order — the
// order the migrator applies them — and hands each one's root-relative path
// and comment-blanked content to fn.
//
// Comments are blanked because forge's own birth migration writes the
// constraints it CANNOT yet apply as commented-out suggestions. Reading
// those would build a schema that does not exist and report fixtures against
// constraints nobody has applied.
func eachMigration(root, migrationsDir string, fn func(relPath, content string)) error {
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		return nil
	}
	var files []string
	if err := filepath.WalkDir(migrationsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".up.sql") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("walk %s: %w", migrationsDir, err)
	}
	sort.Strings(files)

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fn(filepath.ToSlash(rel), blankSQLComments(string(data)))
	}
	return nil
}
