package database

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/sqlident"
)

// MigrationOptions controls what context is included when creating a new migration.
type MigrationOptions struct {
	DSN string // If set, introspect the live DB for schema

	// Context writes the full context block — the schema, the previous
	// migration and the history — into the new file instead of a pointer to
	// where they live. Set by --context, and implied by DSN.
	Context bool

	// The compatibility declaration written at the top of the file (see
	// compatHeader). At most one of BackwardCompatible and BreaksRollback,
	// each the reason; RequireCompatDeclaration writes the choices as
	// examples when neither is given.
	BackwardCompatible       string
	BreaksRollback           string
	RequireCompatDeclaration bool

	// Body is SQL written after the header, for a migration whose content
	// forge already knows (`forge db migration new --closed-set`). Empty
	// leaves the file for the author to write.
	Body string
}

// MigrationContext holds all gathered context for generating rich migration comments.
type MigrationContext struct {
	MigrationName     string
	CreatedAt         time.Time
	ParsedTables      []ParsedTable // Schema reconstructed from existing migrations
	LiveTables        []Table       // Schema from live DB introspection (if --dsn)
	PreviousMigration *PreviousMigrationInfo
	MigrationHistory  []string // List of all previous migration filenames
}

// ParsedTable represents a table reconstructed from parsing migration SQL files.
type ParsedTable struct {
	Name    string
	Columns []ParsedColumn
}

// ParsedColumn represents a column extracted from a CREATE TABLE or ALTER TABLE statement.
type ParsedColumn struct {
	Name       string
	Type       string
	Constraint string // e.g. "PRIMARY KEY", "NOT NULL", "UNIQUE", "REFERENCES users(id)"
}

// PreviousMigrationInfo holds information about the most recent migration.
type PreviousMigrationInfo struct {
	Filename string
	Content  string
}

// --- Schema parsing from migration files ---

