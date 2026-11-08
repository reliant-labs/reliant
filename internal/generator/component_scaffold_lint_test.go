package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// A freshly scaffolded worker, adapter and webhook must pass forge's OWN
// `forge lint`.
//
// This is the same day-one credibility invariant that
// frontend_scaffold_lint_test.go pins for the frontend scaffold, applied to
// the three BACKEND component scaffolds. A dogfood run found that
// `forge scaffold worker` + `scaffold package --type adapter` + `scaffold
// webhook` left `forge lint` red with SEVEN findings, every one in code
// forge wrote and the author never touched:
//
//	internal/blobstore/adapter.go               errcheck (defer resp.Body.Close)
//	internal/handlers/<svc>/webhook_x_test.go   noctx ×3 (httptest.NewRequest)
//	internal/handlers/<svc>/webhook_x.go        staticcheck SA4023 ×2
//	internal/workers/<name>/worker.go           requirecontract
//
// A user whose first `forge lint` is red through no fault of their own
// learns on day one to ignore lint output — which destroys the signal for
// every real finding afterwards, and is strictly worse than not shipping
// the rules at all. `forge lint` also gates the forge-one-shot workflow's
// phases, so a scaffold that fails it makes the scaffold phase
// structurally unpassable.
//
// WHERE EACH LANE IS PINNED. The golangci lanes (errcheck / noctx /
// staticcheck) need a real golangci-lint over a COMPILED project, which is
// an e2e-tier cost; they are asserted end-to-end in
// internal/cli/scaffold_lint_clean_e2e_test.go
// (TestE2EComponentScaffoldsLintClean), which runs `forge lint` itself and
// so cannot drift from the shipped rule set. The requirecontract lane runs
// through the REAL analyzer in
// internal/linter/contract/analyzer_test.go (TestRequireContract_Worker*).
//
// What THIS file adds is the unit-tier tripwire: the source-level SHAPE
// each finding turned on, read off the rendered template, so a regression
// shows up in `go test -short ./...` in milliseconds instead of only in
// the tagged lane.

// ── worker ──────────────────────────────────────────────────────────────

// renderWorkerScaffold renders the worker templates the way `forge scaffold
// worker` does and returns the worker package directory.
func renderWorkerScaffold(t *testing.T, kind string) string {
	t.Helper()
	root := t.TempDir()
	if err := GenerateWorkerFiles(root, "example.com/lintapp", "share_expiry", kind, "0 3 * * *"); err != nil {
		t.Fatalf("GenerateWorkerFiles(%s): %v", kind, err)
	}
	return filepath.Join(root, "internal", "workers", "share_expiry")
}

// TestScaffoldedWorker_ExportsTheSupervisedSurface pins the PRECONDITION
// that makes the requirecontract exemption necessary and non-vacuous: the
// worker scaffold really does emit exported methods and really does not
// emit a contract.go.
//
// Without this, the linter-side exemption test could pass because the
// scaffold quietly stopped exporting anything — a green bar over a rule
// that no longer has an input.
func TestScaffoldedWorker_ExportsTheSupervisedSurface(t *testing.T) {
	for _, kind := range []string{"", "cron"} {
		name := kind
		if name == "" {
			name = "poll"
		}
		t.Run(name, func(t *testing.T) {
			pkgDir := renderWorkerScaffold(t, kind)

			src := readScaffoldFile(t, filepath.Join(pkgDir, "worker.go"))
			// serverkit.Worker is Name() + Start(ctx) + Stop(ctx); the cron
			// variant adds Run(ctx). These are the exported methods the
			// requirecontract rule fires on.
			for _, want := range []string{
				"func (w *Worker) Name() string",
				"func (w *Worker) Start(ctx context.Context) error",
			} {
				if !strings.Contains(src, want) {
					t.Errorf("worker scaffold no longer emits %q — the requirecontract exemption "+
						"it needs would be vacuous:\n%s", want, src)
				}
			}

			if _, err := os.Stat(filepath.Join(pkgDir, "contract.go")); err == nil {
				t.Log("worker scaffold now emits contract.go; the structural exemption is " +
					"harmless but no longer load-bearing")
			}
		})
	}
}

// TestScaffoldedWorker_IsNotOptedOutByDirective pins WHICH repair was
// chosen. The rule could have been satisfied by stamping
// `//forge:exclude-contract` on every worker package, and that was
// deliberately NOT done: the directive is the user's opt-out for a package
// with no behavioral interface, and spending it on forge's behalf would
// silence the rule for any REAL contract the user later adds to the worker
// (a Repository dep interface, say). The exemption is structural, in the
// linter, where it can stay narrow.
func TestScaffoldedWorker_IsNotOptedOutByDirective(t *testing.T) {
	pkgDir := renderWorkerScaffold(t, "")
	if codegen.HasExcludeContractDirective(pkgDir) {
		t.Errorf("the worker scaffold stamps //forge:exclude-contract, which spends the USER's " +
			"per-package opt-out to paper over a forge-side rule gap — and silences the rule " +
			"for any genuine contract the user later adds to this package. The worker's " +
			"contract is serverkit.Worker; exempt it structurally in the linter instead.")
	}
}

// ── webhook ─────────────────────────────────────────────────────────────

// renderWebhookScaffold renders the webhook scaffold the way
// `forge scaffold webhook scanner --service documents` does.
func renderWebhookScaffold(t *testing.T) (handler, test string) {
	t.Helper()
	root := t.TempDir()
	svcDir := filepath.Join(root, "internal", "handlers", "documents")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := GenerateWebhookFiles(root, "example.com/lintapp", "documents", "scanner"); err != nil {
		t.Fatalf("GenerateWebhookFiles: %v", err)
	}
	return readScaffoldFile(t, filepath.Join(svcDir, "webhook_scanner.go")),
		readScaffoldFile(t, filepath.Join(svcDir, "webhook_scanner_test.go"))
}

