package codegen

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The frontend mock fixtures are checked-in generated code, so they have to be
// a pure function of the project's inputs. They were not: `forge generate`
// handed the projection the RUNTIME seed config, which `forge db seed` anchors
// at the current UTC day so a seeded dev database reads as current. Every
// seeded timestamp therefore moved with the calendar, every committed
// src/mocks/*_gen.ts went stale at 00:00Z, and a project's "generated code is
// up to date" CI check turned red each day with no source change.
//
// These tests pin the projection itself as the guarantee — not the CLI's
// choice of config — so no future caller can hand a clock back in.

// mockClockTestSchema is one table with all three ways a timestamp cell is
// synthesized: the managed created_at/updated_at pair, and an undescribed
// time column the built-in four-week window spreads over.
func mockClockTestSchema() []schemadef.Table {
	return []schemadef.Table{{
		Name:   "events",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "title", Type: schemadef.TypeString, NotNull: true},
			{Name: "starts_at", Type: schemadef.TypeTime, NotNull: true, DeclType: "timestamp with time zone"},
			{Name: "created_at", Type: schemadef.TypeTime, NotNull: true, DeclType: "timestamp with time zone"},
			{Name: "updated_at", Type: schemadef.TypeTime, NotNull: true, DeclType: "timestamp with time zone"},
		},
	}}
}

func mockClockTestEventEntity() EntityDef {
	ts := func(name, goName string) EntityField {
		return EntityField{
			Name: name, GoName: goName, ProtoType: "message", GoType: "*timestamppb.Timestamp",
			Kind: FieldKindMessage, MessageType: "google.protobuf.Timestamp",
		}
	}
	return EntityDef{
		Name: "Event", TableName: "events", PkField: "id",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "title", GoName: "Title", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			ts("starts_at", "StartsAt"),
			ts("created_at", "CreatedAt"),
			ts("updated_at", "UpdatedAt"),
		},
	}
}

// TestMockFixturesDoNotReadTheSeedClock renders the fixtures from configs
// anchored on two different calendar days — exactly what the runtime seed
// config looks like on either side of midnight UTC — and requires the same
// bytes from both, and from a config with no anchor at all.
func TestMockFixturesDoNotReadTheSeedClock(t *testing.T) {
	render := func(now time.Time) MockEntityTemplateData {
		t.Helper()
		cfg := seedplan.DefaultConfig()
		cfg.Now = now
		seed := newSeedProjection(mockClockTestSchema(), cfg, nil)
		if seed == nil {
			t.Fatal("the planner refused this schema — nothing below would be comparing anything")
		}
		return EntityDefToMockData(mockClockTestEventEntity(), ServiceDef{Name: "EventService"}, seed)
	}

	reference := render(time.Time{})
	if len(reference.Records) == 0 {
		t.Fatal("no mock records rendered — every assertion below would pass vacuously")
	}
	// Non-vacuous: the timestamp cells must come FROM the seed plan (the
	// only place a clock could leak in), not from the dataset-free
	// placeholder, which never read one.
	plan := newSeedProjection(mockClockTestSchema(), seedplan.DefaultConfig(), nil)
	for _, tc := range []struct{ column, field string }{
		{"starts_at", "startsAt"}, {"created_at", "createdAt"}, {"updated_at", "updatedAt"},
	} {
		raw, ok := plan.Value("events", tc.column, 0)
		if !ok {
			t.Fatalf("the plan holds no value for events.%s — this test cannot see the clock", tc.column)
		}
		if got := mockRecordValue(t, reference.Records[0], tc.field); !strings.Contains(got, raw) {
			t.Fatalf("%s = %s is not the seeded cell %q — this test cannot see the clock", tc.field, got, raw)
		}
	}

	beforeMidnight := render(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	afterMidnight := render(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	for name, got := range map[string]MockEntityTemplateData{
		"anchored 2026-10-11": beforeMidnight,
		"anchored 2026-10-12": afterMidnight,
	} {
		if !reflect.DeepEqual(got, reference) {
			t.Errorf("fixtures from a config %s differ from the unanchored ones:\n got  %v\n want %v\n"+
				"checked-in mocks would be rewritten whenever the calendar moved",
				name, got.Records[0].Fields, reference.Records[0].Fields)
		}
	}
}
