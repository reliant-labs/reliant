package codemod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// connect-go v2 code is never rewritten: v2's NewError takes a message
// string, and the codemod's wire-identity proof is v1's. Each v2 file with a
// connect error is listed with what to do instead, and nothing on disk
// changes. The run never type-checks, so it is fast enough for -short.
func TestConnectErrors_SkipsConnectV2Files(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n",
		"internal/handlers/things/things.go": `package things

import "connectrpc.com/connect/v2"

func f() error { return connect.NewError(connect.CodeInvalidArgument, "id is required") }
`,
		// A test's connect error is a fixture, never listed.
		"internal/handlers/things/things_test.go": `package things

import "connectrpc.com/connect/v2"

var _ = connect.CodeOf(nil)
`,
		// Importing a v2 SUBPACKAGE alone is not connect errors.
		"internal/server/mount.go": `package server

import "connectrpc.com/connect/v2/connecthttp"

var _ = connecthttp.Mount
`,
	}
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := runConnectErrors(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("runConnectErrors: %v", err)
	}
	if rep.Changed() {
		t.Fatalf("a v2 file was rewritten: %v", rep.Rewrites)
	}
	next := strings.Join(rep.Next, "\n")
	if !strings.Contains(next, "left as written: internal/handlers/things/things.go — imports connectrpc.com/connect/v2") {
		t.Errorf("worklist does not name the v2 handler file:\n%s", next)
	}
	if !strings.Contains(next, "forgeconv-handler-connect-error") {
		t.Errorf("worklist does not say what to do with v2 code:\n%s", next)
	}
	for _, rel := range []string{"things_test.go", "mount.go"} {
		if strings.Contains(next, rel) {
			t.Errorf("%s listed, want only files with v2 connect errors:\n%s", rel, next)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "internal", "handlers", "things", "things.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != files["internal/handlers/things/things.go"] {
		t.Errorf("v2 file changed on disk:\n%s", got)
	}

	// Outside --scope, a v2 file is neither rewritten nor listed.
	scope, err := ParseScope(dir, []string{"internal/server"})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := runConnectErrors(dir, Options{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Next) != 0 {
		t.Errorf("a v2 file outside --scope was listed: %v", scoped.Next)
	}
}

func TestImportsConnectV2(t *testing.T) {
	for src, want := range map[string]bool{
		"package a\n\nimport \"connectrpc.com/connect/v2\"\n":                       true,
		"package a\n\nimport cv2 \"connectrpc.com/connect/v2\"\n":                   true,
		"package a\n\nimport \"connectrpc.com/connect\"\n":                          false,
		"package a\n\nimport \"connectrpc.com/connect/v2/connecthttp\"\n":           false,
		"package a\n\n// uses connectrpc.com/connect/v2 later\nimport \"errors\"\n": false,
	} {
		if got := importsConnectV2([]byte(src)); got != want {
			t.Errorf("importsConnectV2(%q) = %v, want %v", src, got, want)
		}
	}
}