// TestScaffoldedWebhook_HasNoTautologicalErrorCheck pins the source shape
// that staticcheck's SA4023 turned on.
//
// The scaffold's verify<Name>Signature ended in an unconditional
// `return fmt.Errorf("TODO: ...")`, so it provably never returns a nil
// error, so the caller's `if err != nil` is a tautology — staticcheck
// reports both the call site and the function.
//
// Failing closed is RIGHT: forge must not scaffold a webhook that accepts
// unsigned payloads. So the repair keeps the rejection and removes only the
// PROVABILITY — the verifier reads a configured secret and returns nil on a
// valid HMAC, so nil is reachable in the type system while an unconfigured
// secret still rejects every delivery. Both halves are asserted here,
// because "make SA4023 go away" has an obvious wrong answer (`return nil`)
// that would ship an open webhook.
func TestScaffoldedWebhook_HasNoTautologicalErrorCheck(t *testing.T) {
	handler, _ := renderWebhookScaffold(t)
	verify := funcBody(t, handler, "verifyScannerSignature")

	if !strings.Contains(verify, "return nil") {
		t.Errorf("verifyScannerSignature never returns nil, so `if err != nil` at its call site is "+
			"a tautology (staticcheck SA4023) and a fresh `forge scaffold webhook` cannot pass "+
			"`forge lint`. Keep failing closed, but make the success path REACHABLE:\n%s", verify)
	}
	if !strings.Contains(verify, "VerifyHMACSHA256") {
		t.Errorf("verifyScannerSignature no longer verifies a signature — the SA4023 repair must "+
			"not be a bare `return nil`, which would scaffold a webhook that accepts unsigned "+
			"payloads:\n%s", verify)
	}
	if !strings.Contains(verify, `secret == ""`) {
		t.Errorf("verifyScannerSignature does not reject when the secret is unconfigured — an unset "+
			"secret must fail closed, never turn verification into a no-op:\n%s", verify)
	}
}

// TestScaffoldedWebhookTest_UsesContextualHTTPTestRequest pins the noctx
// lane: forge's own webhook TEST template called httptest.NewRequest, which
// the .golangci.yml forge itself scaffolds forbids in favour of
// httptest.NewRequestWithContext. Three findings, all in forge's file.
func TestScaffoldedWebhookTest_UsesContextualHTTPTestRequest(t *testing.T) {
	_, test := renderWebhookScaffold(t)

	// NewRequestWithContext shares a prefix with the banned form, so match
	// the bare call including its open paren.
	if strings.Contains(test, "httptest.NewRequest(") {
		t.Errorf("the scaffolded webhook test calls httptest.NewRequest, which the .golangci.yml "+
			"forge scaffolds forbids (noctx). Every new webhook starts life with three lint "+
			"findings in a file forge wrote:\n%s", test)
	}
	if !strings.Contains(test, "httptest.NewRequestWithContext(") {
		t.Errorf("the scaffolded webhook test no longer builds requests — the noctx repair must "+
			"switch to NewRequestWithContext, not delete the coverage:\n%s", test)
	}
}

// ── adapter ─────────────────────────────────────────────────────────────

// renderAdapterScaffold renders adapter.go the way
// `forge scaffold package blobstore --type adapter` does.
func renderAdapterScaffold(t *testing.T) string {
	t.Helper()
	data := struct {
		Name         string
		ImportPath   string
		Module       string
		Flavor       string
		LogLevel     string
		ConfigBlock  string
		ConfigEnvVar string
	}{"blobstore", "blobstore", "example.com/lintapp", "adapter", "slog.LevelDebug", "BlobstoreConfig", "BLOBSTORE_BASE_URL"}
	content, err := templates.InternalPkgKindTemplates("adapter").Render("adapter.go.tmpl", data)
	if err != nil {
		t.Fatalf("render adapter.go.tmpl: %v", err)
	}
	return string(content)
}

// TestScaffoldedAdapter_ChecksDeferredCloseError pins the errcheck lane.
// `defer resp.Body.Close()` discards an error return, which errcheck
// reports; the repair is the idiom forge already uses throughout its own
// pkg/ tree — `defer func() { _ = resp.Body.Close() }()`.
func TestScaffoldedAdapter_ChecksDeferredCloseError(t *testing.T) {
	src := renderAdapterScaffold(t)

	// Declarations only: the comment explaining the repair quotes the
	// REJECTED spelling on purpose, so the next reader learns which is
	// which. Matching the raw file would flag forge's own explanation.
	src = stripComments(src)

	if strings.Contains(src, "defer resp.Body.Close()") {
		t.Errorf("the scaffolded adapter uses `defer resp.Body.Close()`, whose discarded error "+
			"errcheck reports — a lint finding in forge's own file on a brand-new adapter:\n%s", src)
	}
	if !strings.Contains(src, "resp.Body.Close()") {
		t.Errorf("the scaffolded adapter no longer closes the response body — the errcheck repair "+
			"must handle the error, not drop the Close:\n%s", src)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────

// stripComments drops `//` comment text so an assertion about emitted CODE
// is not satisfied — or falsely tripped — by prose that quotes the very
// spelling under test.
func stripComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func readScaffoldFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// funcBody returns the source of the named top-level func, from its `func`
// keyword to the closing brace at column 0, so an assertion about one
// function cannot be satisfied by text elsewhere in the file.
func funcBody(t *testing.T, src, funcName string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "func ") && strings.Contains(line, funcName+"(") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("func %s not found in scaffolded source:\n%s", funcName, src)
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("func %s has no closing brace at column 0", funcName)
	return ""
}
