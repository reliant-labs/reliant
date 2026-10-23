//go:build e2e

package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertStaticLinksResolve serves outDir the way hosted static hosting
// resolves a path — the object itself, then `<path>.html`, then
// `<path>/index.html`, with NO SPA fallback — and asserts each URL lands on
// the exported page named for it, byte for byte. A fallback is left out on
// purpose: hosted would answer a missing route with the root index.html, a
// 200 that is the dashboard, not the page the link meant.
func assertStaticLinksResolve(t *testing.T, outDir string, links map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		candidates := []string{p}
		if path.Ext(p) == "" {
			candidates = append(candidates, p+".html", path.Join(p, "index.html"))
		}
		for _, c := range candidates {
			f := filepath.Join(outDir, filepath.FromSlash(c))
			if info, err := os.Stat(f); err == nil && info.Mode().IsRegular() {
				http.ServeFile(w, r, f)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	for link, page := range links {
		resp, err := http.Get(srv.URL + link)
		if err != nil {
			t.Fatalf("GET %s: %v", link, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		want, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(page)))
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		if resp.StatusCode != http.StatusOK || string(body) != string(want) {
			t.Errorf("GET %s = %d (%d bytes), want 200 with out/%s", link, resp.StatusCode, len(body), page)
		}
	}
}

// TestE2EScaffoldFrontendBuilds scaffolds a project with a --frontend web
// plus ONE CRUD entity, and drives the frontend through its real
// toolchain:
//
//	npm install
//	npm run build
//	npm test
//	npx tsc --noEmit
//
// The CRUD entity (`forge scaffold service item` → Get/List/Create RPCs) is
// load-bearing: it makes `forge generate` emit the detail route
// (`src/app/items/view/page.tsx`), hooks, and dashboard tiles — the
// exact generated surface that historically broke a pristine project:
//
//   - `npm run build` failed under a static export
//     ('Page "/items/[id]" is missing "generateStaticParams()" so it
//     cannot be used with "output: export"') while the generated detail
//     page was a dynamic `[id]` route. The frontend is a static export by
//     default now, so the build below is that export, and it must emit a
//     page per generated route into out/.
//   - `npm test` failed ('No QueryClient set') because page.test.tsx
//     rendered the dashboard bare while dashboard_gen.tsx calls the
//     generated list hooks once an entity exists.
//
// A zero-entity scaffold would pass both while every real project (the
// moment it has one entity) fails — so the entity is part of the gate.
//
// The step split exists because each step guards a different kind of
// regression:
//
//   - `npm install` catches package.json/lockfile issues (unresolvable
//     deps, version conflicts) before any code runs.
//   - `npm run build` exercises the whole build pipeline (Next compile,
//     buf-generated code import graph, Tailwind, etc). This is the big
//     one — failures here usually point at a template regression in
//     one of the src/**/*.tsx files.
//   - `npm test` runs the scaffolded vitest suite (page.test.tsx and
//     any generated hook tests) — a pristine project must be green.
//   - `npx tsc --noEmit` is a stricter type-only check that catches
//     cases where `next build` might elide typing issues (legacy compat
//     flags, SWC-only paths).
//
// Every such call passes `--no-install`, and that flag is load-bearing.
// Bare `npx tsc` falls back to FETCHING a package named "tsc" when the
// frontend's node_modules has no local binary — and `tsc` on npm is an
// unrelated abandoned 2.0.4 package that prints "This is not the tsc
// command you are looking for" and exits 1. So a missing local typescript
// surfaced as an inscrutable failure from a package the project never
// depends on. --no-install makes the real condition ("typescript is not
// installed here") the error, and never reaches the network.
//
// node/npm are a REQUIREMENT, not a preference: requireTool skips on a
// laptop that lacks them and FAILS under CI, where provisioning them is
// .github/workflows/e2e-suite.yml's job. The old spelling skipped in both
// places, which is how this gate spent its life reporting green from a
// runner that never had Node.
//
// Module wiring uses the corpus-style local replace
// (addCorpusForgePkgReplace), like every scaffold e2e test: the forge
// under test compiles against forge/pkg from the same commit.
func TestE2EScaffoldFrontendBuilds(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin,
		"project", "new", "feapp",
		"--mod", "example.com/feapp",
		"--frontend", "web",
	)

	projectDir := filepath.Join(dir, "feapp")
	addCorpusForgePkgReplace(t, projectDir)

	// One CRUD entity so the generated frontend surface (dynamic [id]
	// detail page, list/create pages, hooks, dashboard tiles) exists.
	// `forge scaffold service` scaffolds an empty proto; the documented flow
	// is user-written CRUD RPCs + a migration owning the schema —
	// frontend pages are only emitted for entities whose table exists
	// (codegen.ParseEntityProtos → schemadef.ApplyAndIntrospect).
	runCmd(t, projectDir, forgeBin, "scaffold", "service", "item")
	writeFileE2E(t, filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto"), itemCRUDProto)
	writeFileE2E(t, filepath.Join(projectDir, "db", "migrations", "0001_create_items.up.sql"), itemsTableMigration)
	writeFileE2E(t, filepath.Join(projectDir, "db", "migrations", "0002_items_scalar_vocabulary.up.sql"), itemsVocabularyMigration)

	// Generate the TypeScript stubs the frontend imports. Without this
	// step the frontend build fails with "cannot find module" for every
	// Connect client.
	runCmd(t, projectDir, forgeBin, "generate")

	webDir := filepath.Join(projectDir, "frontends", "web")
	assertPathExistsE2E(t, filepath.Join(webDir, "package.json"))
	// The static detail route must exist — it is the half of this test
	// that guards the build/export-mode interaction.
	assertPathExistsE2E(t, filepath.Join(webDir, "src", "app", "items", "view", "page.tsx"))
	// The build shape lives in next.config — there is no forge.yaml record.
	if !strings.Contains(readFileE2E(t, filepath.Join(webDir, "next.config.ts")), `output: "export"`) {
		t.Fatal("next.config.ts does not build a static export for the scaffolded frontend")
	}

	// npm install — the longest single step. Use --no-audit/--no-fund
	// to reduce noisy output that would otherwise dominate the test
	// log when this test fails. --prefer-offline accelerates repeat
	// runs on developer machines that have a populated npm cache.
	//
	// It resolves @reliantlabs/forge-web-runtime over a file: link into the
	// shared repo checkout and runs its prepare script there; build it once
	// up front so parallel tests do not race that bootstrap.
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, webDir, 5*time.Minute,
		"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")

	// npm run build — the real regression target. If this fails, the
	// output will contain either a Next.js error (template issue) or a
	// missing import (codegen regression).
	runCmdTimeout(t, webDir, 5*time.Minute,
		"npm", "run", "build")

	// The export holds one page per generated route, and every URL the
	// pages link to resolves to its own page — served the way hosted
	// static hosting resolves a path (<path>, then <path>.html).
	outDir := filepath.Join(webDir, "out")
	for _, page := range []string{"items.html", "items/new.html", "items/view.html"} {
		assertPathExistsE2E(t, filepath.Join(outDir, filepath.FromSlash(page)))
	}
	assertStaticLinksResolve(t, outDir, map[string]string{
		"/items":                "items.html",
		"/items/new":            "items/new.html",
		"/items/view?id=item-1": "items/view.html",
	})

	// npm test — the scaffolded vitest suite must be green on a pristine
	// project with one entity.
	runCmdTimeout(t, webDir, 5*time.Minute,
		"npm", "test")

	// Strict type-check as a belt-and-braces guard — catches the cases
	// where Next's build produces a bundle despite type errors.
	runCmdTimeout(t, webDir, 2*time.Minute,
		"npx", "--no-install", "tsc", "--noEmit")
}

// writeFileE2E writes content to path, creating parent directories.
func writeFileE2E(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// itemCRUDProto is the canonical user-written first service: the same
// Get/List/Create shape forge's own example template documents. It is
// what makes `forge generate` emit the dynamic `[id]` detail route and
// the dashboard tile hook — the surfaces this test guards.
//
// The Item message carries the WHOLE proto scalar vocabulary — all
// fifteen kinds, singular and repeated — plus an enum and a repeated
// enum, because `npx tsc --noEmit` below is the only gate that reads the
// emitted TypeScript the way a user's build does, and it costs the same
// whether the entity has four fields or forty.
//
// It had four. That is why a virgin scaffold declaring one `bytes`
// column shipped twelve TypeScript errors before a line of app logic
// existed (mock literals typed `string` into `Uint8Array`, the create
// form typed z.string() into the same), and why a full sweep found
// fifty-seven across `bytes`, `repeated bytes`, `repeated bool`, the
// repeated 64-bit integers, and `repeated enum`. Every one of them was
// reachable from this const.
const itemCRUDProto = `syntax = "proto3";

package services.item.v1;

import "google/protobuf/timestamp.proto";

option go_package = "example.com/feapp/gen/services/item/v1;itemv1";

// ItemService defines the item service RPCs.
service ItemService {
  // GetItem retrieves an item by ID.
  rpc GetItem(GetItemRequest) returns (GetItemResponse) {}

  // ListItems returns a list of items.
  rpc ListItems(ListItemsRequest) returns (ListItemsResponse) {}

  // CreateItem creates a new item.
  rpc CreateItem(CreateItemRequest) returns (CreateItemResponse) {}
}

// ItemTier exercises the enum projections: a singular enum becomes a
// typed <select>, and a REPEATED enum must not (a badge takes one value).
enum ItemTier {
  ITEM_TIER_UNSPECIFIED = 0;
  ITEM_TIER_BASIC = 1;
  ITEM_TIER_PRO = 2;
}

// Item represents an item entity. Every proto scalar kind appears once
// singular and once repeated — the vocabulary is closed at fifteen, so
// this is exhaustive rather than a sample.
message Item {
  string id = 1;
  string name = 2;
  string description = 3;
  google.protobuf.Timestamp created_at = 4;

  bool f_bool = 10;
  bytes f_bytes = 11;
  float f_float = 12;
  double f_double = 13;
  int32 f_int32 = 14;
  sint32 f_sint32 = 15;
  sfixed32 f_sfixed32 = 16;
  int64 f_int64 = 17;
  sint64 f_sint64 = 18;
  sfixed64 f_sfixed64 = 19;
  uint32 f_uint32 = 20;
  fixed32 f_fixed32 = 21;
  uint64 f_uint64 = 22;
  fixed64 f_fixed64 = 23;
  ItemTier f_tier = 24;

  repeated string r_string = 30;
  repeated bool r_bool = 31;
  repeated bytes r_bytes = 32;
  repeated float r_float = 33;
  repeated double r_double = 34;
  repeated int32 r_int32 = 35;
  repeated sint32 r_sint32 = 36;
  repeated sfixed32 r_sfixed32 = 37;
  repeated int64 r_int64 = 38;
  repeated sint64 r_sint64 = 39;
  repeated sfixed64 r_sfixed64 = 40;
  repeated uint32 r_uint32 = 41;
  repeated fixed32 r_fixed32 = 42;
  repeated uint64 r_uint64 = 43;
  repeated fixed64 r_fixed64 = 44;
  repeated ItemTier r_tier = 45;
}

message GetItemRequest {
  string id = 1;
}

message GetItemResponse {
  Item item = 1;
}

message ListItemsRequest {
  int32 page_size = 1;
  string page_token = 2;
}

message ListItemsResponse {
  repeated Item items = 1;
  string next_page_token = 2;
}

// The create FORM is projected from this message, so it carries the same
// vocabulary: a kind that only ever appears on the entity is checked in
// the table cells and the fixtures but never in a zod schema or a
// mutate() payload, which is where four of the five defect classes were.
message CreateItemRequest {
  string name = 1;
  string description = 2;

  bool f_bool = 10;
  bytes f_bytes = 11;
  float f_float = 12;
  double f_double = 13;
  int32 f_int32 = 14;
  sint32 f_sint32 = 15;
  sfixed32 f_sfixed32 = 16;
  int64 f_int64 = 17;
  sint64 f_sint64 = 18;
  sfixed64 f_sfixed64 = 19;
  uint32 f_uint32 = 20;
  fixed32 f_fixed32 = 21;
  uint64 f_uint64 = 22;
  fixed64 f_fixed64 = 23;
  ItemTier f_tier = 24;

  repeated string r_string = 30;
  repeated bool r_bool = 31;
  repeated bytes r_bytes = 32;
  repeated float r_float = 33;
  repeated double r_double = 34;
  repeated int32 r_int32 = 35;
  repeated sint32 r_sint32 = 36;
  repeated sfixed32 r_sfixed32 = 37;
  repeated int64 r_int64 = 38;
  repeated sint64 r_sint64 = 39;
  repeated sfixed64 r_sfixed64 = 40;
  repeated uint32 r_uint32 = 41;
  repeated fixed32 r_fixed32 = 42;
  repeated uint64 r_uint64 = 43;
  repeated fixed64 r_fixed64 = 44;
  repeated ItemTier r_tier = 45;
}

message CreateItemResponse {
  Item item = 1;
}
`

// itemsTableMigration backs the Item entity with a real table —
// migrations own the schema, and frontend CRUD pages are only emitted
// for entities whose table exists in the shadow-applied schema.
//
// Shared with the runtime e2e test, whose Item declares a subset of these
// fields; a column with no wire field is simply not converted.
const itemsTableMigration = `CREATE TABLE items (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

// itemsVocabularyMigration adds one column per proto scalar kind, in the
// exact SQL types entity birth gives them (internal/scaffold's scalarSQL).
// Any other pairing is refused by the conversion gate, so this table IS
// the schema half of the closed vocabulary.
const itemsVocabularyMigration = `ALTER TABLE items
    ADD COLUMN f_bool BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN f_bytes BYTEA NOT NULL DEFAULT '\x',
    ADD COLUMN f_float DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN f_double DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN f_int32 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_sint32 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_sfixed32 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_int64 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_sint64 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_sfixed64 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_uint32 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_fixed32 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_uint64 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_fixed64 BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN f_tier TEXT NOT NULL DEFAULT 'ITEM_TIER_BASIC'
        CHECK (f_tier IN ('ITEM_TIER_BASIC', 'ITEM_TIER_PRO')),
    ADD COLUMN r_string TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_bool BOOLEAN[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_bytes BYTEA[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_float DOUBLE PRECISION[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_double DOUBLE PRECISION[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_int32 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_sint32 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_sfixed32 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_int64 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_sint64 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_sfixed64 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_uint32 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_fixed32 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_uint64 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_fixed64 BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN r_tier TEXT[] NOT NULL DEFAULT '{}';
`

// runCmdTimeout is like runCmd but with an explicit timeout. npm install
// in particular can hang on a flaky network; a timeout gives the test a
// way to fail loudly rather than time out the whole test binary.
func runCmdTimeout(t *testing.T, dir string, timeout time.Duration, name string, args ...string) {
	t.Helper()

	done := make(chan error, 1)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	go func() {
		out, err := cmd.CombinedOutput()
		if err != nil {
			done <- &cmdError{
				name: name, args: args, dir: dir,
				err: err, output: string(out),
			}
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		t.Fatalf("command %q timed out after %s in %s",
			append([]string{name}, args...), timeout, dir)
	}
}

// cmdError is a small helper so runCmdTimeout surfaces the same debug
// information runCmd does when a command fails.
type cmdError struct {
	name   string
	args   []string
	dir    string
	err    error
	output string
}

func (e *cmdError) Error() string {
	return "command " + e.name + " failed in " + e.dir + ": " + e.err.Error() + "\n" + e.output
}
