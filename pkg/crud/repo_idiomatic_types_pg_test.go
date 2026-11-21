package crud

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/uptrace/bun"
)

// block is tagged and typed exactly as internal/generator projects these
// columns (TestPlanORM_IdiomaticLosslessTypes pins that half): integers as
// wide as their column, a jsonb document as json.RawMessage, and
// forge:go-type overrides (a named integer, a map over jsonb, a Duration
// over a bigint).
type block struct {
	bun.BaseModel `bun:"table:blocks,alias:blocks"`

	ID       string          `bun:"id,pk"`
	Position int32           `bun:"position,notnull"`
	Depth    int16           `bun:"depth,notnull"`
	Payload  json.RawMessage `bun:"payload,notnull"`
	Extra    json.RawMessage `bun:"extra"`
	Kind     blockKind       `bun:"kind,notnull"`
	Attrs    map[string]any  `bun:"attrs"`
	TTL      time.Duration   `bun:"ttl,notnull"`
}

type blockKind int32

func TestRepo_IdiomaticTypesRoundTrip(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	if _, err := db.Bun().ExecContext(ctx, `CREATE TABLE blocks (
		id TEXT PRIMARY KEY, position INTEGER NOT NULL, depth SMALLINT NOT NULL,
		payload JSONB NOT NULL, extra JSONB, kind INTEGER NOT NULL, attrs JSONB, ttl BIGINT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo[block]()
	in := &block{
		ID: "b1", Position: 2147483647, Depth: -32768,
		Payload: json.RawMessage(`{"text":"hi","n":[1,2]}`),
		Kind:    3, Attrs: map[string]any{"bold": true}, TTL: 90 * time.Second,
	}
	if err := repo.Create(ctx, db, in); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(ctx, db, "b1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Position != 2147483647 || got.Depth != -32768 || got.Kind != 3 || got.TTL != 90*time.Second {
		t.Errorf("numbers did not round-trip: %+v", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(got.Payload, &doc); err != nil || doc["text"] != "hi" {
		t.Errorf("payload = %s (%v), want the stored document", got.Payload, err)
	}
	if got.Extra != nil {
		t.Errorf("a NULL jsonb column must read back nil, got %q", got.Extra)
	}
	if got.Attrs["bold"] != true {
		t.Errorf("attrs = %v", got.Attrs)
	}
}
