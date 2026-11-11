package generator

import (
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// The generator half of F12.5 (reliant P5b): a NOT NULL column whose
// DEFAULT is not its Go zero says "unset" with its TYPE. pkg/crud's
// repo_insert_default_pg_test.go runs these exact tags against postgres.
func TestPlanORM_NotNullDefaultsSayUnsetWithTheirType(t *testing.T) {
	ent := config.PlanEntity{
		Name:      "Thread",
		TableName: "threads",
		Fields: []config.PlanEntityField{
			{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
			{Name: "title", Type: "string", NotNull: true},
			{Name: "status", Type: "int64", NotNull: true, Default: "2"},
			{Name: "pinned", Type: "bool", NotNull: true, Default: "true"},
			{Name: "kind", Type: "string", NotNull: true, Default: "'llm'::text"},
			{Name: "metadata", Type: "json", NotNull: true, Default: "'{}'::jsonb"},
			{Name: "labels", Type: "[]string", NotNull: true, Default: "'{inbox}'::text[]"},
			{Name: "unread", Type: "int64", NotNull: true, Default: "0"},
			{Name: "empty_tags", Type: "[]string", NotNull: true, Default: "'{}'::text[]"},
			{Name: "tone", Type: "string", NotNull: true, Default: "'TONE_WARM'::text", ZeroRejected: true},
		},
	}
	src := string(renderORMEntity("db", ent, false))
	for field, want := range map[string]string{
		"Status":    "*int64 `bun:\"status,notnull\"`",
		"Pinned":    "*bool `bun:\"pinned,notnull\"`",
		"Kind":      "*string `bun:\"kind,notnull\"`",
		"Metadata":  "json.RawMessage `bun:\"metadata,notnull,nullzero\"`",
		"Labels":    "[]string `bun:\"labels,array,notnull,nullzero\"`",
		"Unread":    "int64 `bun:\"unread,notnull\"`",
		"EmptyTags": "[]string `bun:\"empty_tags,array,notnull\"`",
		"Title":     "string `bun:\"title,notnull\"`",
		// A born enum column: its CHECK refuses "", so it keeps its type.
		"Tone": "string `bun:\"tone,notnull,nullzero\"`",
	} {
		re := regexp.MustCompile(`(?m)^\t` + field + `\s+(.+)$`)
		m := re.FindStringSubmatch(src)
		if m == nil {
			t.Errorf("no %s field in:\n%s", field, src)
			continue
		}
		if got := strings.Join(strings.Fields(m[1]), " "); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}
}
