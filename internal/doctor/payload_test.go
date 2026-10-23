package doctor

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// payloadProject materialises a project tree from project-relative paths.
// Every fixture below spells out ONLY the files its case is about, so what
// a test asserts and what the check reads are the same list.
func payloadProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const (
	// payloadForgeYAML is the minimum forge.yaml config.LoadProjectDir
	// accepts. The project KIND it reports is derived from the tree, not
	// from this file, so each fixture steers the kind by which directories
	// it creates.
	payloadForgeYAML = "name: app\nmodule_path: github.com/acme/app\n"

	// payloadDescriptorWithService / payloadDescriptorEmpty are the two
	// authoritative answers gen/forge_descriptor.json can give. Neither
	// carries sources: an unstamped descriptor skips the staleness
	// guard, which is what a hand-built fixture wants.
	payloadDescriptorWithService = `{"services":[{"Name":"BillingService","Package":"acme.billing.v1"}]}`
	payloadDescriptorEmpty       = `{"services":[]}`
)

func TestCheckPayloadLimits(t *testing.T) {
	// The shape forge emits today: the cap is read off a config struct
	// this file never assigns, so connect receives 0 = unlimited.
	unassignedField := `package cmd

import "connectrpc.com/connect"

func Serve() {
	skCfg := serverkit.Config{
		Addr:        ":8080",
		ServiceName: "app",
	}
	opts := []connect.HandlerOption{
		connect.WithReadMaxBytes(skCfg.ReadMaxBytes),
		connect.WithSendMaxBytes(skCfg.SendMaxBytes),
	}
	_ = opts
}
`

	assignedInLiteral := `package cmd

import "connectrpc.com/connect"

func Serve() {
	skCfg := serverkit.Config{
		Addr:         ":8080",
		ReadMaxBytes: 4 << 20,
		SendMaxBytes: 4 << 20,
	}
	opts := []connect.HandlerOption{
		connect.WithReadMaxBytes(skCfg.ReadMaxBytes),
		connect.WithSendMaxBytes(skCfg.SendMaxBytes),
	}
	_ = opts
}
`

	assignedBySelector := `package cmd

import "connectrpc.com/connect"

func Serve() {
	var skCfg serverkit.Config
	skCfg.ReadMaxBytes = 4 << 20
	skCfg.SendMaxBytes = 4 << 20
	opts := []connect.HandlerOption{
		connect.WithReadMaxBytes(skCfg.ReadMaxBytes),
		connect.WithSendMaxBytes(skCfg.SendMaxBytes),
	}
	_ = opts
}
`

	// The shape forge emits for a project whose config proto predates the
	// read_max_bytes / send_max_bytes fields: nothing assigns the field, but
	// the config is normalized before it is read, so the caps are 4 MiB. A
	// FAIL here would be a false positive on correct, protected code.
	normalizedBeforeRead := `package cmd

import "connectrpc.com/connect"

func Serve() {
	skCfg, err := projectServerkitConfig(cfg)
	_ = err
	opts := []connect.HandlerOption{
		connect.WithReadMaxBytes(skCfg.ReadMaxBytes),
		connect.WithSendMaxBytes(skCfg.SendMaxBytes),
	}
	_ = opts
}

func projectServerkitConfig(cfg *config.Config) (serverkit.Config, error) {
	skCfg := serverkit.Config{Addr: ":8080"}
	skCfg.Normalize()
	return skCfg, nil
}
`

	literalZero := `package cmd

import "connectrpc.com/connect"

func Serve() {
	opts := []connect.HandlerOption{
		connect.WithReadMaxBytes(0),
	}
	_ = opts
}
`

	// The scaffold since the chain became a component: serverkit.NewChain
	// applies the caps itself and maps zero to 4 MiB, so a chain is capped
	// whether or not the field is set — unless it names a negative cap.
	chainCapped := `package cmd

import "github.com/reliant-labs/forge/pkg/serverkit"

func Serve() {
	chain, _ := serverkit.NewChain(serverkit.ChainDeps{
		Auth:         auth,
		ReadMaxBytes: skCfg.ReadMaxBytes,
	})
	_ = chain
}
`
	chainUncapped := `package cmd

import sk "github.com/reliant-labs/forge/pkg/serverkit"

func Serve() {
	chain, _ := sk.NewChain(sk.ChainDeps{Auth: auth, SendMaxBytes: -1})
	_ = chain
}
`

	tests := []struct {
		name     string
		serve    string
		want     Status
		evidence string
	}{
		{
			name:     "a serverkit chain is capped",
			serve:    chainCapped,
			want:     StatusPass,
			evidence: "NewChain.SendMaxBytes(default 4 MiB)",
		},
		{
			name:     "a chain with a negative cap is unlimited",
			serve:    chainUncapped,
			want:     StatusFail,
			evidence: "SendMaxBytes: -1",
		},
		{
			name:     "cap read from a never-assigned field is unlimited",
			serve:    unassignedField,
			want:     StatusFail,
			evidence: "ReadMaxBytes is never assigned",
		},
		{
			name:  "cap set in the composite literal passes",
			serve: assignedInLiteral,
			want:  StatusPass,
		},
		{
			name:  "cap set by selector assignment passes",
			serve: assignedBySelector,
			want:  StatusPass,
		},
		{
			name:  "cap read from a normalized config passes",
			serve: normalizedBeforeRead,
			want:  StatusPass,
		},
		{
			name:     "literal zero is unlimited",
			serve:    literalZero,
			want:     StatusFail,
			evidence: "WithReadMaxBytes(0)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := payloadProject(t, map[string]string{
				"cmd/app/cmd/serve.go":       tt.serve,
				"forge.yaml":                 payloadForgeYAML,
				"gen/forge_descriptor.json":  payloadDescriptorWithService,
				"internal/handlers/svc/s.go": "package svc\n",
			})
			env := &Environment{ProjectName: "test", ProjectDir: dir}
			got := CheckPayloadLimits(context.Background(), env)
			if got.Status != tt.want {
				t.Fatalf("status = %q, want %q (msg=%s ev=%s)", got.Status, tt.want, got.Message, got.Evidence)
			}
			// Evidence cites project files by their native path (backslashed on
			// Windows); the expectations are written slash-separated.
			if tt.evidence != "" && !strings.Contains(filepath.ToSlash(got.Evidence), tt.evidence) {
				t.Errorf("evidence %q does not mention %q", got.Evidence, tt.evidence)
			}
		})
	}
}

