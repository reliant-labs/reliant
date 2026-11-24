//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2ECRUDOpsOverDeclaredStore is the acceptance gate for generated CRUD
// ops that persist through a declared store instead of `DB orm.Context`.
//
// The claim is that a handler can name its entity's generated store as its
// one persistence dependency, and then test its CRUD path against the
// harness's generated double with no database at all. Every half of that
// is somewhere else — the ops (codegen), Deps-field reconciliation
// (compose.go), the double and its default (helpers_gen_test.go) — so only
// the path a user walks proves it:
//
//  1. the scaffolded DB shape builds, and its ops still call the ORM's free
//     functions over s.deps.DB (the shape every existing project has);
//  2. replace `DB orm.Context` with `Widgets db.WidgetStore` and regenerate:
//     the ops call s.deps.Widgets, no DB field comes back, compose.go wires
//     the store, and the project builds and vets (its tests compile);
//  3. a handler test driving Create and Get over the generated StubWidgets
//     passes with no database: the run points the test database at a port
//     nothing listens on, so the harness asking for one would fail it;
//  4. the scaffolded lifecycle test, which seeds through WithDB, passes too:
//     the harness defaults the store to the real one over its test database;
//  5. the built server persists a row through the store over HTTP.
func TestE2ECRUDOpsOverDeclaredStore(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "storeapp", "--mod", "example.com/storeapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "storeapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widgets", "v1", "widgets.proto")
	writeFileE2E(t, protoPath, readFileE2E(t, protoPath)+`
// forge:entity
message Widget {
  string id = 1;
  string name = 2;
}
`)
	runCmd(t, projectDir, forgeBin, "scaffold")

	handlerDir := filepath.Join(projectDir, "internal", "handlers", "widgets")
	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")

	// ── 1. The DB shape: unchanged, and it builds ────────────────────────
	ops := readFileE2E(t, opsPath)
	if !strings.Contains(ops, "db.CreateWidget(ctx, s.deps.DB, entity)") {
		t.Fatalf("a Deps with only DB must keep the free-function ops:\n%s", ops)
	}
	runCmd(t, projectDir, "go", "build", "./...")

	// ── 2. Declare the store instead of the handle ───────────────────────
	svcPath := filepath.Join(handlerDir, "service.go")
	svc := readFileE2E(t, svcPath)
	svc = regexp.MustCompile(`\tDB\s+orm\.Context\n`).ReplaceAllString(svc, "\tWidgets db.WidgetStore\n")
	svc = regexp.MustCompile(`(?s)\tif d\.DB == nil \{\n.*?\n\t\}\n`).ReplaceAllString(svc,
		"\tif d.Widgets == nil {\n\t\treturn fmt.Errorf(\"widgets: Deps.Widgets is required\")\n\t}\n")
	if !regexp.MustCompile(`\borm\.`).MatchString(svc) {
		svc = strings.Replace(svc, "\t\"github.com/reliant-labs/forge/pkg/orm\"\n", "", 1)
	}
	svc = strings.Replace(svc, "import (", "import (\n\t\"example.com/storeapp/internal/db\"", 1)
	writeFileE2E(t, svcPath, svc)

	runCmd(t, projectDir, forgeBin, "generate")

	ops = readFileE2E(t, opsPath)
	for _, want := range []string{
		"s.deps.Widgets.CreateWidget(ctx, entity)",
		"s.deps.Widgets.GetWidgetByID(ctx, id)",
		"s.deps.Widgets.ListWidget(ctx, opts...)",
		"s.deps.Widgets.UpdateWidget(ctx, entity)",
		"s.deps.Widgets.DeleteWidget(ctx, id)",
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops must persist through the declared store, missing %q:\n%s", want, ops)
		}
	}
	if strings.Contains(ops, "s.deps.DB") {
		t.Errorf("no op may still reach s.deps.DB once every entity has a store:\n%s", ops)
	}
	if svc := readFileE2E(t, svcPath); regexp.MustCompile(`\tDB\s+orm\.Context`).MatchString(svc) {
		t.Errorf("generate re-injected `DB orm.Context`, which no op uses any more:\n%s", svc)
	}
	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	if !strings.Contains(compose, "db.NewStore(g.ORM()).Widgets()") {
		t.Errorf("compose.go must wire the store from the graph's ORM:\n%s", compose)
	}
	helpers := readFileE2E(t, filepath.Join(handlerDir, "helpers_gen_test.go"))
	for _, want := range []string{
		"deps.Widgets = db.NewStore(cfg.database()).Widgets()",
		"type StubWidgets struct",
		"func WithDB(",
	} {
		if !strings.Contains(helpers, want) {
			t.Errorf("helpers_gen_test.go missing %q:\n%s", want, helpers)
		}
	}
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")

	// ── 3. A handler test over the generated double, no database ─────────
	writeFileE2E(t, filepath.Join(handlerDir, "store_double_test.go"), `package widgets_test

import (
	"context"
	"testing"

	pb "example.com/storeapp/gen/services/widgets/v1"
	"example.com/storeapp/internal/db"
	widgets "example.com/storeapp/internal/handlers/widgets"
)

func TestWidgetsOverTheStoreDouble(t *testing.T) {
	rows := map[string]*db.Widget{}
	store := &widgets.StubWidgets{
		CreateWidgetFunc: func(_ context.Context, w *db.Widget) error {
			w.ID = "w-1"
			rows[w.ID] = w
			return nil
		},
		GetWidgetByIDFunc: func(_ context.Context, id string) (*db.Widget, error) {
			return rows[id], nil
		},
	}
	svc := widgets.NewTestWidgets(t, widgets.WithWidgetsDeps(widgets.Deps{Widgets: store}))
	ctx := widgets.AuthedContext(t)
	created, err := svc.CreateWidget(ctx, &pb.CreateWidgetRequest{Name: "gizmo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.GetWidget(ctx, &pb.GetWidgetRequest{Id: created.GetWidget().GetId()})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetWidget().GetName() != "gizmo" || got.GetWidget().GetId() != "w-1" {
		t.Fatalf("get returned %+v, want the row the double stored", got.GetWidget())
	}
	if n := store.CallCount("CreateWidget"); n != 1 {
		t.Fatalf("CreateWidget reached the store %d times, want 1", n)
	}
}
`)
	// The test database points at a port nothing listens on: if the
	// harness asked for one, the test would fail, so a pass is the proof
	// that the double-backed path never touches postgres.
	cmd := exec.Command("go", "test", "-count=1", "-run", "^TestWidgetsOverTheStoreDouble$", "-v", "./internal/handlers/widgets/")
	cmd.Dir = projectDir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(),
		"GOFLAGS=",
		pgtest.EnvBaseURL+"=postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1",
	))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestWidgetsOverTheStoreDouble") {
		t.Fatalf("the double-backed handler test did not pass without a database (%v):\n%s", err, out)
	}

	// ── 4. The scaffolded lifecycle test, over the default real store ────
	runCmd(t, projectDir, "go", "test", "-count=1", "./internal/handlers/widgets/")

	// ── 5. Production composition persists through the store ─────────────
	bootStoreSeamRoundTrip(t, projectDir)
}
