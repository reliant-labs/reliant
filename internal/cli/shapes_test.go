package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `forge project shapes` replaces the grep-by-symbol loop that was the single
// largest recoverable cost in a measured fan-out (28.4 min / 123 calls). Two
// properties make it worth using, and both are pinned here: it finds symbols
// across LAYERS in one call, and every hit carries file:line so the next step
// is a targeted read rather than another search.
func TestCollectShapes_FindsSymbolsAcrossLayers(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("proto/services/shop/v1/shop.proto", `syntax = "proto3";
service ShopService {
  rpc ListWidgets(ListWidgetsRequest) returns (ListWidgetsResponse);
}
message Widget {
  string id = 1;
}
enum WidgetKind {
  WIDGET_KIND_UNSPECIFIED = 0;
}
`)
	mk("db/migrations/00001_create_widgets.up.sql", "CREATE TABLE widgets (\n  id TEXT PRIMARY KEY\n);\n")
	mk("internal/handlers/shop/rpc_list_widgets.go", `package shop

// forge:gen unwired-stub symbol=shop.ListWidgets
func (s *Service) ListWidgets(ctx context.Context) error { return nil }
`)
	mk("frontends/app/src/hooks/shop-service-hooks_gen.ts", "export const useListWidgets = () => {};\n")

	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	got := collectShapes(dir)
	index := map[string]shape{}
	for _, s := range got {
		index[s.Kind+":"+s.Name] = s
	}

	for _, want := range []string{
		"rpc:ShopService.ListWidgets",
		"message:Widget",
		"enum:WidgetKind",
		"table:widgets",
		"handler:ShopService.ListWidgets",
		"hook:useListWidgets",
	} {
		s, ok := index[want]
		if !ok {
			t.Errorf("missing %s — a layer this command does not see is a layer agents keep grepping", want)
			continue
		}
		if s.Line <= 0 || s.File == "" {
			t.Errorf("%s has no file:line; without it the next step is another search, which is the cost being removed", want)
		}
	}

	if d := index["handler:ShopService.ListWidgets"].Detail; !strings.Contains(d, "unwired-stub") {
		t.Errorf("an unimplemented handler must be flagged so 'declared vs implemented' is answerable here; got %q", d)
	}
	if d := index["rpc:ShopService.ListWidgets"].Detail; !strings.Contains(d, "ListWidgetsRequest") {
		t.Errorf("an rpc must carry its request/response types, or the agent reads the proto anyway; got %q", d)
	}
}

// The command must not answer from gen/forge_descriptor/: that file is a
// cache which can be stale, and recon output that is confidently wrong is
// worse than none. A planted lie in the cache must not appear in the output.
func TestCollectShapes_IgnoresTheDescriptorCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "proto", "svc", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proto", "svc", "v1", "a.proto"),
		[]byte("service S {\n  rpc RealRpc(A) returns (B);\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen", "forge_descriptor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor", "fixture.json"),
		[]byte(`{"services":[{"Name":"S","Methods":[{"Name":"GhostRpc"}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, s := range collectShapes(dir) {
		names = append(names, s.Name)
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, "GhostRpc") {
		t.Error("shapes reported an RPC that exists only in the cache — it must read the protos")
	}
	if !strings.Contains(joined, "RealRpc") {
		t.Errorf("shapes missed an RPC declared in a .proto; got %v", names)
	}
}

// An rpc of a `// forge:unmanaged` service is declared but deliberately has no
// forge handler. `--kind rpc,handler` answers "declared vs implemented", so
// the rpc says so — otherwise every unmanaged rpc reads as a missing handler.
// The marker binds only to the service it precedes, per file.
func TestCollectShapes_TagsUnmanagedServiceRPCs(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proto/services/legacy/v1/legacy.proto", "syntax = \"proto3\";\n\n// forge:unmanaged\n\n// Legacy docs.\nservice LegacyService {\n  rpc Ping(A) returns (B);\n}\n")
	write("proto/services/shop/v1/shop.proto", "syntax = \"proto3\";\nservice ShopService {\n  rpc Buy(A) returns (B);\n}\n")

	details := map[string]string{}
	for _, s := range collectShapes(dir) {
		if s.Kind == "rpc" {
			details[s.Name] = s.Detail
		}
	}
	if d := details["LegacyService.Ping"]; !strings.HasSuffix(d, " unmanaged") {
		t.Errorf("unmanaged service's rpc not tagged: %q", d)
	}
	if d := details["ShopService.Buy"]; strings.Contains(d, "unmanaged") {
		t.Errorf("managed service's rpc tagged unmanaged: %q", d)
	}
}

// TestScanMigrations_SchemaQualifiedTables pins F19: pg_dump writes every
// table schema-qualified (`CREATE TABLE public.tasks`), and the scanner read
// that as a table named `public` — every table, in a dump-style baseline.
// The table is the last part of the name, quoted or not; the schema rides
// along as detail.
func TestScanMigrations_SchemaQualifiedTables(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "db", "migrations", "00001_baseline.up.sql")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	sql := `CREATE TABLE public.tasks (
    id text NOT NULL
);
CREATE TABLE "public"."orders" (id text);
CREATE TABLE public."Users" (id text);
CREATE TABLE IF NOT EXISTS app.items (id text);
CREATE TABLE plain (id text);
`
	if err := os.WriteFile(p, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range scanMigrations(dir) {
		got[s.Name] = s.Detail
	}
	want := map[string]string{
		"tasks":  "schema=public",
		"orders": "schema=public",
		"Users":  "schema=public",
		"items":  "schema=app",
		"plain":  "",
	}
	for name, detail := range want {
		if d, ok := got[name]; !ok || d != detail {
			t.Errorf("table %q: detail %q (found %v), want %q; all: %v", name, d, ok, detail, got)
		}
	}
	if _, ok := got["public"]; ok {
		t.Errorf("a schema was read as a table name: %v", got)
	}
}
