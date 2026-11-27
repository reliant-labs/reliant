package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

func covBWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func covBEchoDescriptor() (map[string]codegen.ServiceDef, *ForgeDescriptor) {
	svc := codegen.ServiceDef{
		Name:      "EchoService",
		ProtoFile: "proto/services/echo/v1/echo.proto",
		Methods:   []codegen.Method{{Name: "Say"}, {Name: "Shout"}},
	}
	desc := &ForgeDescriptor{
		Services: []codegen.ServiceDef{svc},
		Configs:  []codegen.ConfigMessage{{Name: "AppConfig", Fields: []codegen.ConfigField{{}, {}}}},
	}
	return map[string]codegen.ServiceDef{"EchoService": svc, "echo": svc}, desc
}

func TestClassifyGeneratedFile(t *testing.T) {
	byName, desc := covBEchoDescriptor()
	const proto = "proto/services/echo/v1/echo.proto"

	root := t.TempDir()
	// A package whose contract is derived from a //forge:contract type AND
	// that still carries a hand-written contract.go.
	covBWrite(t, root, "internal/billing/service.go", "package billing\n\n//forge:contract\ntype Service struct{}\n")
	covBWrite(t, root, "internal/billing/contract.go", "package billing\n")

	tests := []struct {
		name        string
		rel         string
		desc        *ForgeDescriptor
		byName      map[string]codegen.ServiceDef
		wantKind    string
		wantSources []string
		wantNotes   []string
	}{
		{
			name: "handler gen resolves its service and lists rpcs", rel: "internal/handlers/echo/handlers_crud_ops_gen.go",
			desc: desc, byName: byName, wantKind: "service-handler", wantSources: []string{proto},
			wantNotes: []string{"2 RPC(s) declared (Say, Shout)"},
		},
		{
			name: "handler gen for an unknown service has no sources", rel: "internal/handlers/ghost/x_gen.go",
			desc: desc, byName: byName, wantKind: "service-handler",
		},
		{
			name: "handler mock", rel: "internal/handlers/mocks/echo_mock_gen.go",
			desc: desc, byName: byName, wantKind: "service-mock", wantSources: []string{proto},
		},
		{
			name: "handler mock for an unknown service", rel: "internal/handlers/mocks/ghost_mock_gen.go",
			desc: desc, byName: byName, wantKind: "service-mock",
		},
		{
			name: "connect stub", rel: "gen/services/echo/v1/echo.pb.go",
			desc: desc, byName: byName, wantKind: "service-stub", wantSources: []string{proto},
		},
		{
			name: "orm entity", rel: "internal/db/user_orm_gen.go",
			desc: desc, byName: byName, wantKind: "entity-orm", wantSources: []string{"db/migrations/"},
			wantNotes: []string{"entity ORM projected from the applied schema (shadow-introspected db/migrations)"},
		},
		{
			name: "contract with marked type and contract.go", rel: "internal/billing/contract_gen.go",
			desc: desc, byName: byName, wantKind: "contract",
			wantSources: []string{"internal/billing/service.go", "internal/billing/contract.go"},
			wantNotes:   []string{"contract derived from the //forge:contract type Service"},
		},
		{
			name: "contract mock with no source on disk", rel: "internal/nothere/mock_gen.go",
			desc: desc, byName: byName, wantKind: "contract",
		},
		{
			name: "config names each config message", rel: "pkg/config/config_gen.go",
			desc: desc, byName: byName, wantKind: "config", wantNotes: []string{"config message AppConfig (2 field(s))"},
		},
		{
			name: "config without a descriptor", rel: "pkg/config/config_gen.go",
			byName: byName, wantKind: "config",
		},
		{
			name: "frontend hook matches its service case-insensitively", rel: "frontends/web/src/hooks/echo-hooks.ts",
			desc: desc, byName: byName, wantKind: "frontend", wantSources: []string{proto},
		},
		{
			name: "frontend file matching no service", rel: "frontends/web/src/app/page.tsx",
			desc: desc, byName: byName, wantKind: "frontend",
		},
		{
			name: "bootstrap", rel: "pkg/app/bootstrap.go",
			desc: desc, byName: byName, wantKind: "bootstrap", wantNotes: []string{"wires every service + every package contract"},
		},
		{name: "anything else", rel: "README.md", desc: desc, byName: byName, wantKind: "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, sources, notes := classifyGeneratedFile(tc.rel, root, tc.byName, tc.desc)
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if !reflect.DeepEqual(sources, tc.wantSources) {
				t.Errorf("sources = %v, want %v", sources, tc.wantSources)
			}
			if !reflect.DeepEqual(notes, tc.wantNotes) {
				t.Errorf("notes = %v, want %v", notes, tc.wantNotes)
			}
		})
	}
}

