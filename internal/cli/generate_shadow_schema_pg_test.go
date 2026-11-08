package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// TestGenerate_OneRunAppliesTheShadowSchemaOnce pins the run scope at the
// level that owns it: the generate pipeline. Two steps read the applied
// schema the way the production readers do — one the model, one a live copy
// — and the run must apply the migrations once between them, hand both the
// schema a private apply produces, report it in --timings, and leave no
// shadow database behind once it returns.
func TestGenerate_OneRunAppliesTheShadowSchemaOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: applies migrations to a real postgres; runs in task test")
	}
	dir := t.TempDir()
	migDir := filepath.Join(dir, "db", "migrations")
	writeTreeFileT(t, filepath.Join(migDir, "00001_init.up.sql"), `
CREATE TABLE widgets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (char_length(name) > 0)
);
`)
	want, err := schemadef.ApplyAndIntrospectAt(migDir, shadowdb.Resolve(dir))
	if err != nil {
		t.Fatalf("private apply: %v", err)
	}

	var modelRead, liveRead []schemadef.Table
	withPipelineSteps(t, []GenStep{
		{
			Name: "read the applied schema", Gate: always, Tag: "codegen", ReadOnly: true,
			Run: func(ctx *pipelineContext) error {
				var err error
				modelRead, err = shadowdb.Tables(ctx.ProjectDir, migDir)
				return err
			},
		},
		{
			Name: "execute against the applied schema", Gate: always, Tag: "codegen", ReadOnly: true,
			Run: func(ctx *pipelineContext) error {
				tables, shadow, err := shadowdb.Open(ctx.ProjectDir, migDir)
				if err != nil {
					return err
				}
				defer shadow.Close()
				liveRead = tables
				_, err = shadow.DB().Exec(`INSERT INTO widgets (id, name) VALUES ('w1', 'one')`)
				return err
			},
		},
	})
	t.Setenv(generateTimingsEnv, "1")
	start := shadowdb.CurrentStats()
	out := runPipelineOK(t, dir)
	stats := shadowdb.StatsSince(start)

	if stats.Applies != 1 || stats.Reuses != 1 {
		t.Fatalf("one generate run, two readers: applied %d×, reused %d× — want one apply, one reuse", stats.Applies, stats.Reuses)
	}
	if !reflect.DeepEqual(modelRead, want) || !reflect.DeepEqual(liveRead, want) {
		t.Fatalf("a reader in the run saw a schema other than a private apply's:\n model %+v\n live  %+v\n want  %+v", modelRead, liveRead, want)
	}
	if !strings.Contains(out, "shadow schema: migrations applied 1×") || !strings.Contains(out, "reused by 1 more reader(s)") {
		t.Errorf("--timings must say how many times the run applied the migrations:\n%s", out)
	}

	// The run is over: its template is gone, so the next read applies
	// afresh rather than reusing a database the run should have dropped.
	start = shadowdb.CurrentStats()
	if _, err := shadowdb.Tables(dir, migDir); err != nil {
		t.Fatal(err)
	}
	if s := shadowdb.StatsSince(start); s.Reuses != 0 {
		t.Fatalf("a read after the generate run returned reused its template: %+v", s)
	}
}
