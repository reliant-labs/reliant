package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectServiceInterfaceName covers the three resolution outcomes:
// canonical `Service` always wins, a `//forge:service`/`//forge:contract`
// marker frees a role-oriented name, and neither yields "".
func TestDetectServiceInterfaceName(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "canonical Service interface",
			src:  "package p\ntype Service interface{ Do() }\ntype Deps struct{}\n",
			want: "Service",
		},
		{
			name: "canonical wins over a marked sibling",
			src: "package p\n" +
				"//forge:service\ntype Gateway interface{ Charge() }\n" +
				"type Service interface{ Do() }\n",
			want: "Service",
		},
		{
			name: "service marker frees the name",
			src:  "package p\n//forge:service\ntype Gateway interface{ Charge() }\n",
			want: "Gateway",
		},
		{
			name: "contract marker synonym (spaced form)",
			src:  "package p\n// forge:contract\ntype Dispatcher interface{ Dispatch() }\n",
			want: "Dispatcher",
		},
		{
			name: "inline marker slot",
			src:  "package p\ntype Provider interface{ Get() } // forge:service\n",
			want: "Provider",
		},
		{
			name: "no service type and no marker",
			src:  "package p\ntype Repository interface{ Get() }\ntype Deps struct{}\n",
			want: "",
		},
		{
			name: "marker on a struct does not count (interfaces only)",
			src:  "package p\n//forge:service\ntype Config struct{ X int }\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(tc.src), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := DetectServiceInterfaceName(dir); got != tc.want {
				t.Fatalf("DetectServiceInterfaceName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateInject_MarkedInterfaceWiresByType is the codegen half of the
// marker relaxation: a package whose contract interface is `//forge:service`
// -marked `Gateway` produces the type key `<path>.Gateway`, so a consumer's
// `payments.Gateway` Deps field resolves to it by type (constructed first),
// exactly as a canonical `Service` would — no rename, no name-match.
func TestGenerateInject_MarkedInterfaceWiresByType(t *testing.T) {
	dir := newInjectProject(t)

	// Producer package `payments` whose contract interface is a marked Gateway.
	pkgDir := filepath.Join(dir, "internal", "payments")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payments := "package payments\n\n" +
		"//forge:service\ntype Gateway interface{ Charge() }\n\n" +
		"type Deps struct{}\n\n" +
		"func New(d Deps) (Gateway, error) { return nil, nil }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(payments), 0o644); err != nil {
		t.Fatalf("write payments: %v", err)
	}

	// Consumer handler `checkout` depends on payments.Gateway BY TYPE. It
	// carries no import block, so the resolver falls back to the bare
	// package-clause key `payments.Gateway` — unambiguous here, so it
	// resolves to the marked producer just the same.
	writeComponentDeps(t, dir, "internal/handlers", "checkout", "checkout",
		"\tPayments payments.Gateway")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "CheckoutService", ModulePath: "example.com/proj"}},
		Packages: []BootstrapPackageData{
			{Name: "payments", Package: "payments", ImportPath: "payments", FieldName: "Payments", Fallible: true},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	// The marked producer is constructed and the consumer's by-type dep
	// resolves to its local var (not infra / not a MissingProvider).
	if !strings.Contains(out, "Payments: paymentsInst,") {
		t.Fatalf("consumer's payments.Gateway dep should wire to the marked producer var:\n%s", out)
	}
}
