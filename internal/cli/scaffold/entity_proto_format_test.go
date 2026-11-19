package scaffold

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/prototext"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/protofmt"
	"github.com/reliant-labs/forge/pkg/auth"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// A project gates its protos on `buf format -d --exit-code` (the scaffolded
// pre-commit hook). The quintet completion edits the service proto, so what it
// leaves there must be buf's output: an evaluator's first commit after
// `forge scaffold` was refused over an import forge put out of order, the
// one-field `(forge.v1.method)` literals forge spread over three lines, and
// the entity's validate rules forge folded onto one line in the Create
// request.

// The shapes forge authors itself are buf's, with no buf needed to see it.
func TestEnsureProtoImportSortsIntoPlace(t *testing.T) {
	for _, tc := range []struct{ name, in, path, want string }{
		{
			name: "between existing imports",
			in:   "syntax = \"proto3\";\n\npackage a.v1;\n\nimport \"buf/validate/validate.proto\";\nimport \"google/protobuf/timestamp.proto\";\n",
			path: "google/protobuf/field_mask.proto",
			want: "syntax = \"proto3\";\n\npackage a.v1;\n\nimport \"buf/validate/validate.proto\";\nimport \"google/protobuf/field_mask.proto\";\nimport \"google/protobuf/timestamp.proto\";\n",
		},
		{
			name: "after the last import",
			in:   "package a.v1;\n\nimport \"forge/v1/forge.proto\";\n\noption go_package = \"x\";\n",
			path: "google/protobuf/timestamp.proto",
			want: "package a.v1;\n\nimport \"forge/v1/forge.proto\";\nimport \"google/protobuf/timestamp.proto\";\n\noption go_package = \"x\";\n",
		},
		{
			name: "first import, set off from the package line",
			in:   "package a.v1;\noption go_package = \"x\";\n",
			path: "forge/v1/forge.proto",
			want: "package a.v1;\n\nimport \"forge/v1/forge.proto\";\n\noption go_package = \"x\";\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ensureProtoImport(tc.in, tc.path); got != tc.want {
				t.Errorf("ensureProtoImport:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestCRUDRPCOneFieldOptionLiteralIsOneLine(t *testing.T) {
	for _, p := range buildEntityCRUDRPCPieces("Counter") {
		if strings.Contains(p.text, "{\n      auth_required: true\n    };") {
			t.Errorf("%s: a one-field option literal spans three lines; buf format writes {auth_required: true}:\n%s", p.name, p.text)
		}
	}
}

// The option literals changed shape for buf format; what they declare must
// not. Each of the five rpcs' literal is parsed as the text-format
// forge.v1.MethodOptions it is (what buf compiles it into), must set
// auth_required explicitly to true — in the one-line shape (Get, List) and
// the multi-line one (Create, Update, Delete) alike — and, projected the way
// generate projects it, must leave every rpc out of the open-procedure set,
// so an unauthenticated caller is refused. Dropping auth_required, emitting
// false or mangling either shape fails here.
func TestCRUDRPCOptionsStayAuthRequired(t *testing.T) {
	const svcPkg, svcName = "services.counters.v1", "CountersService"
	literal := regexp.MustCompile(`(?s)option \(forge\.v1\.method\) = \{(.*?)\};`)
	wantIdem := map[string]bool{"CreateCounter": true, "GetCounter": false, "UpdateCounter": true, "DeleteCounter": true, "ListCounters": false}

	pieces := buildEntityCRUDRPCPieces("Counter")
	if len(pieces) != len(wantIdem) {
		t.Fatalf("got %d CRUD rpcs, want %d", len(pieces), len(wantIdem))
	}
	svc := codegen.ServiceDef{Name: svcName, Package: svcPkg, GoPackage: "example.com/counters/gen/services/counters/v1", PkgName: "countersv1"}
	for _, p := range pieces {
		want, known := wantIdem[p.name]
		if !known {
			t.Fatalf("unexpected CRUD rpc %s", p.name)
		}
		m := literal.FindStringSubmatch(p.text)
		if m == nil {
			t.Fatalf("%s: no (forge.v1.method) option:\n%s", p.name, p.text)
		}
		var mo forgev1.MethodOptions
		if err := prototext.Unmarshal([]byte(m[1]), &mo); err != nil {
			t.Fatalf("%s: option literal is not a forge.v1.MethodOptions (%v):\n%s", p.name, err, p.text)
		}
		if mo.AuthRequired == nil || !*mo.AuthRequired {
			t.Errorf("%s: auth_required is not explicitly true:\n%s", p.name, p.text)
		}
		if mo.GetIdempotencyKey() != want {
			t.Errorf("%s: idempotency_key = %v, want %v:\n%s", p.name, mo.GetIdempotencyKey(), want, p.text)
		}
		// Mirrors the descriptor projection: fail-closed by default, and
		// internal/cli applyMethodOptions lets an explicit value win.
		method := codegen.Method{Name: p.name, AuthRequired: true}
		if mo.AuthRequired != nil {
			method.AuthRequired = *mo.AuthRequired
		}
		svc.Methods = append(svc.Methods, method)
	}

	var skip []string
	for _, e := range codegen.BuildOpenProcedures([]codegen.ServiceDef{svc}, "example.com/counters").Open {
		skip = append(skip, "/"+e.Service+"/"+e.Method)
	}
	if len(skip) != 0 {
		t.Fatalf("CRUD rpcs projected into the open-procedure set: %v", skip)
	}
	v, err := auth.NewValidator(auth.Config{Provider: auth.ProviderJWT, JWT: auth.JWTConfig{SigningMethod: "HS256", Secret: "test-secret"}, SkipMethods: skip})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range svc.Methods {
		proc := "/" + svcPkg + "." + svcName + "/" + m.Name
		if v.IsUnauthenticatedProcedure(proc, skip) {
			t.Errorf("%s bypasses authentication", proc)
		}
		_, err := v.AuthenticateHeaders(context.Background(), http.Header{}, auth.InterceptorOptions{SkipMethods: skip})
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: a credential-less caller got %v, want Unauthenticated", proc, err)
		}
	}
}

// The whole edit, judged by buf: quintet completion over an entity whose
// rules are a multi-option list and a braced aggregate — text forge copies
// rather than authors — settles to buf's output, and a session leaves a proto
// the author had not formatted exactly as the author left it.
func TestQuintetCompletionSettlesToBufFormat(t *testing.T) {
	testkit.Slow(t, "execs buf")
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf not on PATH")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "buf.yaml"), []byte("version: v2\nmodules:\n  - path: proto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "proto", "services", "counters", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	servicePath := filepath.Join(dir, "counters.proto")
	if err := os.WriteFile(servicePath, []byte(`syntax = "proto3";

package services.counters.v1;

import "buf/validate/validate.proto";
import "forge/v1/forge.proto";
import "google/protobuf/timestamp.proto";

option go_package = "example.com/counters/gen/services/counters/v1;countersv1";

service CountersService {}

// forge:entity
message Counter {
  string id = 1;
  string name = 2 [
    (buf.validate.field).string.min_len = 1,
    (buf.validate.field).string.max_len = 64
  ];
  int64 value = 3 [(buf.validate.field).int64 = {
    gte: 0
    lte: 1000
  }];
  google.protobuf.Timestamp created_at = 4;
  google.protobuf.Timestamp updated_at = 5;
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Another proto, edited by the "author" and not buf-format clean: the
	// session must not take it over.
	authorPath := filepath.Join(dir, "author.proto")
	authorText := "syntax = \"proto3\";\npackage services.counters.v1;\nmessage   Author {string id = 1;}\n"
	if err := os.WriteFile(authorPath, []byte(authorText), 0o644); err != nil {
		t.Fatal(err)
	}
	bufFormatClean(t, root, "proto/services/counters/v1/counters.proto")

	end := protofmt.Begin(root, nil)
	scan, err := codegen.ScanRawProtoDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := scan.MessageByName("Counter")
	if !ok {
		t.Fatal("raw scan did not find Counter")
	}
	fields, _ := entityFieldsFromSchemaDefs("services.counters.v1", m.Fields)
	if _, err := completeEntityCRUDProto(root, servicePath, m.File, "Counter", fields, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorPath, []byte(authorText+"message   Other {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := protofmt.Settle(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	end()

	bufFormatClean(t, root, "proto/services/counters/v1/counters.proto")
	got, _ := os.ReadFile(servicePath)
	if !strings.Contains(string(got), "message CreateCounterRequest") {
		t.Fatalf("the quintet was not written:\n%s", got)
	}
	if b, _ := os.ReadFile(authorPath); string(b) != authorText+"message   Other {}\n" {
		t.Errorf("a proto that was not buf-format clean before the session was reformatted:\n%s", b)
	}
}

func bufFormatClean(t *testing.T, root, rel string) {
	t.Helper()
	cmd := exec.Command("buf", "format", "-d", "--exit-code", "--path", rel)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s is not buf format clean (%v):\n%s", rel, err, out)
	}
}
