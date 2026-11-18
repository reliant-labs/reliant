package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/config"
)

// TestGeneratePlanORM_OutputTypeChecks COMPILES the emitted ORM code —
// the gate the kalshi escapes proved was missing. fr-3fba9166ba shipped
// because every unit test asserted on substrings of the render while
// the render itself could never pass the type checker (`undefined:
// time`, IsZero on a string). The entity matrix here covers every
// stamping/PK branch the emitter has:
//
//   - string PK + soft-delete + declared NOT NULL time columns
//   - legacy TEXT created_at/updated_at (string stamps)
//   - nullable managed columns (pointer stamps), time and string
//   - server-allocated int64 PKs, with and without timestamps
//   - unstampable epoch-integer "timestamps" (no stamping emitted)
//   - id-only integer-PK table (DEFAULT VALUES, no strings import)
//
// The generated files are type-checked in-module via a go/packages
// overlay (they import forge/pkg/orm, otel, ulid — all dependencies of
// this repo), so this is a real compile, not a substring proxy.
// Full-mode only: loading the dependency graph takes seconds.
func TestGeneratePlanORM_OutputTypeChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checking generated output loads the full dependency graph (>2s)")
	}

	root := t.TempDir()
	entities := []config.PlanEntity{
		{
			Name: "Project", Timestamps: true, SoftDelete: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "org_id", Type: "string", NotNull: true},
				{Name: "name", Type: "string", NotNull: true},
				{Name: "tags", Type: "[]string"},
				{Name: "created_at", Type: "time", NotNull: true},
				{Name: "updated_at", Type: "time", NotNull: true},
			},
		},
		{
			Name: "Trade", Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "ticker", Type: "string", NotNull: true},
				{Name: "created_at", Type: "string", NotNull: true},
				{Name: "updated_at", Type: "string", NotNull: true},
			},
		},
		{
			Name: "Audit", Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "created_at", Type: "time"},
				{Name: "updated_at", Type: "time"},
			},
		},
		{
			Name: "Legacy", Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "created_at", Type: "string"},
				{Name: "updated_at", Type: "string"},
			},
		},
		{
			Name: "Hypothesis", Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "int64", PrimaryKey: true, NotNull: true},
				{Name: "title", Type: "string", NotNull: true},
				{Name: "created_at", Type: "time", NotNull: true},
				{Name: "updated_at", Type: "time", NotNull: true},
			},
		},
		{
			Name: "Tick",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "int64", PrimaryKey: true, NotNull: true},
				{Name: "label", Type: "string", NotNull: true},
			},
		},
		{
			Name: "Epoch", Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "created_at", Type: "int64", NotNull: true},
				{Name: "updated_at", Type: "int64", NotNull: true},
			},
		},
		{
			Name: "Only",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "int64", PrimaryKey: true, NotNull: true},
			},
		},
		// Idiomatic, lossless types (F12.6): width-refined integers, a json
		// document, and forge:go-type overrides — builtin, stdlib and a
		// qualified package with its own name.
		{
			Name: "Block", TableName: "blocks",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "int32", PrimaryKey: true, NotNull: true},
				{Name: "position", Type: "int32", NotNull: true},
				{Name: "priority", Type: "int16"},
				{Name: "seqs", Type: "[]int32"},
				{Name: "payload", Type: "json", NotNull: true},
				{Name: "extra", Type: "json"},
				{Name: "attrs", Type: "json", GoType: "map[string]any"},
				{Name: "ttl", Type: "int64", NotNull: true, GoType: "time.Duration"},
				{Name: "kind", Type: "int32", GoType: "github.com/reliant-labs/forge/pkg/orm;orm.Isolation"},
			},
		},
		// Composite primary keys (F12.3): a counter, a join table whose
		// key column is a Go keyword, and an append-only one.
		{
			Name: "MessageOrderCounter", TableName: "message_order_counters", Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "chat_id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "kind", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "next_seq", Type: "int64", NotNull: true},
				{Name: "created_at", Type: "time", NotNull: true},
				{Name: "updated_at", Type: "time", NotNull: true},
			},
		},
		{
			Name: "ProjectDaemon", TableName: "project_daemons", SoftDelete: true,
			Fields: []config.PlanEntityField{
				{Name: "project_id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "type", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "seq", Type: "int64", PrimaryKey: true, NotNull: true},
				{Name: "deleted_at", Type: "time"},
			},
		},
		{
			Name: "LedgerLine", TableName: "ledger_lines", AppendOnly: true,
			Fields: []config.PlanEntityField{
				{Name: "ledger_id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "line", Type: "int64", PrimaryKey: true, NotNull: true},
				{Name: "amount", Type: "int64", NotNull: true},
			},
		},
		// Read-only view entities (F12.7): keyed, and keyless.
		{
			Name: "ChatsWithActivity", TableName: "chats_with_activity", ReadOnly: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "title", Type: "string"},
				{Name: "last_seen", Type: "time"},
				{Name: "has_unread", Type: "bool"},
			},
		},
		{
			Name: "ChatCount", TableName: "chat_counts", ReadOnly: true,
			Fields: []config.PlanEntityField{
				{Name: "n", Type: "int64"},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}

	dbDir := filepath.Join(root, "internal", "db")
	files, err := os.ReadDir(dbDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	// Overlay the generated files into a synthetic package inside THIS
	// module so all their imports resolve through forge's own go.mod.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	synthDir := filepath.Join(repoRoot, "internal", "generator", "zz_orm_typecheck", "db")
	overlay := map[string][]byte{}
	var goFiles []string
	for _, f := range files {
		content, rerr := os.ReadFile(filepath.Join(dbDir, f.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", f.Name(), rerr)
		}
		// github.com/oklog/ulid/v2 is a dependency of generated PROJECTS,
		// not of the forge module itself, so the overlay can't resolve
		// it. Substitute the one expression that uses it; everything
		// else type-checks for real. (The ULID chokepoint's emission is
		// pinned by TestGeneratePlanORM_Basic.)
		src := strings.ReplaceAll(string(content), "\t\"github.com/oklog/ulid/v2\"\n", "")
		src = strings.ReplaceAll(src, "ulid.Make().String()", `"ulid-stub"`)
		path := filepath.Join(synthDir, f.Name())
		overlay[path] = []byte(src)
		goFiles = append(goFiles, path)
	}

	// A caller, compiled with the generated package: every generated
	// by-key write takes the options its pkg/crud call does, through the
	// delegates and through the stores, single and composite key alike.
	// (P5b L4 had to reach for the unexported <e>Repo to pass NoReadBack.)
	overlay[filepath.Join(synthDir, "zz_update_options_caller.go")] = []byte(`package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
)

func callerPassesUpdateOptions(ctx context.Context, d orm.Context, p *Project, c *MessageOrderCounter, ps ProjectStore, cs MessageOrderCounterStore) error {
	if err := UpdateProjectMasked(ctx, d, p, []string{"name"}, crud.NoReadBack()); err != nil {
		return err
	}
	if err := UpdateProject(ctx, d, p, crud.NoReadBack(), crud.Preserve("name")); err != nil {
		return err
	}
	if err := UpdateMessageOrderCounterMasked(ctx, d, c, []string{"next_seq"}, crud.NoReadBack()); err != nil {
		return err
	}
	if err := ps.UpdateProjectMasked(ctx, p, []string{"name"}, crud.NoReadBack()); err != nil {
		return err
	}
	if err := ps.UpdateProject(ctx, p, crud.NoReadBack()); err != nil {
		return err
	}
	if err := cs.UpdateMessageOrderCounterMasked(ctx, c, []string{"next_seq"}, crud.NoReadBack()); err != nil {
		return err
	}
	return cs.UpdateMessageOrderCounter(ctx, c, crud.NoReadBack())
}
`)

	cfg := &packages.Config{
		Mode:    packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Dir:     repoRoot,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, "file="+goFiles[0])
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("packages.Load returned no packages")
	}
	failed := false
	for _, p := range pkgs {
		for _, e := range p.Errors {
			failed = true
			t.Errorf("generated ORM code does not compile: %v", e)
		}
	}
	if failed {
		for _, f := range files {
			content, _ := os.ReadFile(filepath.Join(dbDir, f.Name()))
			t.Logf("==== %s ====\n%s", f.Name(), content)
		}
	}
}
