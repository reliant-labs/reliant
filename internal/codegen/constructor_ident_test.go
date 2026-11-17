package codegen

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `// forge:constructor` must identify a constructor by the MARKER, not by the
// name. Three scanners used to spell the check as `fn.Name.Name != "New"`, so
// the marker whose only job is to say "this is the constructor" was itself
// gated on the constructor already being called New. Tagging `NewReadOnly`,
// `Open` or `Connect` did nothing, and did it silently.
//
// forge already detects the CONTRACT name (DetectServiceInterfaceName, with
// "Service" merely a fallback) so a package can name its interface `Mailer` or
// `Repository`. The constructor gets the same freedom: forge promotes good
// practice, it does not require every component to be `New` returning
// `Service`.
func TestIsComponentConstructor(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "marker on an arbitrarily named func",
			src:  "package p\n// forge:constructor\nfunc Open(d Deps) (Mailer, error) { return nil, nil }\n",
			want: true,
		},
		{
			name: "marker on a New-prefixed variant",
			src:  "package p\n// forge:constructor\nfunc NewReadOnly(d Deps) (Mailer, error) { return nil, nil }\n",
			want: true,
		},
		{
			name: "unmarked New still counts, so an unmarked package works",
			src:  "package p\nfunc New(d Deps) (Service, error) { return nil, nil }\n",
			want: true,
		},
		{
			name: "unmarked, non-New func is not a constructor",
			src:  "package p\nfunc Open(d Deps) (Mailer, error) { return nil, nil }\n",
			want: false,
		},
		{
			name: "a METHOD is never a constructor, marker or not",
			src:  "package p\n// forge:constructor\nfunc (s *svc) New(d Deps) (Service, error) { return nil, nil }\n",
			want: false,
		},
		{
			name: "unspaced marker form is recognized",
			src:  "package p\n//forge:constructor\nfunc Connect(d Deps) (Store, error) { return nil, nil }\n",
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "x.go", tc.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var fn *ast.FuncDecl
			for _, d := range file.Decls {
				if f, ok := d.(*ast.FuncDecl); ok {
					fn = f
				}
			}
			if fn == nil {
				t.Fatal("no func in fixture")
			}
			if got := IsComponentConstructor(fn); got != tc.want {
				t.Errorf("IsComponentConstructor = %v, want %v — the marker exists to NAME the "+
					"constructor; gating it on the name defeats its purpose", got, tc.want)
			}
		})
	}
}

// DetectConstructorName is the EMIT-side half of the marker. Freeing the name
// on the discovery side alone would be worse than not freeing it: lint would
// bless `// forge:constructor func Open(Deps) (Mailer, error)` while the
// injector kept emitting `pkg.New(pkg.Deps{...})`, so `forge generate` would
// write a compile-broken compose.go and point the user at generated code they
// don't own — the exact failure the contract-shape rule exists to prevent.
func TestDetectConstructorName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "unmarked package keeps the canonical New",
			files: map[string]string{"contract.go": "package p\n\ntype Deps struct{}\nfunc New(d Deps) (Service, error) { return nil, nil }\n"},
			want:  "New",
		},
		{
			name:  "marked name is what the injector must emit",
			files: map[string]string{"contract.go": "package p\n\ntype Deps struct{}\n// forge:constructor\nfunc Open(d Deps) (Mailer, error) { return nil, nil }\n"},
			want:  "Open",
		},
		{
			name: "a marked func in a sibling file beats an unmarked New",
			files: map[string]string{
				"contract.go": "package p\n\ntype Deps struct{}\nfunc New(d Deps) (Store, error) { return nil, nil }\n",
				"store.go":    "package p\n\n//forge:constructor\nfunc Connect(d Deps) (Store, error) { return nil, nil }\n",
			},
			want: "Connect",
		},
		{
			name:  "a method is never the constructor",
			files: map[string]string{"contract.go": "package p\n\ntype Deps struct{}\n// forge:constructor\nfunc (s *svc) Open(d Deps) (Service, error) { return nil, nil }\n"},
			want:  "New",
		},
		{
			name:  "generated and test files never supply the name",
			files: map[string]string{"mock_gen.go": "package p\n\n//forge:constructor\nfunc NewMock(d Deps) (Service, error) { return nil, nil }\n"},
			want:  "New",
		},
		{
			name:  "a missing package falls back to the scaffolded shape",
			files: nil,
			want:  "New",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, src := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			if tc.files == nil {
				dir = filepath.Join(dir, "absent")
			}
			if got := DetectConstructorName(dir); got != tc.want {
				t.Errorf("DetectConstructorName = %q, want %q — detection and emission must "+
					"read the same marker or generate writes code that cannot compile", got, tc.want)
			}
		})
	}
}

// The injector must emit the DETECTED constructor, not the literal `New`.
// This is the assertion that makes the marker mean something end-to-end.
func TestComposeComponentBlockEmitsDetectedConstructor(t *testing.T) {
	for _, tc := range []struct {
		name string
		data InjectComponentData
		want string
	}{
		{
			name: "fallible, marked name",
			data: InjectComponentData{FieldName: "Mailer", LocalVar: "mailerInst", Alias: "mailer", Constructor: "Open", Fallible: true},
			want: "mailer.Open(mailer.Deps{",
		},
		{
			name: "infallible, marked name",
			data: InjectComponentData{FieldName: "Store", LocalVar: "storeInst", Alias: "store", Constructor: "Connect"},
			want: "store.Connect(store.Deps{",
		},
		{
			name: "unset Constructor still renders the canonical selector",
			data: InjectComponentData{FieldName: "Api", LocalVar: "apiInst", Alias: "api"},
			want: "api.New(api.Deps{",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := composeComponentBlock.Execute(&buf, tc.data); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("compose block must call %q; got:\n%s", tc.want, buf.String())
			}
		})
	}
}