// noCaps is a composition root that mounts Connect handlers and passes no
// cap of any kind — the exposure the FAIL branch exists to name.
const noCaps = `package cmd

import "connectrpc.com/connect"

func Serve() {
	opts := []connect.HandlerOption{
		connect.WithInterceptors(),
	}
	_ = opts
}
`

// noConnect is a CLI-shaped composition root: no Connect anywhere.
const noConnect = `package cmd

func Run() error { return nil }
`

// TestCheckPayloadLimitsMountingVerdict covers the discriminator that used
// to be a substring grep of internal/handlers/ for "connect.HandlerOption".
// Every row here is a shape that grep got wrong, or a shape whose honest
// answer is "I could not tell".
func TestCheckPayloadLimitsMountingVerdict(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		want     Status
		evidence string
	}{
		{
			// The conventional shape: handlers under internal/handlers/,
			// declared in the descriptor. Unchanged verdict, now reached
			// through the descriptor rather than a text match.
			name: "handlers in the conventional place with no caps fails",
			files: map[string]string{
				"cmd/app/cmd/serve.go":      noConnect,
				"forge.yaml":                payloadForgeYAML,
				"gen/forge_descriptor.json": payloadDescriptorWithService,
				"internal/handlers/billing/service.go": "package billing\n\n" +
					"import \"connectrpc.com/connect\"\n\n" +
					"func Mount(opts ...connect.HandlerOption) {}\n",
			},
			want:     StatusFail,
			evidence: "BillingService",
		},
		{
			// THE REGRESSION THIS REWRITE EXISTS FOR. The handler package
			// lives in internal/server/, not internal/handlers/, so the old
			// grep found nothing and the check reported "not applicable" on
			// a project with unbounded payloads.
			name: "handlers mounted outside internal/handlers are not a silent skip",
			files: map[string]string{
				"cmd/app/cmd/serve.go":      noConnect,
				"forge.yaml":                payloadForgeYAML,
				"gen/forge_descriptor.json": payloadDescriptorWithService,
				"internal/server/mount.go": "package server\n\n" +
					"import \"connectrpc.com/connect\"\n\n" +
					"func Mount(opts ...connect.HandlerOption) {}\n",
			},
			want:     StatusFail,
			evidence: "gen/forge_descriptor.json declares 1 Connect service(s)",
		},
		{
			// The other half of the same blind spot: the composition root
			// itself builds the handler options and the project has no
			// internal/handlers/ tree at all.
			name: "handler wiring in the composition root alone is proof",
			files: map[string]string{
				"cmd/app/cmd/serve.go": noCaps,
				"forge.yaml":           payloadForgeYAML,
			},
			want:     StatusFail,
			evidence: "cmd/app/cmd/serve.go",
		},
		{
			// A generated Connect handler constructor is a mount even when
			// the option type is never named.
			name: "a generated NewXxxHandler call is proof",
			files: map[string]string{
				"cmd/app/cmd/serve.go": "package cmd\n\n" +
					"import billingv1connect \"github.com/acme/app/gen/acme/billing/v1/billingv1connect\"\n\n" +
					"func Serve(mux *http.ServeMux, svc billingv1connect.BillingServiceHandler) {\n" +
					"\tmux.Handle(billingv1connect.NewBillingServiceHandler(svc))\n" +
					"}\n",
				"forge.yaml": payloadForgeYAML,
			},
			want:     StatusFail,
			evidence: "NewBillingServiceHandler",
		},
		{
			// The grep's other direction: a doc comment naming the type was
			// read as proof and the check FAILED a project that mounts
			// nothing. A comment produces no AST node, so it cannot.
			name: "a comment mentioning connect.HandlerOption is not proof",
			files: map[string]string{
				"cmd/app/cmd/serve.go":      noConnect,
				"cmd/app/main.go":           "package main\n\nfunc main() {}\n",
				"forge.yaml":                payloadForgeYAML,
				"gen/forge_descriptor.json": payloadDescriptorEmpty,
				"internal/handlers/doc.go": "package handlers\n\n" +
					"// Handlers here are mounted with connect.HandlerOption values\n" +
					"// supplied by the composition root. Nothing is mounted yet.\n",
			},
			want:     StatusSkip,
			evidence: "declares no Connect services",
		},
		{
			// A genuine CLI: no descriptor is a permanent, correct state,
			// and the derived kind says so.
			name: "a cli project is not applicable",
			files: map[string]string{
				"cmd/app/cmd/serve.go": noConnect,
				"cmd/app/main.go":      "package main\n\nfunc main() {}\n",
				"forge.yaml":           payloadForgeYAML,
			},
			want:     StatusSkip,
			evidence: "cli project",
		},
		{
			// UNDETERMINED #1: service-shaped, but the authoritative list
			// of Connect services has never been generated.
			name: "service-shaped project with no descriptor is undetermined",
			files: map[string]string{
				"cmd/app/cmd/serve.go":       noConnect,
				"forge.yaml":                 payloadForgeYAML,
				"internal/handlers/svc/s.go": "package svc\n",
			},
			want:     StatusUnknown,
			evidence: "has not been generated",
		},
		{
			// UNDETERMINED #2: the descriptor exists and will not parse, so
			// forge has neither list nor fallback.
			name: "an unreadable descriptor is undetermined",
			files: map[string]string{
				"cmd/app/cmd/serve.go":      noConnect,
				"forge.yaml":                payloadForgeYAML,
				"gen/forge_descriptor.json": "{not json",
			},
			want:     StatusUnknown,
			evidence: "could not be read",
		},
		{
			// UNDETERMINED #3: neither artefact exists. A bare cmd/ tree
			// says nothing about Connect either way.
			name: "no descriptor and no forge.yaml is undetermined",
			files: map[string]string{
				"cmd/app/cmd/serve.go": noConnect,
			},
			want:     StatusUnknown,
			evidence: "no readable forge.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := payloadProject(t, tt.files)
			got := CheckPayloadLimits(context.Background(), &Environment{ProjectDir: dir})
			if got.Status != tt.want {
				t.Fatalf("status = %q, want %q (msg=%s ev=%s)", got.Status, tt.want, got.Message, got.Evidence)
			}
			// Native paths in the evidence; slash-separated expectations.
			if tt.evidence != "" && !strings.Contains(filepath.ToSlash(got.Evidence), tt.evidence) {
				t.Errorf("evidence %q does not mention %q", got.Evidence, tt.evidence)
			}
		})
	}
}