var (
	// Table names may be schema-qualified and quoted (pg_dump writes
	// `CREATE TABLE public.tasks`); sqlident.Last reads the table out of
	// whatever these capture.
	createTableRe = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + sqlident.Qualified + `)\s*\(`)
	alterTableRe  = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:ONLY\s+)?(` + sqlident.Qualified + `)\s+ADD\s+(?:COLUMN\s+)?(` + sqlident.Part + `)\s+(.+?)(?:;|$)`)
	dropTableRe   = regexp.MustCompile(`(?i)DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(` + sqlident.Qualified + `)`)
	columnLineRe  = regexp.MustCompile(`^\s*(\w+)\s+(\S+(?:\s+\S+)?)\s*(.*?)(?:,?\s*)$`)
)

// ParseMigrationsForSchema reads all .up.sql files in dir (sorted by name) and
// reconstructs a best-effort schema by parsing CREATE TABLE and ALTER TABLE statements.
func ParseMigrationsForSchema(dir string) ([]ParsedTable, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading migrations directory: %w", err)
	}

	// Collect and sort .up.sql files.
	var upFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			upFiles = append(upFiles, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(upFiles)

	tableMap := make(map[string]*ParsedTable)

	for _, path := range upFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		parseSQL(string(content), tableMap)
	}

	// Convert map to sorted slice.
	var tables []ParsedTable
	for _, t := range tableMap {
		tables = append(tables, *t)
	}
	sort.Slice(tables, func(i, j int) bool {
		return tables[i].Name < tables[j].Name
	})

	return tables, nil
}

// parseSQL processes a single SQL file's content and updates the table map.
func parseSQL(content string, tableMap map[string]*ParsedTable) {
	// Strip SQL comment lines (-- ...) to avoid parsing context comments as SQL.
	content = stripSQLComments(content)

	// Handle DROP TABLE — remove from map.
	for _, match := range dropTableRe.FindAllStringSubmatch(content, -1) {
		delete(tableMap, sqlident.Last(match[1]))
	}

	// Handle CREATE TABLE.
	parseCreateTables(content, tableMap)

	// Handle ALTER TABLE ADD COLUMN.
	for _, match := range alterTableRe.FindAllStringSubmatch(content, -1) {
		tableName := sqlident.Last(match[1])
		colName := sqlident.Last(match[2])
		colRest := strings.TrimSpace(match[3])

		t, ok := tableMap[tableName]
		if !ok {
			t = &ParsedTable{Name: tableName}
			tableMap[tableName] = t
		}

		// Parse the type and constraints from the rest.
		parts := strings.SplitN(colRest, " ", 2)
		colType := parts[0]
		constraint := ""
		if len(parts) > 1 {
			constraint = strings.TrimSpace(parts[1])
			// Clean trailing semicolons.
			constraint = strings.TrimRight(constraint, ";")
		}

		t.Columns = append(t.Columns, ParsedColumn{
			Name:       colName,
			Type:       colType,
			Constraint: constraint,
		})
	}
}

// stripSQLComments removes lines that are SQL comments (starting with --).
func stripSQLComments(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "--") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// parseCreateTables extracts CREATE TABLE blocks and their column definitions.
func parseCreateTables(content string, tableMap map[string]*ParsedTable) {
	matches := createTableRe.FindAllStringSubmatchIndex(content, -1)
	for _, loc := range matches {
		tableName := sqlident.Last(content[loc[2]:loc[3]])
		// Find the matching closing paren by counting parens from the opening.
		start := loc[1] // position after the opening paren
		depth := 1
		end := start
		for end < len(content) && depth > 0 {
			switch content[end] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth > 0 {
				end++
			}
		}

		if depth != 0 {
			continue
		}

		body := content[start:end]
		t := &ParsedTable{Name: tableName}
		t.Columns = parseColumnDefs(body)
		tableMap[tableName] = t
	}
}

// parseColumnDefs parses the body between CREATE TABLE parens into column definitions.
func parseColumnDefs(body string) []ParsedColumn {
	var columns []ParsedColumn
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		// Skip table-level constraints (PRIMARY KEY, UNIQUE, CONSTRAINT, CHECK, FOREIGN KEY).
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "PRIMARY KEY") ||
			strings.HasPrefix(upper, "UNIQUE") ||
			strings.HasPrefix(upper, "CONSTRAINT") ||
			strings.HasPrefix(upper, "CHECK") ||
			strings.HasPrefix(upper, "FOREIGN KEY") ||
			strings.HasPrefix(upper, "EXCLUDE") {
			continue
		}

		matches := columnLineRe.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		colName := matches[1]
		colType := matches[2]
		rest := strings.TrimSpace(matches[3])

		// Clean trailing commas.
		rest = strings.TrimRight(rest, ",")
		rest = strings.TrimSpace(rest)

		columns = append(columns, ParsedColumn{
			Name:       colName,
			Type:       colType,
			Constraint: rest,
		})
	}
	return columns
}

// --- Previous migration ---

// migrationBodyMarker is the last line of the header GenerateContextComment
// writes. Everything after it is the migration's own SQL.
const migrationBodyMarker = "-- Write your migration SQL below:"

// migrationBody returns the SQL a migration carries below forge's generated
// context header, or the whole file when it has no such header (a
// hand-written or imported migration).
//
// Without this cut every header would embed the previous file's header,
// which embeds the one before it, so the comment grew one level deeper with
// every migration. Only an unprefixed marker line counts: the marker of an
// older header quoted inside a PREVIOUS MIGRATION section is commented out
// ("-- -- Write ..."), so the first exact match is the file's own.
func migrationBody(content string) string {
	offset := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		offset += len(line)
		if strings.TrimRight(line, " \t\r\n") == migrationBodyMarker {
			return content[offset:]
		}
	}
	return content
}

// GetPreviousMigration reads the most recent .up.sql file from the migrations
// dir. Content is that migration's SQL without forge's generated header.
func GetPreviousMigration(dir string) (*PreviousMigrationInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var upFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			upFiles = append(upFiles, e.Name())
		}
	}

	if len(upFiles) == 0 {
		return nil, nil
	}

	sort.Strings(upFiles)
	latest := upFiles[len(upFiles)-1]

	content, err := os.ReadFile(filepath.Join(dir, latest))
	if err != nil {
		return nil, err
	}

	return &PreviousMigrationInfo{
		Filename: latest,
		Content:  migrationBody(string(content)),
	}, nil
}

// GetMigrationHistory returns a sorted list of all migration filenames (just .up.sql).
func GetMigrationHistory(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// --- Context comment generation ---

// GeneratePointerComment is the header a new migration gets by default: what
// it is and where the schema lives, not the schema itself.
//
// The full context block (GenerateContextComment) copies the reconstructed
// schema, the whole previous migration and every filename into the file. On a
// real project that is thousands of lines — 4,658 on reliant, most of it a
// baseline it had just squashed — in a file whose SQL is three lines, and an
// agent or reviewer reads all of it. The schema is one command away, kept
// current, and not frozen into a migration that will outlive it.
//
// It names no path: the file is committed, and the directory it sits in is
// the one it means.
func GeneratePointerComment(name string, history []string, created time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "-- Migration: %s\n", name)
	fmt.Fprintf(&sb, "-- Created: %s\n", created.Format(time.RFC3339))
	sb.WriteString("--\n")
	switch n := len(history); n {
	case 0:
		sb.WriteString("-- The first migration in this directory: the schema is what this file creates.\n")
	default:
		fmt.Fprintf(&sb, "-- The schema is the %d migration(s) in this directory, applied in order (newest: %s). To read it:\n",
			n, history[n-1])
		sb.WriteString("--   forge project shapes --kind table          every table, with the file:line declaring it\n")
		sb.WriteString("--   forge db introspect --dsn \"$DATABASE_URL\"  the schema a database actually has\n")
		sb.WriteString("--   forge db migration new <name> --context    a new file with the schema written into it\n")
	}
	sb.WriteString("--\n")
	sb.WriteString("-- Write your migration SQL below:\n\n")
	return sb.String()
}

// GenerateContextComment builds the full SQL comment block for a new migration.
func GenerateContextComment(ctx *MigrationContext) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "-- Migration: %s\n", ctx.MigrationName)
	fmt.Fprintf(&sb, "-- Created: %s\n", ctx.CreatedAt.Format(time.RFC3339))
	sb.WriteString("--\n")

	// Schema section.
	hasParsedSchema := len(ctx.ParsedTables) > 0
	hasLiveSchema := len(ctx.LiveTables) > 0

	if hasLiveSchema {
		sb.WriteString("-- === CURRENT SCHEMA (from database) ===\n")
		for _, t := range ctx.LiveTables {
			writeTableComment(&sb, t.Name, liveTableToColumns(t))
		}
		sb.WriteString("--\n")
	} else if hasParsedSchema {
		sb.WriteString("-- === CURRENT SCHEMA (from existing migrations) ===\n")
		for _, t := range ctx.ParsedTables {
			writeParsedTableComment(&sb, t)
		}
		sb.WriteString("--\n")
	}

	// Previous migration.
	if ctx.PreviousMigration != nil {
		fmt.Fprintf(&sb, "-- === PREVIOUS MIGRATION (%s) ===\n", ctx.PreviousMigration.Filename)
		if body := strings.TrimSpace(ctx.PreviousMigration.Content); body == "" {
			sb.WriteString("-- (no SQL)\n")
		} else {
			for _, line := range strings.Split(body, "\n") {
				fmt.Fprintf(&sb, "-- %s\n", line)
			}
		}
		sb.WriteString("--\n")
	}

	// Migration history.
	if len(ctx.MigrationHistory) > 0 {
		sb.WriteString("-- === MIGRATION HISTORY ===\n")
		for _, name := range ctx.MigrationHistory {
			fmt.Fprintf(&sb, "-- %s\n", name)
		}
		sb.WriteString("--\n")
	}

	sb.WriteString(migrationBodyMarker + "\n\n")

	return sb.String()
}

type commentColumn struct {
	name       string
	typeName   string
	constraint string
}

func liveTableToColumns(t Table) []commentColumn {
	var cols []commentColumn
	for _, c := range t.Columns {
		constraint := ""
		var parts []string
		if c.IsPrimary {
			parts = append(parts, "PRIMARY KEY")
		}
		if !c.Nullable {
			parts = append(parts, "NOT NULL")
		}
		if c.Default != "" {
			parts = append(parts, "DEFAULT "+c.Default)
		}
		constraint = strings.Join(parts, " ")

		var typeName string
		if c.UDTName != "" && !strings.EqualFold(c.UDTName, c.Type) {
			typeName = strings.ToUpper(c.UDTName)
		} else {
			typeName = strings.ToUpper(c.Type)
		}

		cols = append(cols, commentColumn{
			name:       c.Name,
			typeName:   typeName,
			constraint: constraint,
		})
	}
	return cols
}

func writeTableComment(sb *strings.Builder, name string, cols []commentColumn) {
	fmt.Fprintf(sb, "--\n-- TABLE %s (\n", name)
	for i, c := range cols {
		line := fmt.Sprintf("--   %s %s", c.name, c.typeName)
		if c.constraint != "" {
			line += " " + c.constraint
		}
		if i < len(cols)-1 {
			line += ","
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("-- );\n")
}

func writeParsedTableComment(sb *strings.Builder, t ParsedTable) {
	fmt.Fprintf(sb, "--\n-- TABLE %s (\n", t.Name)
	for i, c := range t.Columns {
		// The parsed type can still carry the separator it was split on
		// ("image_url TEXT," with no constraint following), and the loop
		// below adds its own — which rendered `image_url TEXT,,`. Strip it
		// so the comment reads as the SQL it is summarizing.
		line := fmt.Sprintf("--   %s %s", c.Name, strings.TrimRight(strings.ToUpper(c.Type), ","))
		if c.Constraint != "" {
			line += " " + strings.TrimRight(c.Constraint, ",")
		}
		if i < len(t.Columns)-1 {
			line += ","
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("-- );\n")
}

// GatherMigrationContext collects all available context for a new migration.
func GatherMigrationContext(ctx context.Context, name, migDir string, opts MigrationOptions) (*MigrationContext, error) {
	mctx := &MigrationContext{
		MigrationName: name,
		CreatedAt:     time.Now().UTC(),
	}

	// Parse existing migrations for schema.
	parsed, err := ParseMigrationsForSchema(migDir)
	if err != nil {
		// Non-fatal: we'll just skip this section.
		parsed = nil
	}
	mctx.ParsedTables = parsed

	// Live DB introspection.
	if opts.DSN != "" {
		db, err := ConnectDB(ctx, opts.DSN)
		if err == nil {
			defer func() { _ = db.Close() }()
			tables, err := IntrospectSchema(ctx, db, "")
			if err == nil {
				mctx.LiveTables = tables
			}
		}
	}

	// Previous migration.
	prev, err := GetPreviousMigration(migDir)
	if err == nil {
		mctx.PreviousMigration = prev
	}

	// Migration history.
	history, err := GetMigrationHistory(migDir)
	if err == nil {
		mctx.MigrationHistory = history
	}

	return mctx, nil
}
