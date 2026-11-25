package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"

	"github.com/reliant-labs/forge/internal/codegen"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// writeProjectFile writes rel under root, creating its directories.
func writeProjectFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// In a project nobody signs in to (`forge project new --auth-mode none`: a
// frontend, no deploy/kcl/identity.k, no dev IdP), nothing can issue the
// credential a fail-closed rpc requires, so the born CRUD rpcs are public —
// written out as auth_required: false, so the author reads the decision — and
// the completion says so. The same project with an identity keeps them
// fail-closed.
func TestCompleteEntityCRUDProto_NoSignInBirthsPublicRPCs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity bool
		unmarked bool
		want     string
	}{
		{name: "no sign-in", identity: false, want: "auth_required: false"},
		{name: "identity declared", identity: true, want: "auth_required: true"},
		{name: "external issuer", unmarked: true, want: "auth_required: true"},
		{name: "custom validator", unmarked: true, want: "auth_required: true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := scaffoldEntityProject(t)
			writeProjectFile(t, root, "frontends/web/next.config.ts", "export default {};\n")
			dev := "frontends = []\n"
			if !tc.unmarked {
				dev = codegen.AuthNoneDeclaration + "\n" + dev
			}
			writeProjectFile(t, root, "deploy/kcl/dev/main.k", dev)
			if tc.identity {
				writeProjectFile(t, root, codegen.IdentityKCLRelPath, "IDENTITY = 1\n")
			}
			protoPath := filepath.Join(root, "proto", "services", "item", "v1", "item.proto")
			scan, err := codegen.ScanRawProtoDir(filepath.Dir(protoPath))
			if err != nil {
				t.Fatal(err)
			}
			m, ok := scan.MessageByName("Bookmark")
			if !ok {
				t.Fatal("raw scan did not find Bookmark")
			}
			fields, _ := entityFieldsFromSchemaDefs("services.item.v1", m.Fields)
			res, err := completeEntityCRUDProto(root, protoPath, m.File, "Bookmark", fields, false)
			if err != nil {
				t.Fatal(err)
			}
			got := readFileT(t, protoPath)
			if n := strings.Count(got, tc.want); n != 5 {
				t.Errorf("%d of the five CRUD rpcs declare %q, want all five:\n%s", n, tc.want, got)
			}
			wantPublic := !tc.identity && !tc.unmarked
			if res.Public != wantPublic {
				t.Errorf("result Public = %v, want %v", res.Public, wantPublic)
			}
			if note := res.publicNote(); (note != "") != wantPublic {
				t.Errorf("publicNote = %q with identity = %v", note, tc.identity)
			}
		})
	}
}

// Public-born rpcs, projected the way generate projects them, land in the
// open-procedure set: every one answers a credential-less caller.
func TestCRUDRPCOptionsPublicWhenNotAuthRequired(t *testing.T) {
	literal := regexp.MustCompile(`(?s)option \(forge\.v1\.method\) = \{(.*?)\};`)
	svc := codegen.ServiceDef{Name: "CountersService", Package: "services.counters.v1", GoPackage: "example.com/counters/gen/services/counters/v1", PkgName: "countersv1"}
	for _, p := range buildEntityCRUDRPCPieces("Counter", false) {
		m := literal.FindStringSubmatch(p.text)
		if m == nil {
			t.Fatalf("%s: no (forge.v1.method) option:\n%s", p.name, p.text)
		}
		var mo forgev1.MethodOptions
		if err := prototext.Unmarshal([]byte(m[1]), &mo); err != nil {
			t.Fatalf("%s: option literal is not a forge.v1.MethodOptions (%v):\n%s", p.name, err, p.text)
		}
		if mo.AuthRequired == nil || *mo.AuthRequired {
			t.Errorf("%s: auth_required is not explicitly false:\n%s", p.name, p.text)
		}
		svc.Methods = append(svc.Methods, codegen.Method{Name: p.name, AuthRequired: *mo.AuthRequired})
	}
	if open := codegen.BuildOpenProcedures([]codegen.ServiceDef{svc}, "example.com/counters").Open; len(open) != 5 {
		t.Errorf("%d CRUD rpcs projected into the open-procedure set, want 5: %+v", len(open), open)
	}
}
