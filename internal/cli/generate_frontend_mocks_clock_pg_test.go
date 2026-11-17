// File: internal/cli/generate_frontend_mocks_clock_pg_test.go
//
// `forge generate`'s frontend mock fixtures are committed files, so they must
// be a pure function of the project's inputs. They read the wall clock
// instead: the mocks were built from the same seed config `forge db seed`
// uses, which anchors relative time at the current UTC day. Every committed
// src/mocks/*_gen.ts therefore went stale at 00:00Z, and a project's
// "generated code is up to date" CI check went red daily with no source
// change.
//
// This drives the real generate step on either side of midnight UTC and
// requires byte-identical output.
//
// Gated behind testing.Short(): the seed projection applies the migrations to
// a real postgres (pgtest).

package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// clockTestMigration carries every way a seeded timestamp is synthesized:
// the managed created_at/updated_at pair and an undescribed TIMESTAMPTZ.
const clockTestMigration = `
CREATE TABLE events (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    starts_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func TestGenerateFrontendMocks_OutputDoesNotDependOnTheDay(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds the seed projection against a real postgres; runs in task test")
	}

	project := t.TempDir()
	migDir := filepath.Join(project, "db", "migrations")
	feRel := filepath.Join("frontends", "web")
	for _, d := range []string{migDir, filepath.Join(project, feRel)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(migDir, "000001_events.up.sql"), []byte(clockTestMigration), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}}}
	svc := codegen.ServiceDef{
		Name: "EventService", Package: "events.v1", ProtoFile: "proto/services/events/v1/events.proto",
		Methods: []codegen.Method{
			{Name: "ListEvents", InputType: "ListEventsRequest", OutputType: "ListEventsResponse"},
			{Name: "GetEvent", InputType: "GetEventRequest", OutputType: "GetEventResponse"},
			{Name: "CreateEvent", InputType: "CreateEventRequest", OutputType: "CreateEventResponse"},
		},
	}
	ts := func(name, goName string) codegen.EntityField {
		return codegen.EntityField{
			Name: name, GoName: goName, ProtoType: "message", GoType: "*timestamppb.Timestamp",
			Kind: codegen.FieldKindMessage, MessageType: "google.protobuf.Timestamp",
		}
	}
	entity := codegen.EntityDef{
		Name: "Event", TableName: "events", PkField: "id", ProtoFile: svc.ProtoFile,
		Fields: []codegen.EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "title", GoName: "Title", ProtoType: "string", GoType: "string", Kind: codegen.FieldKindScalar},
			ts("starts_at", "StartsAt"),
			ts("created_at", "CreatedAt"),
			ts("updated_at", "UpdatedAt"),
		},
	}

	// generateAt runs the generate step with the wall clock reading `now`
	// and returns every file it left under the frontend.
	generateAt := func(now time.Time) map[string][]byte {
		t.Helper()
		prev := seedClock
		seedClock = func() time.Time { return now }
		t.Cleanup(func() { seedClock = prev })

		cs, err := checksums.Load(project)
		if err != nil {
			t.Fatal(err)
		}
		if err := generateFrontendMocks(cfg, []codegen.ServiceDef{svc}, []codegen.EntityDef{entity}, project, cs); err != nil {
			t.Fatalf("generate at %s: %v", now.Format(time.RFC3339), err)
		}
		out := map[string][]byte{}
		root := filepath.Join(project, feRel)
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			out[filepath.ToSlash(rel)] = body
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	before := generateAt(time.Date(2026, 10, 11, 23, 59, 0, 0, time.UTC))
	after := generateAt(time.Date(2026, 10, 12, 0, 1, 0, 0, time.UTC))

	if len(before) != len(after) {
		t.Errorf("generate wrote %d files on 2026-10-11 and %d on 2026-10-12", len(before), len(after))
	}
	for rel, want := range before {
		got, ok := after[rel]
		if !ok {
			t.Errorf("%s was generated on 2026-10-11 but not on 2026-10-12", rel)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs across midnight UTC — committed generated code would go stale daily:\n%s",
				rel, firstDifferingLine(want, got))
		}
	}

	if t.Failed() {
		return
	}

	// Non-vacuous: equal output only means something if the fixture holds
	// the SEEDED timestamps — the only path a clock could reach — rather
	// than dataset-free placeholders, which never read one. That needs the
	// projection to have been built.
	const fixture = "src/mocks/events_gen.ts"
	seed, err := codegen.BuildSeedProjection(project, seedplan.DefaultConfig())
	if err != nil || seed == nil {
		t.Fatalf("no seed projection (err=%v) — the fixtures degraded to placeholders and cannot see a clock", err)
	}
	for _, col := range []string{"starts_at", "created_at", "updated_at"} {
		raw, ok := seed.Value("events", col, 0)
		if !ok {
			t.Fatalf("the plan holds no value for events.%s", col)
		}
		if !bytes.Contains(after[fixture], []byte(raw)) {
			t.Fatalf("%s does not carry the seeded events.%s %q — this test cannot see the clock:\n%s",
				fixture, col, raw, after[fixture])
		}
	}
}

// firstDifferingLine renders the first content line two files disagree on.
// The forge:hash stamp is skipped: it differs whenever anything does, so it
// says nothing about WHAT moved.
func firstDifferingLine(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] && !strings.Contains(al[i], "forge:hash=") {
			return fmt.Sprintf("  line %d:\n  - %s\n  + %s", i+1, al[i], bl[i])
		}
	}
	return "  (one file is a prefix of the other)"
}
