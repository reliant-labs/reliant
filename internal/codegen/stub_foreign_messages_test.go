package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// crossPackageService is control-plane's daemon_admin shape: the service's
// file imports controlplane/v1/controller.proto and its rpcs take and
// return messages declared there, which protoc-gen-go puts in that file's
// package, not the service's.
func crossPackageService() ServiceDef {
	const own = "proto/services/daemon_admin/v1/daemon_admin.proto"
	const foreign = "proto/controlplane/v1/controller.proto"
	return ServiceDef{
		Name:       "DaemonAdminService",
		Package:    "controlplane.v1",
		GoPackage:  "github.com/test/cp/gen/services/daemon_admin/v1",
		PkgName:    "daemon_adminv1",
		ProtoFile:  own,
		ModulePath: "github.com/test/cp",
		Methods: []Method{
			{Name: "GetDaemonRollout", InputType: "GetDaemonRolloutRequest", OutputType: "GetDaemonRolloutResponse", InputProtoFile: foreign, OutputProtoFile: foreign},
			{Name: "Mixed", InputType: "MixedRequest", OutputType: "GetDaemonRolloutResponse", InputProtoFile: own, OutputProtoFile: foreign},
			{Name: "WatchRollout", InputType: "GetDaemonRolloutRequest", OutputType: "RolloutEvent", InputProtoFile: foreign, OutputProtoFile: own, ServerStreaming: true},
		},
	}
}

// importsAndUses returns a Go file's import aliases (sorted) and reports any
// that the file never references: an unused import does not compile, and a
// referenced alias with no import is `undefined`.
func importsAndUses(t *testing.T, path string) (aliases []string, unused, undefined []string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	imported := map[string]bool{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imported[name] = true
	}
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	for name := range imported {
		aliases = append(aliases, name)
		if !used[name] {
			unused = append(unused, name)
		}
	}
	for _, name := range []string{"pb", "controlplanev1"} {
		if used[name] && !imported[name] {
			undefined = append(undefined, name)
		}
	}
	sort.Strings(aliases)
	return aliases, unused, undefined
}

// A stub for an rpc whose messages live in an imported proto file names them
// from that file's generated package. Before, every stub qualified them with
// the service's own `pb`, so it did not compile and generate rolled back.
func TestGenerateMissingHandlerStubs_MessagesFromAnImportedProtoFile(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "connect-v1", true: "connect-v2"}[v2], func(t *testing.T) {
			project := t.TempDir()
			if v2 {
				writeConnectV2GoMod(t, project)
			}
			dir := filepath.Join(project, "internal", "handlers", "daemon_admin")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte("package daemon_admin\n\ntype Service struct{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// The append arm: the rpc's file exists without the method.
			if err := os.WriteFile(filepath.Join(dir, RPCHandlerFileName("Mixed")), []byte("package daemon_admin\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := GenerateMissingHandlerStubs(crossPackageService(), project, dir, nil, nil); err != nil {
				t.Fatalf("GenerateMissingHandlerStubs: %v", err)
			}

			for _, tc := range []struct {
				file, wantType string
				want, notWant  []string
			}{
				{RPCHandlerFileName("GetDaemonRollout"), "controlplanev1.GetDaemonRolloutRequest", []string{"controlplanev1"}, []string{"pb"}},
				{RPCHandlerFileName("Mixed"), "pb.MixedRequest", []string{"controlplanev1", "pb"}, nil},
				{RPCHandlerFileName("WatchRollout"), "controlplanev1.GetDaemonRolloutRequest", []string{"controlplanev1"}, nil},
				{ScaffoldTestFileName("GetDaemonRollout"), "controlplanev1.GetDaemonRolloutRequest", []string{"controlplanev1"}, []string{"pb"}},
				{ScaffoldTestFileName("WatchRollout"), "controlplanev1.GetDaemonRolloutRequest", []string{"controlplanev1"}, []string{"pb"}},
			} {
				path := filepath.Join(dir, tc.file)
				body := readFile(t, path)
				if !strings.Contains(body, tc.wantType) {
					t.Errorf("%s does not name %s:\n%s", tc.file, tc.wantType, body)
				}
				if !strings.Contains(body, `controlplanev1 "github.com/test/cp/gen/controlplane/v1"`) {
					t.Errorf("%s does not import the messages' package:\n%s", tc.file, body)
				}
				aliases, unused, undefined := importsAndUses(t, path)
				if len(unused) > 0 || len(undefined) > 0 {
					t.Errorf("%s: unused imports %v, undefined qualifiers %v:\n%s", tc.file, unused, undefined, body)
				}
				have := map[string]bool{}
				for _, a := range aliases {
					have[a] = true
				}
				for _, a := range tc.want {
					if !have[a] {
						t.Errorf("%s does not import %s:\n%s", tc.file, a, body)
					}
				}
				for _, a := range tc.notWant {
					if have[a] {
						t.Errorf("%s imports %s, which names none of its messages:\n%s", tc.file, a, body)
					}
				}
			}
		})
	}
}