// A project with no cmd/ tree (a library) must skip, not fail.
func TestCheckPayloadLimitsSkipsWithoutCmd(t *testing.T) {
	env := &Environment{ProjectName: "test", ProjectDir: t.TempDir()}
	if got := CheckPayloadLimits(context.Background(), env); got.Status != StatusSkip {
		t.Fatalf("status = %q, want %q", got.Status, StatusSkip)
	}
}

// _test.go files under cmd/ must not be read: a test may legitimately
// exercise a zero cap.
func TestCheckPayloadLimitsIgnoresTestFiles(t *testing.T) {
	dir := payloadProject(t, map[string]string{
		"cmd/app/cmd/serve.go":      "package cmd\n",
		"cmd/app/cmd/serve_test.go": "package cmd\n\nimport \"connectrpc.com/connect\"\n\nfunc x() { connect.WithReadMaxBytes(0) }\n",
		"forge.yaml":                payloadForgeYAML,
		"gen/forge_descriptor.json": payloadDescriptorWithService,
	})
	got := CheckPayloadLimits(context.Background(), &Environment{ProjectDir: dir})
	if strings.Contains(got.Evidence, "serve_test.go") {
		t.Fatalf("check sourced a finding from a _test.go file: %s", got.Evidence)
	}
	// With the test file ignored, the only remaining shape is "Connect
	// services declared, no caps anywhere".
	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q (msg=%s)", got.Status, StatusFail, got.Message)
	}
}

