package generator

import (
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// Idiomatic, lossless entity types (reliant P5b, F12.6): Go initialisms,
// an integer as wide as its column, a json document as json.RawMessage, and
// a per-column forge:go-type. pkg/crud's repo_idiomatic_types_pg_test.go
// round-trips these exact types through postgres.
func TestPlanORM_IdiomaticLosslessTypes(t *testing.T) {
	ent := config.PlanEntity{
		Name: "MessageContentBlock", TableName: "message_content_blocks",
		Fields: []config.PlanEntityField{
			{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
			{Name: "message_id", Type: "string", NotNull: true},
			{Name: "position", Type: "int32", NotNull: true},
			{Name: "depth", Type: "int16", NotNull: true},
			{Name: "total", Type: "int64", NotNull: true},
			{Name: "payload", Type: "json", NotNull: true},
			{Name: "block_type", Type: "int32", NotNull: true, GoType: "github.com/acme/app/gen/app/v1;appv1.ContentBlockType"},
			{Name: "attrs", Type: "json", GoType: "map[string]any"},
		},
	}
	src := string(renderORMEntity("db", ent, false))
	for field, want := range map[string]string{
		"ID":        "string `bun:\"id,pk\"`",
		"MessageID": "string `bun:\"message_id,notnull\"`",
		"Position":  "int32 `bun:\"position,notnull\"`",
		"Depth":     "int16 `bun:\"depth,notnull\"`",
		"Total":     "int64 `bun:\"total,notnull\"`",
		"Payload":   "json.RawMessage `bun:\"payload,notnull\"`",
		"BlockType": "appv1.ContentBlockType `bun:\"block_type,notnull\"`",
		"Attrs":     "map[string]any `bun:\"attrs\"`",
	} {
		m := regexp.MustCompile(`(?m)^\t` + field + `\s+(.+)$`).FindStringSubmatch(src)
		if m == nil {
			t.Errorf("no %s field in:\n%s", field, src)
			continue
		}
		if got := strings.Join(strings.Fields(m[1]), " "); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}
	for _, imp := range []string{`"encoding/json"`, `appv1 "github.com/acme/app/gen/app/v1"`} {
		if !strings.Contains(src, imp) {
			t.Errorf("missing import %s:\n%s", imp, src)
		}
	}
	if !strings.Contains(src, `MessageContentBlockFieldMessageID = "message_id"`) {
		t.Errorf("field constants follow the field names:\n%s", src)
	}
}