// printExplainLog labels each certified file as rewritten or skipped by
// diffing the pre-generate body hash against the post-generate one, and
// groups the log by kind.
func TestPrintExplainLogLabelsRewrittenAndSkipped(t *testing.T) {
	root := t.TempDir()
	stamp := func(rel, body string) {
		t.Helper()
		stamped, ok := checksums.Stamp(rel, []byte(body))
		if !ok {
			t.Fatalf("%s is not stampable", rel)
		}
		covBWrite(t, root, rel, string(stamped))
	}
	stamp("pkg/app/bootstrap.go", "package app\n")
	stamp("internal/db/user_orm_gen.go", "package db\n")

	markers := checksums.ScanMarkers(root)
	if len(markers) != 2 {
		t.Fatalf("fixture: want 2 certified files, got %v", markers)
	}
	// bootstrap.go was unchanged by the run; the ORM file had no entry before it.
	pre := map[string]string{"pkg/app/bootstrap.go": markers["pkg/app/bootstrap.go"].Body}

	out := captureStdout(t, func() {
		if err := printExplainLog(root, pre); err != nil {
			t.Errorf("printExplainLog: %v", err)
		}
	})
	for _, want := range []string{
		"Generation provenance",
		"── bootstrap ──",
		"── entity-orm ──",
		"no input change since last gen (idempotent skip)",
		"reason: rewritten this run",
		"← from db/migrations/",
		"(2 output(s) tracked, generated at ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain log missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "── bootstrap ──") > strings.Index(out, "── entity-orm ──") {
		t.Errorf("kinds must print sorted (bootstrap before entity-orm):\n%s", out)
	}
	if strings.Count(out, "idempotent skip") != 1 {
		t.Errorf("only the unchanged file is skipped:\n%s", out)
	}
}

func TestPrintExplainLogWithNothingTracked(t *testing.T) {
	out := captureStdout(t, func() {
		if err := printExplainLog(t.TempDir(), nil); err != nil {
			t.Errorf("printExplainLog: %v", err)
		}
	})
	if !strings.Contains(out, "no tracked outputs — nothing to explain") {
		t.Errorf("an empty project must say there is nothing to explain:\n%s", out)
	}
}

// A descriptor on disk feeds the handler sources; the file is classified
// from the proto-declared service.
func TestPrintExplainLogUsesTheForgeDescriptor(t *testing.T) {
	root := t.TempDir()
	covBWrite(t, root, "gen/forge_descriptor/services/echo/v1/echo.json",
		`{"services":[{"Name":"EchoService","ProtoFile":"proto/services/echo/v1/echo.proto","Methods":[{"Name":"Say"}]}]}`)
	stamped, _ := checksums.Stamp("internal/handlers/echo/handlers_gen.go", []byte("package echo\n"))
	covBWrite(t, root, "internal/handlers/echo/handlers_gen.go", string(stamped))

	out := captureStdout(t, func() {
		if err := printExplainLog(root, nil); err != nil {
			t.Errorf("printExplainLog: %v", err)
		}
	})
	for _, want := range []string{"── service-handler ──", "← from proto/services/echo/v1/echo.proto", "1 RPC(s) declared (Say)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
