package codegen

import (
	"go/ast"
	"go/printer"
	"go/token"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
)

// The success-log sampling wire has TWO ends and they have to agree.
//
//   - proto/config/v1/config.proto (config.proto.tmpl) declares
//     log_success_sample_window. It is what types the value, validates it at
//     startup, and generates the KCL schema + env projection a deployment
//     sets it through (deploy/kcl/<env>/config.k).
//   - the scaffolded serve.go hands the LOADED value to the RPC edge's
//     logging layer, as observe.WithSuccessSampling in serverkit.ChainDeps'
//     LogOptions. forge/pkg/observe reads no environment, so this is the
//     only way the setting reaches it.
//
// A break between the two is silent: the field validates, the manifest
// carries the variable, and nothing reads it. So both ends are pinned here.

// TestScaffoldLogSuccessSampleWindowDeclaration: the declaration types the
// window, defaults to logging every success, and — because the loaded value
// is the one observe uses — accepts every source the loader reads, a flag
// included.
func TestScaffoldLogSuccessSampleWindowDeclaration(t *testing.T) {
	t.Parallel()
	out, err := templates.ProjectTemplates().Render("config.proto.tmpl", configproto.For("proj").TemplateData("example.com/proj"))
	if err != nil {
		t.Fatalf("render config.proto.tmpl: %v", err)
	}
	proto := string(out)
	idx := strings.Index(proto, "google.protobuf.Duration log_success_sample_window = ")
	if idx < 0 {
		t.Fatal("the scaffolded config.proto declares no log_success_sample_window Duration field — " +
			"a deployment then has no typed, per-environment way to turn success sampling on")
	}
	rest := proto[idx:]
	end := strings.Index(rest, "}]")
	if end < 0 {
		t.Fatal("log_success_sample_window has an unterminated option block")
	}
	block := rest[:end]

	if !strings.Contains(block, `env_var: "LOG_SUCCESS_SAMPLE_WINDOW"`) {
		t.Errorf("log_success_sample_window is not projected onto LOG_SUCCESS_SAMPLE_WINDOW, the "+
			"variable config_gen.k writes per environment:\n%s", block)
	}
	if !strings.Contains(block, `flag: "log-success-sample-window"`) {
		t.Errorf("log_success_sample_window declares no flag. serve.go passes the LOADED value to "+
			"observe, so every source the loader reads must work — a --config file and a flag "+
			"included:\n%s", block)
	}

	m := regexp.MustCompile(`default_value:\s*"([^"]*)"`).FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("log_success_sample_window has no default_value:\n%s", block)
	}
	window, perr := time.ParseDuration(m[1])
	if perr != nil {
		t.Fatalf("default_value %q is not a Go duration: %v", m[1], perr)
	}
	if window > 0 {
		t.Errorf("default_value %q samples successes; the default must log every success "+
			"(sampling is a per-environment opt-in)", m[1])
	}
}

// TestServeScaffoldPassesSuccessSampleWindow: when the config declares the
// field, serve.go's serverkit.ChainDeps literal carries
// LogOptions: observe.WithSuccessSampling(cfg.LogSuccessSampleWindow.AsDuration()).
// When it does not, the scaffold names no such field — a reference to it
// would not compile.
//
// It reads the PARSED scaffold: the option is found as the LogOptions key of
// the serverkit.ChainDeps literal, the only position serverkit.NewChain reads it from,
// so a mention in a comment or a dead assignment cannot satisfy it.
func TestServeScaffoldPassesSuccessSampleWindow(t *testing.T) {
	t.Parallel()
	const want = "[]observe.LogOption{observe.WithSuccessSampling(cfg.LogSuccessSampleWindow.AsDuration())}"

	got, found := chainLogOptions(t, parseServeScaffold(t, "LogSuccessSampleWindow"))
	if !found {
		t.Fatalf("serve.go's serverkit.ChainDeps sets no LogOptions although the config declares " +
			"log_success_sample_window — the window a deployment configured never reaches the " +
			"logging interceptor, and every success is logged whatever config.k says")
	}
	if got != want {
		t.Fatalf("serverkit.ChainDeps.LogOptions = %s, want %s", got, want)
	}

	if got, found := chainLogOptions(t, parseServeScaffold(t)); found {
		t.Fatalf("with no log_success_sample_window in the config, serve.go must not reference "+
			"it, but serverkit.ChainDeps.LogOptions = %s", got)
	}
}

// chainLogOptions returns the source of the LogOptions value in the
// scaffold's serverkit.ChainDeps literal, and whether it is set at all.
func chainLogOptions(t *testing.T, file *ast.File) (string, bool) {
	t.Helper()
	var literals int
	var value string
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isQualifiedType(lit.Type, "serverkit", "ChainDeps") {
			return true
		}
		literals++
		for _, elt := range lit.Elts {
			kv, isKV := elt.(*ast.KeyValueExpr)
			if !isKV {
				continue
			}
			if key, isIdent := kv.Key.(*ast.Ident); isIdent && key.Name == "LogOptions" {
				var b strings.Builder
				if err := printer.Fprint(&b, token.NewFileSet(), kv.Value); err != nil {
					t.Fatalf("print LogOptions: %v", err)
				}
				value, found = b.String(), true
			}
		}
		return true
	})
	if literals != 1 {
		t.Fatalf("rendered serve.go builds %d serverkit.ChainDeps literals, want exactly 1 — the "+
			"interceptor chain this guard inspects", literals)
	}
	return value, found
}
