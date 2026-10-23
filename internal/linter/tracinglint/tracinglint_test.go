package tracinglint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// writeTree writes files (project-relative path → content) under a fresh
// project root with a go.mod for module example.com/app.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.com/app\n\ngo 1.24\n"
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// hits renders findings as "rule file:line" for compact assertions.
func hits(res Result) []string {
	var out []string
	for _, f := range res.Findings {
		out = append(out, f.Rule+" "+f.File+":"+itoa(f.Line))
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func assertHits(t *testing.T, res Result, want ...string) {
	t.Helper()
	got := hits(res)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestLogWithoutCtx(t *testing.T) {
	src := `package svc

import (
	"context"
	"log/slog"
	"net/http"

	"go.uber.org/zap"
)

type Service struct {
	log *slog.Logger
}

type other struct {
	log *zap.Logger // same name, different type: s.log is ambiguous package-wide
}

type worker struct {
	logger *slog.Logger
}

func (s *Service) Get(ctx context.Context, id string) {
	slog.Info("get", "id", id) // 24: flagged
}

func NoCtx() {
	slog.Info("boot") // no context in scope: not flagged
}

func (w *worker) Run(ctx context.Context) {
	w.logger.Warn("tick")                       // 32: flagged
	l := slog.Default().With("k", "v")
	l.Error("boom")                              // 34: flagged
	go func(ctx string) { slog.Info(ctx) }("x")  // ctx rebound to a string: not flagged
	func() { slog.Debug("inner") }()             // 36: closure sees ctx: flagged
}

func (s *Service) Ambiguous(ctx context.Context) {
	s.log.Info("x") // field name declared with two types: not flagged
}

func Handler(w http.ResponseWriter, r *http.Request) {
	slog.Info("req") // 44: flagged, r.Context()
}

func Blank(_ context.Context) {
	slog.Info("blank") // unnamed context: not flagged
}

func Already(ctx context.Context) {
	slog.InfoContext(ctx, "ok")
}
`
	root := writeTree(t, map[string]string{"internal/svc/svc.go": src})
	res, err := Lint(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := "internal/svc/svc.go"
	assertHits(t, res,
		RuleLogWithoutCtx+" "+f+":24",
		RuleLogWithoutCtx+" "+f+":32",
		RuleLogWithoutCtx+" "+f+":34",
		RuleLogWithoutCtx+" "+f+":36",
		RuleLogWithoutCtx+" "+f+":44",
	)

	res, err = Lint(root, Options{Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("fix left findings: %v", hits(res))
	}
	if len(res.Fixed) != 1 || res.Fixed[0].Count != 5 {
		t.Fatalf("fixed = %+v, want one file with 5 rewrites", res.Fixed)
	}
	got, _ := os.ReadFile(filepath.Join(root, f))
	for _, want := range []string{
		`slog.InfoContext(ctx, "get", "id", id)`,
		`w.logger.WarnContext(ctx, "tick")`,
		`l.ErrorContext(ctx, "boom")`,
		`slog.DebugContext(ctx, "inner")`,
		`slog.InfoContext(r.Context(), "req")`,
		`slog.Info("boot")`,
		`s.log.Info("x")`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("fixed file lacks %q:\n%s", want, got)
		}
	}
	// Idempotent: a second run finds and rewrites nothing.
	res, err = Lint(root, Options{Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings)+len(res.Fixed) != 0 {
		t.Fatalf("second run not clean: %+v", res)
	}
}

func TestLogWithoutCtxWrappers(t *testing.T) {
	logging := `package logging

import "log/slog"

func Info(msg string, args ...any) { slog.Info(msg, args...) }
func Warn(msg string, args ...any) { slog.Warn(msg, args...) }
`
	src := `package svc

import (
	"context"

	"example.com/app/internal/logging"
)

func Do(ctx context.Context) {
	logging.Info("same package target", "k", 1)
	logging.Warn("other package target")
}
`
	// Every call in this file moves to slog: the wrapper import must go.
	onlyOther := `package other

import (
	"context"

	"example.com/app/internal/logging"
)

func Do(ctx context.Context) { logging.Warn("moved") }
`
	root := writeTree(t, map[string]string{
		"internal/logging/logging.go": logging,
		"internal/svc/svc.go":         src,
		"internal/other/other.go":     onlyOther,
	})
	opts := Options{LogWrappers: []LogWrapper{
		{Call: "example.com/app/internal/logging.Info"},
		{Call: "example.com/app/internal/logging.Warn", ContextCall: "log/slog.WarnContext"},
	}}
	res, err := Lint(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	// The wrapper's own body has no context in scope: only the callers hit.
	assertHits(t, res,
		RuleLogWithoutCtx+" internal/other/other.go:9",
		RuleLogWithoutCtx+" internal/svc/svc.go:10",
		RuleLogWithoutCtx+" internal/svc/svc.go:11",
	)
	opts.Fix = true
	if _, err := Lint(root, opts); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "internal/svc/svc.go"))
	for _, want := range []string{
		`logging.InfoContext(ctx, "same package target", "k", 1)`,
		`slog.WarnContext(ctx, "other package target")`,
		`"log/slog"`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("fixed file lacks %q:\n%s", want, got)
		}
	}
	other, _ := os.ReadFile(filepath.Join(root, "internal/other/other.go"))
	if strings.Contains(string(other), "internal/logging") || !strings.Contains(string(other), `slog.WarnContext(ctx, "moved")`) {
		t.Errorf("the unused wrapper import should be dropped and slog added:\n%s", other)
	}
}

func TestFixRuleNarrowsFix(t *testing.T) {
	root := writeTree(t, map[string]string{"a/a.go": `package a

import (
	"context"
	"log/slog"
)

func F(ctx context.Context) { slog.Info("x") }
`})
	res, err := Lint(root, Options{Fix: true, FixFilter: func(finding.Finding) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	assertHits(t, res, RuleLogWithoutCtx+" a/a.go:8")
	if len(res.Fixed) != 0 {
		t.Fatalf("FixFilter declined, but fixed %+v", res.Fixed)
	}
}

func TestRawHTTPClient(t *testing.T) {
	src := `package out

import (
	"net/http"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

type Infra struct{ base http.RoundTripper }

func (i *Infra) transport() http.RoundTripper { return i.base }

func clients() {
	_ = &http.Client{Timeout: time.Second}                                      // 15
	_ = &http.Client{Transport: observe.NewHTTPTransport(http.DefaultTransport)} // traced
	i := &Infra{}
	_ = &http.Client{Transport: i.transport()} // opaque: trusted
	t := &http.Transport{MaxIdleConns: 1}
	_ = &http.Client{Transport: t}                    // 20
	_ = http.Client{Transport: http.DefaultTransport} // 21
	_ = new(http.Client)                              // 22
	_, _ = http.Get("http://x")                       // 23
	_, _ = http.DefaultClient.Do(nil)                 // 24
	_ = observe.NewHTTPClient(observe.HTTPClientOptions{})
}

func init() {
	http.DefaultClient = observe.NewHTTPClient(observe.HTTPClientOptions{}) // installing a traced default
}

var pkgLevel = &http.Client{} // 32
`
	root := writeTree(t, map[string]string{"internal/out/out.go": src})
	res, err := Lint(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := RuleRawHTTPClient + " internal/out/out.go:"
	assertHits(t, res, f+"15", f+"20", f+"21", f+"22", f+"23", f+"24", f+"32")
}

func TestRawSQLOpen(t *testing.T) {
	src := `package db

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
)

func open(ctx context.Context, dsn string) {
	_, _ = sql.Open("pgx", dsn)    // 12
	_, _ = dbtrace.Open("pgx", dsn) // traced
	_, _ = pgxpool.New(ctx, dsn)   // 14
}

func traced(ctx context.Context, dsn string) {
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.Tracer = nil
	_, _ = pgxpool.NewWithConfig(ctx, cfg) // a Tracer is set: trusted
}
`
	root := writeTree(t, map[string]string{"internal/db/open.go": src})
	res, err := Lint(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := RuleRawSQLOpen + " internal/db/open.go:"
	assertHits(t, res, f+"12", f+"14")
}

// The remedy for a raw pgx pool names a tracer forge ships. It once named
// otelpgx, which no project has in go.mod, and an agent hand-wrote a
// pgx.QueryTracer to follow it.
func TestRawSQLOpenPgxRemedyIsShippedByForge(t *testing.T) {
	src := `package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func open(ctx context.Context, dsn string) { _, _ = pgxpool.New(ctx, dsn) }
`
	root := writeTree(t, map[string]string{"internal/db/open.go": src})
	res, err := Lint(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want one finding, got %+v", res.Findings)
	}
	r := res.Findings[0].Remediation
	if strings.Contains(r, "otelpgx") {
		t.Errorf("remediation names otelpgx, which forge does not ship or pin: %s", r)
	}
	if !strings.Contains(r, "dbtrace.NewPgxTracer()") {
		t.Errorf("remediation does not name dbtrace.NewPgxTracer(): %s", r)
	}
}

// A pool opened only to look its driver up runs no statement: it is how a
// traced pool is built from a driver name (dbtrace.Open), not a defect.
func TestRawSQLOpenDriverProbe(t *testing.T) {
	src := `package db

import (
	"database/sql"
	"database/sql/driver"
)

func lookup(name, dsn string) (driver.Driver, error) {
	probe, err := sql.Open(name, dsn) // probe: only .Driver and .Close
	if err != nil {
		return nil, err
	}
	d := probe.Driver()
	_ = probe.Close()
	return d, nil
}

func leaks(name, dsn string) (*sql.DB, error) {
	db, err := sql.Open(name, dsn) // 19: returned, so it runs queries
	_ = db.Driver()
	return db, err
}

func discards(name, dsn string) {
	_, _ = sql.Open(name, dsn) // 25
}
`
	root := writeTree(t, map[string]string{"internal/db/open.go": src})
	res, err := Lint(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := RuleRawSQLOpen + " internal/db/open.go:"
	assertHits(t, res, f+"19", f+"25")
}

// A file that exports telemetry over OTLP keeps its client untraced on
// purpose; the same client anywhere else is flagged.
func TestRawHTTPClientTelemetryExporter(t *testing.T) {
	exporter := `package export

import (
	"net/http"

	"go.opentelemetry.io/collector/pdata/pprofile/pprofileotlp"
)

var _ = pprofileotlp.NewExportRequest

var client = &http.Client{}
`
	plain := "package export\n\nimport \"net/http\"\n\nvar client2 = &http.Client{}\n"
	root := writeTree(t, map[string]string{"internal/export/otlp.go": exporter, "internal/export/plain.go": plain})
	res, err := Lint(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertHits(t, res, RuleRawHTTPClient+" internal/export/plain.go:5")
}

// LibraryOnly lints only what another module can import.
func TestLibraryOnly(t *testing.T) {
	bad := "package %s\n\nimport \"net/http\"\n\nvar c = &http.Client{}\n"
	pkg := func(name string) string { return strings.Replace(bad, "%s", name, 1) }
	root := writeTree(t, map[string]string{
		"internal/cli/cli.go":        pkg("cli"),
		"cmd/tool/main.go":           pkg("main"),
		"tools/build/main.go":        pkg("main"),
		"pkg/client/client.go":       pkg("client"),
		"pkg/client/internal/x/x.go": pkg("x"),
		"root.go":                    pkg("app"),
	})
	res, err := Lint(root, Options{LibraryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	f := RuleRawHTTPClient + " "
	assertHits(t, res, f+"pkg/client/client.go:5", f+"pkg/client/internal/x/x.go:5", f+"root.go:5")
	if res, _ := Lint(root, Options{}); len(res.Findings) != 6 {
		t.Errorf("without LibraryOnly: %v, want all six", hits(res))
	}
}

func TestContractUninstrumented(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/billing/contract.go": "package billing\n\ntype Service interface{ Charge() }\n\ntype Deps struct{}\n\nfunc New(Deps) Service { return nil }\n",
		"internal/billing/middleware_gen.go": "// Code generated by forge. DO NOT EDIT.\n\npackage billing\n\n" +
			"func NewServiceWithForgeMiddleware(s Service) Service { return s }\n",
		"internal/billing/internal_use.go": "package billing\n\nfunc self() Service { return New(Deps{}) }\n",
		"internal/plain/plain.go":          "package plain\n\nfunc New() int { return 0 }\n",
		// Instrumented, but its decorator is not generated yet: compose.go
		// would not wrap it either, so it is not flagged.
		"internal/fresh/contract.go": "package fresh\n\ntype Service interface{ Do() }\n\ntype Deps struct{}\n\nfunc New(Deps) Service { return nil }\n",
		"internal/app/providers.go": `package app

import (
	"example.com/app/internal/billing"
	"example.com/app/internal/fresh"
	"example.com/app/internal/plain"
)

func wire() error {
	_ = billing.New(billing.Deps{}) // 10: unwrapped
	_ = billing.NewServiceWithForgeMiddleware(billing.New(billing.Deps{}))
	inst := billing.New(billing.Deps{})
	_ = billing.NewServiceWithForgeMiddleware(inst)
	_ = plain.New() // not instrumented
	_ = fresh.New(fresh.Deps{}) // no decorator generated yet
	return nil
}
`,
		"internal/app/providers_test.go": "package app\n\nimport \"example.com/app/internal/billing\"\n\nvar _ = billing.New(billing.Deps{})\n",
	})
	instrumented := func(dir string) bool { return filepath.Base(dir) == "billing" || filepath.Base(dir) == "fresh" }
	res, err := Lint(root, Options{Instrumented: instrumented})
	if err != nil {
		t.Fatal(err)
	}
	assertHits(t, res, RuleContractUninstrumented+" internal/app/providers.go:10")
	if r := res.Findings[0].Remediation; !strings.Contains(r, "billing.NewServiceWithForgeMiddleware(billing.New(…))") {
		t.Errorf("remediation does not name the decorator: %s", r)
	}
	if res, _ := Lint(root, Options{}); len(res.Findings) != 0 {
		t.Errorf("no instrument decision supplied, yet: %v", hits(res))
	}
}

func TestSkipsGeneratedTestsAndNestedModules(t *testing.T) {
	bad := "package x\n\nimport \"database/sql\"\n\nvar _, _ = sql.Open(\"pgx\", \"\")\n"
	root := writeTree(t, map[string]string{
		"x/x_test.go":         bad,
		"x/x_gen.go":          bad,
		"x/header.go":         "// Code generated by protoc-gen-x. DO NOT EDIT.\n\n" + bad,
		"gen/y/y.go":          bad,
		"x/testdata/z.go":     bad,
		"tools/go.mod":        "module tools\n",
		"tools/t.go":          bad,
		"node_modules/n/n.go": bad,
	})
	res, err := Lint(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertHits(t, res)
}

// A construction wired outside compose.go — providers.go, a *_wire.go file —
// in every position a call can take, is rewritten to the decorated twin the
// package's middleware_gen.go declares, with no change to any type; a
// NewInstrumented whose package opted out is rewritten back.
func TestContractUninstrumentedFixesProvidersSites(t *testing.T) {
	decorated := func(pkg, results string) map[string]string {
		ret := "Service"
		body := "\treturn NewServiceWithForgeMiddleware(New(deps))\n"
		if results == "fallible" {
			ret = "(Service, error)"
			body = "\ts, err := New(deps)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn NewServiceWithForgeMiddleware(s), nil\n"
		}
		return map[string]string{
			"internal/" + pkg + "/contract.go": "package " + pkg + "\n\ntype Service interface{ Do() }\n\ntype Deps struct{}\n\nfunc New(Deps) " + ret + " { panic(0) }\n",
			"internal/" + pkg + "/middleware_gen.go": "// Code generated by forge. DO NOT EDIT.\n\npackage " + pkg + "\n\n" +
				"func NewServiceWithForgeMiddleware(s Service) Service { return s }\n\n" +
				"func NewInstrumented(deps Deps) " + ret + " {\n" + body + "}\n",
		}
	}
	files := map[string]string{
		"internal/optedout/contract.go": "package optedout\n\ntype Service interface{ Do() }\n\nfunc New() Service { return nil }\n",
		"internal/app/providers.go": `package app

import (
	"example.com/app/internal/billing"
	"example.com/app/internal/ledger"
	"example.com/app/internal/optedout"
)

type Infra struct {
	Ledger ledger.Service
}

type holder struct{ L ledger.Service }

func wire(infra *Infra) (billing.Service, error) {
	bill, err := billing.New(billing.Deps{})
	if err != nil {
		return nil, err
	}
	_ = bill
	infra.Ledger = ledger.New(ledger.Deps{})
	_ = holder{L: ledger.New(ledger.Deps{})}
	_ = optedout.NewInstrumented()
	return billing.New(billing.Deps{})
}
`,
	}
	for k, v := range decorated("billing", "fallible") {
		files[k] = v
	}
	for k, v := range decorated("ledger", "single") {
		files[k] = v
	}
	root := writeTree(t, files)
	instrumented := func(dir string) bool { return filepath.Base(dir) != "optedout" }

	res, err := Lint(root, Options{Instrumented: instrumented})
	if err != nil {
		t.Fatal(err)
	}
	f := RuleContractUninstrumented + " internal/app/providers.go:"
	assertHits(t, res, f+"16", f+"21", f+"22", f+"23", f+"24")

	res, err = Lint(root, Options{Instrumented: instrumented, Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 || len(res.Fixed) != 1 || res.Fixed[0].Count != 5 {
		t.Fatalf("fix: findings %v, fixed %+v; want all 5 sites rewritten", hits(res), res.Fixed)
	}
	got, _ := os.ReadFile(filepath.Join(root, "internal/app/providers.go"))
	for _, want := range []string{
		"bill, err := billing.NewInstrumented(billing.Deps{})",
		"infra.Ledger = ledger.NewInstrumented(ledger.Deps{})",
		"holder{L: ledger.NewInstrumented(ledger.Deps{})}",
		"_ = optedout.New()",
		"return billing.NewInstrumented(billing.Deps{})",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("providers.go lacks %q:\n%s", want, got)
		}
	}
	// Idempotent, and nothing left for the rule.
	if res, _ := Lint(root, Options{Instrumented: instrumented, Fix: true}); len(res.Findings)+len(res.Fixed) != 0 {
		t.Fatalf("second run not clean: %+v", res)
	}
}
