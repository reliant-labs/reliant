package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestHandlerPathsFromServices_TableDriven exercises the pure
// service→path projection. Table-driven so each case documents one
// expected behaviour (sorting, package+method composition, skip-empty,
// stable order across methods).
func TestHandlerPathsFromServices_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		in   []codegen.ServiceDef
		want []string // expected Path values in expected order
	}{
		{
			name: "empty input → empty output",
			in:   nil,
			want: nil,
		},
		{
			name: "single service single method",
			in: []codegen.ServiceDef{
				{
					Package: "services.admin_server.v1",
					Name:    "AdminServerService",
					Methods: []codegen.Method{{Name: "Create"}},
				},
			},
			want: []string{"/services.admin_server.v1.AdminServerService/Create"},
		},
		{
			name: "methods sorted within service",
			in: []codegen.ServiceDef{
				{
					Package: "services.admin_server.v1",
					Name:    "AdminServerService",
					Methods: []codegen.Method{
						{Name: "Update"},
						{Name: "Create"},
						{Name: "Get"},
					},
				},
			},
			want: []string{
				"/services.admin_server.v1.AdminServerService/Create",
				"/services.admin_server.v1.AdminServerService/Get",
				"/services.admin_server.v1.AdminServerService/Update",
			},
		},
		{
			name: "services sorted across packages",
			in: []codegen.ServiceDef{
				{
					Package: "controlplane.v1",
					Name:    "UserService",
					Methods: []codegen.Method{{Name: "GetUser"}},
				},
				{
					Package: "services.admin_server.v1",
					Name:    "AdminServerService",
					Methods: []codegen.Method{{Name: "Create"}},
				},
			},
			want: []string{
				"/controlplane.v1.UserService/GetUser",
				"/services.admin_server.v1.AdminServerService/Create",
			},
		},
		{
			name: "skips entries missing package, name, or method",
			in: []codegen.ServiceDef{
				{Package: "", Name: "Headless", Methods: []codegen.Method{{Name: "X"}}},
				{Package: "p.v1", Name: "", Methods: []codegen.Method{{Name: "X"}}},
				{
					Package: "p.v1", Name: "Svc",
					Methods: []codegen.Method{{Name: ""}, {Name: "Real"}},
				},
			},
			want: []string{"/p.v1.Svc/Real"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := handlerPathsFromServices(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d paths, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].Path != want {
					t.Errorf("path[%d]: got %q, want %q", i, got[i].Path, want)
				}
			}
		})
	}
}

// TestWriteHandlerPaths_TextFormat asserts the text format emits one
// path per line, in order, with no extra framing. Downstream callers
// pipe this into grep / diff so framing changes are breaking.
func TestWriteHandlerPaths_TextFormat(t *testing.T) {
	paths := []HandlerPath{
		{Service: "p.v1.A", Method: "M1", Path: "/p.v1.A/M1"},
		{Service: "p.v1.A", Method: "M2", Path: "/p.v1.A/M2"},
	}
	var buf bytes.Buffer
	if err := writeHandlerPaths(&buf, paths, "text"); err != nil {
		t.Fatalf("writeHandlerPaths: %v", err)
	}
	want := "/p.v1.A/M1\n/p.v1.A/M2\n"
	if buf.String() != want {
		t.Errorf("text output\n got: %q\nwant: %q", buf.String(), want)
	}
}

// TestWriteHandlerPaths_JSONFormat asserts the JSON format
// round-trips into a typed slice and emits an empty list (never
// null) for empty input so consumers can `.[]` unconditionally.
func TestWriteHandlerPaths_JSONFormat(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		paths := []HandlerPath{
			{Service: "p.v1.A", Method: "M1", Path: "/p.v1.A/M1"},
		}
		var buf bytes.Buffer
		if err := writeHandlerPaths(&buf, paths, "json"); err != nil {
			t.Fatalf("writeHandlerPaths: %v", err)
		}
		var decoded []HandlerPath
		if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
		}
		if len(decoded) != 1 || decoded[0].Path != "/p.v1.A/M1" {
			t.Errorf("decoded round-trip mismatch: %+v", decoded)
		}
	})
	t.Run("empty emits [] not null", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeHandlerPaths(&buf, nil, "json"); err != nil {
			t.Fatalf("writeHandlerPaths: %v", err)
		}
		got := strings.TrimSpace(buf.String())
		if got != "[]" {
			t.Errorf("empty json: got %q, want %q", got, "[]")
		}
	})
}

// TestRunIntrospectHandlers_FromDescriptor wires the full command
// body against a real on-disk forge_descriptor.json. This is the only
// I/O-bound test in the file — the other tests stay pure.
//
// We use t.Chdir() (Go 1.24+) so findProjectConfigFile picks up our
// fixture forge.yaml without leaking cwd state to sibling tests.
func TestRunIntrospectHandlers_FromDescriptor(t *testing.T) {
	dir := t.TempDir()

	// Minimal forge.yaml so findProjectConfigFile() roots at dir.
	yaml := `name: introspect-test
module_path: github.com/test/introspect-test
version: 0.0.1
forge_version: dev
`
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	markServiceProject(t, dir)

	// Synthetic descriptor with two services so we can assert both
	// the cross-service sort and the within-service method sort.
	desc := codegen.ForgeDescriptor{
		Services: []codegen.ServiceDef{
			{
				Package: "services.admin_server.v1",
				Name:    "AdminServerService",
				Methods: []codegen.Method{{Name: "Get"}, {Name: "Create"}},
			},
			{
				Package: "controlplane.v1",
				Name:    "UserService",
				Methods: []codegen.Method{{Name: "GetUser"}},
			},
		},
	}
	descBytes, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatalf("mkdir gen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"), descBytes, 0o644); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	t.Chdir(dir)

	var buf bytes.Buffer
	if err := runIntrospectHandlers(&buf, "proto/services", "text"); err != nil {
		t.Fatalf("runIntrospectHandlers: %v", err)
	}
	want := strings.Join([]string{
		"/controlplane.v1.UserService/GetUser",
		"/services.admin_server.v1.AdminServerService/Create",
		"/services.admin_server.v1.AdminServerService/Get",
		"",
	}, "\n")
	if buf.String() != want {
		t.Errorf("text output\n got: %q\nwant: %q", buf.String(), want)
	}

	// Sanity-check the JSON path too — same descriptor, just a
	// different formatter on the way out.
	buf.Reset()
	if err := runIntrospectHandlers(&buf, "proto/services", "json"); err != nil {
		t.Fatalf("runIntrospectHandlers json: %v", err)
	}
	var decoded []HandlerPath
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal json output: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("got %d paths, want 3: %+v", len(decoded), decoded)
	}
}

// TestRunIntrospectHandlers_UnknownFormat asserts the validator
// rejects bogus --format values up front rather than silently
// emitting text.
func TestRunIntrospectHandlers_UnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	err := runIntrospectHandlers(&buf, "proto/services", "yaml")
	if err == nil {
		t.Fatal("expected error for unknown format, got nil")
	}
	if !strings.Contains(err.Error(), "unknown --format") {
		t.Errorf("error message: got %q, want substring %q", err.Error(), "unknown --format")
	}
}