// An unreadable cmd/ ROOT is UNDETERMINED, not the Skip ("no Go files
// under cmd/") it used to produce — nothing was read, so the check has no
// answer to give.
func TestCheckPayloadLimitsUnknownWhenCmdUnreadable(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("permission bits do not deny the caller here")
	}
	dir := payloadProject(t, map[string]string{
		"cmd/app/cmd/serve.go": noConnect,
		"forge.yaml":           payloadForgeYAML,
	})
	cmdDir := filepath.Join(dir, "cmd")
	if err := os.Chmod(cmdDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cmdDir, 0o755) })

	got := CheckPayloadLimits(context.Background(), &Environment{ProjectDir: dir})
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q (msg=%s)", got.Status, StatusUnknown, got.Message)
	}
}

// connectHandlerEvidence must read the file's IMPORTS, not its spelling:
// that is what makes it immune to both grep failures at once.
func TestConnectHandlerEvidence(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string // "" → no evidence at all
	}{
		{
			name: "aliased runtime import still resolves",
			src: "package cmd\n\nimport conn \"connectrpc.com/connect\"\n\n" +
				"func f(opts ...conn.HandlerOption) {}\n",
			want: "conn.HandlerOption",
		},
		{
			name: "pre-rename connect-go import resolves",
			src: "package cmd\n\nimport \"github.com/bufbuild/connect-go\"\n\n" +
				"func f(opts ...connect.HandlerOption) {}\n",
			want: "connect.HandlerOption",
		},
		{
			name: "generated handler constructor counts",
			src: "package cmd\n\nimport \"github.com/acme/app/gen/acme/v1/acmev1connect\"\n\n" +
				"func f() { mux.Handle(acmev1connect.NewAcmeServiceHandler(svc)) }\n",
			want: "NewAcmeServiceHandler",
		},
		{
			name: "a comment is never evidence",
			src: "package cmd\n\n" +
				"// Mount wires handlers; the caller supplies connect.HandlerOption values.\n" +
				"func Mount() {}\n",
			want: "",
		},
		{
			// The binding is what counts. Nothing in this file imports the
			// connect-go runtime, so the identifier `connect` names
			// something else and the selector is not proof of anything.
			name: "a selector bound to no connect import is never evidence",
			src:  "package cmd\n\nfunc f(opts ...connect.HandlerOption) {}\n",
			want: "",
		},
		{
			// A project's own internal/connect package is not connect-go.
			name: "a local package named connect is not the runtime",
			src: "package cmd\n\nimport \"github.com/acme/app/internal/connect\"\n\n" +
				"func f(opts ...connect.HandlerOption) {}\n",
			want: "",
		},
		{
			// A blank import wires nothing; a dot import puts names in
			// scope unqualified, where a selector scan cannot see them.
			// Neither may be reported as proof.
			name: "a blank import is never evidence",
			src:  "package cmd\n\nimport _ \"connectrpc.com/connect\"\n\nfunc f() {}\n",
			want: "",
		},
		{
			name: "a client-only file is not a mount",
			src: "package cmd\n\nimport \"connectrpc.com/connect\"\n\n" +
				"func f(opts ...connect.ClientOption) {}\n",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "serve.go", tt.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			got := connectHandlerEvidence(file, fset, "cmd/app/cmd/serve.go")
			joined := strings.Join(got, "\n")
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("expected no evidence, got %q", joined)
				}
				return
			}
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("evidence %q does not mention %q", joined, tt.want)
			}
		})
	}
}
