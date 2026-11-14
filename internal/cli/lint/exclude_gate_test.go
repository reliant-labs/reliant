package lint

import (
	"context"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/contract"
)

// The exclusion gate (internal/linter/contract/exclude_directive.go), driven
// end to end through the in-process contract lane `forge lint` runs. One
// self-contained module holds a package per verdict, so each assertion below
// names the package it is about and fails on its own.
//
// Stdlib only (net/http, database/sql), so the fixture loads offline.
func writeExcludeGateFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",

		// A bare marker: excluded, but no reason.
		"internal/bare/bare.go": `// Package bare is pure.
//
//forge:exclude-contract
package bare

func Double(n int) int { return n * 2 }
`,
		// A reasoned marker on a pure package: accepted.
		"internal/purefmt/purefmt.go": `// Package purefmt is pure string formatting.
//
//forge:exclude-contract: pure string formatting, no I/O, time or state
package purefmt

import "strings"

func Title(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
`,
		// A reasoned marker on an outbound HTTP client: refused.
		"internal/httpclient/client.go": `// Package httpclient calls a vendor.
//
//forge:exclude-contract: just a thin client
package httpclient

import (
	"context"
	"net/http"
)

type Client struct{ hc *http.Client }

func New() *Client { return &Client{hc: &http.Client{}} }

func (c *Client) Ping(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
		// A reasoned marker on a database-backed store: refused.
		"internal/sqlstore/store.go": `// Package sqlstore persists widgets.
//
//forge:exclude-contract: a small store
package sqlstore

import (
	"context"
	"database/sql"
)

type Store struct{ db *sql.DB }

func (s *Store) Count(ctx context.Context) (n int, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM widgets").Scan(&n)
	return n, err
}
`,
		// Names *sql.DB in a type but never calls it: NOT I/O.
		"internal/sqltypes/types.go": `// Package sqltypes only names database types.
//
//forge:exclude-contract: configuration types, no calls
package sqltypes

import "database/sql"

type Config struct{ DB *sql.DB }
`,
		// An exported interface with two implementations, one in-package:
		// refused.
		"internal/stores/stores.go": `// Package stores has pluggable backends.
//
//forge:exclude-contract: simple
package stores

type Store interface{ Get(key string) string }

type Memory struct{}

func (Memory) Get(string) string { return "" }
`,
		"internal/diskstore/disk.go": `package diskstore

type Disk struct{}

func (Disk) Get(string) string { return "disk" }
`,
		// A consumer-side seam: declared here, implemented only elsewhere
		// (twice). The house rule — declare interfaces at the consumer —
		// must not trip the gate.
		"internal/consumer/consumer.go": `// Package consumer declares the seam it needs.
//
//forge:exclude-contract: pure orchestration over a caller-supplied getter
package consumer

type Getter interface{ Get(key string) string }

func Lookup(g Getter, k string) string { return g.Get(k) }
`,
		// The reliant accountlimits shape: a seam declared where it is
		// consumed (the Fetcher's Tokens field), with an in-package default
		// AND a second implementation elsewhere. Two implementations, one
		// in-package — but a seam the package NEEDS is not a contract it
		// offers, so its exclusion stands.
		"internal/tokens/tokens.go": `// Package tokens polls usage with a caller-supplied token source.
//
//forge:exclude-contract: a background poller; callers supply the token source
package tokens

import "context"

type TokenSource interface {
	AccessToken(ctx context.Context, accountID string) (string, error)
}

type StoredTokens struct{}

func (StoredTokens) AccessToken(context.Context, string) (string, error) { return "stored", nil }

type Fetcher struct{ Tokens TokenSource }

func (f *Fetcher) Fetch(ctx context.Context, id string) (string, error) {
	return f.Tokens.AccessToken(ctx, id)
}
`,
		"internal/refresher/refresher.go": `package refresher

import "context"

type RefreshingTokens struct{}

func (RefreshingTokens) AccessToken(context.Context, string) (string, error) { return "fresh", nil }
`,
		// A consumer-declared seam implemented by two OTHER packages and
		// none here: the house rule in its plainest form.
		"internal/poller/poller.go": `// Package poller runs on a caller-supplied clock.
//
//forge:exclude-contract: a background loop over a caller-supplied clock
package poller

import "time"

type Clock interface{ Now() time.Time }

type Poller struct{ Clock Clock }

func (p *Poller) Due(at time.Time) bool { return !p.Clock.Now().Before(at) }
`,
		"internal/realclock/real.go": `package realclock

import "time"

type Real struct{}

func (Real) Now() time.Time { return time.Now() }
`,
		"internal/fixedclock/fixed.go": `package fixedclock

import "time"

type Fixed struct{ At time.Time }

func (f Fixed) Now() time.Time { return f.At }
`,
		// A PROVIDER whose own decorator holds the interface and whose
		// constructor takes and returns it: neither is consumption, so the
		// two-implementation interface is still the contract and refused.
		"internal/decorated/decorated.go": `// Package decorated provides a store and its logging decorator.
//
//forge:exclude-contract: simple
package decorated

type Store interface{ Get(key string) string }

type Memory struct{}

func (Memory) Get(string) string { return "" }

type logging struct{ next Store }

func (l logging) Get(k string) string { return l.next.Get(k) }

func WithLogging(s Store) Store { return logging{next: s} }
`,
		// A strategy registry: multi-impl by design, exempt.
		"internal/algos/algos.go": `// Package algos is a strategy registry.
//
//forge:exclude-contract: strategy registry, one constructor per algorithm
package algos

type Strategy interface{ Name() string }

var registry = map[string]Strategy{}

func Register(s Strategy) { registry[s.Name()] = s }

type Mean struct{}

func (Mean) Name() string { return "mean" }

type Median struct{}

func (Median) Name() string { return "median" }
`,
		// The same registry as a method on a Registry type — the idiomatic
		// spelling (deploytarget.Registry.Register).
		"internal/providers/providers.go": `// Package providers dispatches to pluggable providers.
//
//forge:exclude-contract: provider registry, each provider registers itself
package providers

type Provider interface{ Name() string }

type Registry struct{ byName map[string]Provider }

func (r *Registry) Register(p Provider) { r.byName[p.Name()] = p }

type Local struct{}

func (Local) Name() string { return "local" }

type Remote struct{}

func (Remote) Name() string { return "remote" }
`,
		// Two refusals on one marker, each with its own stacked allowance.
		"internal/natsish/natsish.go": `// Package natsish is I/O AND owns a two-impl interface.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: reviewed I/O allowance
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: reviewed multi-impl allowance
//forge:exclude-contract: stacked allowances
package natsish

import "net/http"

type Runner interface{ Run() error }

type A struct{}

func (A) Run() error { _, err := http.Get("http://x.invalid"); return err }

type B struct{}

func (B) Run() error { return nil }
`,
		// Only ONE of the two refusals is allowed: the other must still fire.
		"internal/halfallowed/half.go": `// Package halfallowed allows I/O but not its multi-impl interface.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: reviewed I/O allowance
//forge:exclude-contract: half allowed
package halfallowed

import "net/http"

type Runner interface{ Run() error }

type A struct{}

func (A) Run() error { _, err := http.Get("http://x.invalid"); return err }

type B struct{}

func (B) Run() error { return nil }
`,
		// Test-support: non-test source imports "testing", so standing up an
		// HTTP server here is its job.
		"internal/fakevendor/fake.go": `// Package fakevendor is a test server.
//
//forge:exclude-contract: test-only fake vendor server
package fakevendor

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func Start(t *testing.T) string {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
	}
	return srv.URL
}
`,
		// The reliant preview-forwarder shape: a net.Dialer to the user's
		// own dev server on 127.0.0.1 then ::1, plus an http.Get of a
		// constant localhost URL. Loopback cannot leave the machine, so it
		// is not an outbound boundary.
		"internal/loopback/forward.go": `// Package loopback forwards to a local dev server.
//
//forge:exclude-contract: preview forwarder over the user's own loopback dev server
package loopback

import (
	"context"
	"net"
	"net/http"
)

const devHost = "127.0.0.1"

func Dial(ctx context.Context, port string) (net.Conn, error) {
	d := &net.Dialer{}
	if c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(devHost, port)); err == nil {
		return c, nil
	}
	if c, err := d.DialContext(ctx, "tcp6", net.JoinHostPort("::1", port)); err == nil {
		return c, nil
	}
	return net.Dial("tcp", "localhost:9191")
}

func Health() error {
	resp, err := http.Get("http://localhost:3000/health")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
		// The same dial with a host the type checker cannot see: still I/O,
		// and the refusal says how to make a real loopback dial visible.
		"internal/dynhost/dial.go": `// Package dynhost dials a caller-chosen host.
//
//forge:exclude-contract: forwards to a configured host
package dynhost

import (
	"context"
	"net"
)

func Dial(ctx context.Context, host, port string) (net.Conn, error) {
	d := &net.Dialer{}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
}
`,
		// Mixed spellings, as reliant writes them: a SPACED marker and an
		// unspaced allowance. gofmt moves the unspaced directive to the end
		// of the doc comment, BELOW the marker; the allowance must still
		// reach it. This file is the gofmt OUTPUT, not the input.
		"internal/reflowed/client.go": `// Package reflowed probes a vendor.
//
// forge:exclude-contract: legacy probe, conversion tracked in #7
//
// More prose the author wrote after the marker.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: health probe only, being converted in #7
package reflowed

import "net/http"

func Probe(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
		// A justified exception, written with the ordinary suppression
		// mechanism on the marker's line.
		"internal/allowed/client.go": `// Package allowed is an I/O package with a reviewed allowance.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: health probe only, being converted in #123
//forge:exclude-contract: legacy probe
package allowed

import "net/http"

func Probe(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// excludeGateDiags runs the in-process contract lane over the fixture and
// returns its exclusion-gate diagnostics keyed by package directory.
func excludeGateDiags(t *testing.T) map[string][]contractDiagnostic {
	t.Helper()
	return excludeGateDiagsWith(t, contract.ExcludeGateOptions{})
}

func excludeGateDiagsWith(t *testing.T, gate contract.ExcludeGateOptions) map[string][]contractDiagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("loads a module with go/packages (type-checks net/http); skipped under -short")
	}
	t.Chdir(writeExcludeGateFixture(t))
	diags, err := runContractAnalysisInProcess(context.Background(), []string{"./..."}, nil, gate)
	if err != nil {
		t.Fatalf("in-process contract analysis: %v", err)
	}
	byPkg := map[string][]contractDiagnostic{}
	for _, d := range diags {
		if !strings.HasPrefix(d.Analyzer, "forge-exclude-contract") && !strings.HasPrefix(d.Analyzer, "forge-suppression") {
			continue
		}
		pkg := filepath.Base(filepath.Dir(d.Pos.Filename))
		byPkg[pkg] = append(byPkg[pkg], d)
	}
	return byPkg
}

func rulesOf(ds []contractDiagnostic) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Analyzer)
	}
	return out
}

func hasRule(ds []contractDiagnostic, rule string) bool {
	for _, d := range ds {
		if d.Analyzer == rule {
			return true
		}
	}
	return false
}

func TestExcludeGate(t *testing.T) {
	byPkg := excludeGateDiags(t)

	// A bare marker is refused, and the refusal says what is missing.
	t.Run("bare marker is refused", func(t *testing.T) {
		ds := byPkg["bare"]
		if !hasRule(ds, "forge-exclude-contract-reason") {
			t.Fatalf("bare //forge:exclude-contract was not refused: %v", rulesOf(ds))
		}
		if !strings.Contains(ds[0].Message, "requires a reason") || !strings.Contains(ds[0].FixHint, "//forge:exclude-contract: <why>") {
			t.Errorf("refusal does not name the missing reason: %+v", ds[0])
		}
		if ds[0].Warning {
			t.Errorf("a bare marker must gate (error), got warning")
		}
	})

	// A reasoned marker on a pure package is accepted.
	t.Run("reasoned marker on a pure package is accepted", func(t *testing.T) {
		for _, pkg := range []string{"purefmt", "sqltypes"} {
			if ds := byPkg[pkg]; len(ds) != 0 {
				t.Errorf("%s: pure package refused: %v", pkg, ds)
			}
		}
	})

	// A reasoned marker on an I/O package is refused, naming the signal and
	// the adapter fix.
	t.Run("reasoned marker on an I/O package is refused", func(t *testing.T) {
		for pkg, signal := range map[string]string{
			"httpclient": "HTTP client call (net/http)",
			"sqlstore":   "database call (database/sql)",
		} {
			ds := byPkg[pkg]
			if !hasRule(ds, "forge-exclude-contract-outbound-io") {
				t.Errorf("%s: I/O package not refused: %v", pkg, rulesOf(ds))
				continue
			}
			if hasRule(ds, "forge-exclude-contract-reason") {
				t.Errorf("%s: reasoned marker reported as bare", pkg)
			}
			for _, d := range ds {
				if d.Analyzer == "forge-exclude-contract-outbound-io" &&
					(!strings.Contains(d.Message, signal) || !strings.Contains(d.FixHint, "forge:outbound-io")) {
					t.Errorf("%s: refusal must name %q and the adapter fix: %+v", pkg, signal, d)
				}
			}
		}
	})

	// A two-implementation interface package is refused, naming both.
	t.Run("two-implementation interface package is refused", func(t *testing.T) {
		ds := byPkg["stores"]
		if !hasRule(ds, "forge-exclude-contract-multi-impl") {
			t.Fatalf("multi-impl package not refused: %v", rulesOf(ds))
		}
		for _, d := range ds {
			if d.Analyzer == "forge-exclude-contract-multi-impl" &&
				(!strings.Contains(d.Message, "Memory") || !strings.Contains(d.Message, "diskstore.Disk")) {
				t.Errorf("refusal must name both implementations: %s", d.Message)
			}
		}
	})

	// The low-false-positive boundaries.
	t.Run("consumer-side seam, registry and test-support are not refused", func(t *testing.T) {
		for _, pkg := range []string{"consumer", "algos", "providers", "fakevendor"} {
			if ds := byPkg[pkg]; len(ds) != 0 {
				t.Errorf("%s: refused, but it is a legitimate exclusion: %v", pkg, ds)
			}
		}
	})

	t.Run("a reasoned lint-disable is the narrow allowance", func(t *testing.T) {
		if ds := byPkg["allowed"]; len(ds) != 0 {
			t.Errorf("a reasoned forge:lint-disable-next-line did not suppress the refusal: %v", ds)
		}
	})

	// Two refusals on one marker need two allowances, and the second one
	// necessarily sits between the first and the marker.
	t.Run("stacked allowances each reach the marker", func(t *testing.T) {
		if ds := byPkg["natsish"]; len(ds) != 0 {
			t.Errorf("stacked allowances did not both apply: %v", rulesOf(ds))
		}
		ds := byPkg["halfallowed"]
		if !hasRule(ds, "forge-exclude-contract-multi-impl") || hasRule(ds, "forge-exclude-contract-outbound-io") {
			t.Errorf("an allowance for one rule must not silence the other: %v", rulesOf(ds))
		}
	})
}

// A seam a package declares where it CONSUMES it is not that package's
// contract, even when the package ships one implementation and another
// package a second. Red before: reliant's accountlimits was refused for
// TokenSource (its Fetcher's dependency, implemented by an in-package
// default and by llm/drivers) and told to move it into a contract.go.
func TestExcludeGate_ConsumerSeamIsNotTheContract(t *testing.T) {
	byPkg := excludeGateDiags(t)

	for _, pkg := range []string{"tokens", "poller"} {
		if ds := byPkg[pkg]; len(ds) != 0 {
			t.Errorf("%s: a consumer-declared seam was refused as the package's contract: %v", pkg, ds)
		}
	}
	// A provider's decorator field and constructor parameter are its own
	// shapes, not consumption: the gate still refuses it.
	ds := byPkg["decorated"]
	if !hasRule(ds, "forge-exclude-contract-multi-impl") {
		t.Fatalf("a provider with a decorator escaped the multi-impl refusal: %v", rulesOf(ds))
	}
}

// A dial whose target is statically loopback is not outbound I/O; a dial
// whose host the gate cannot see still is, and the refusal says how to make
// a real loopback dial visible. Red before the exemption: the loopback
// package was refused for "a network dial (net)".
func TestExcludeGate_LoopbackLiteralIsNotOutbound(t *testing.T) {
	byPkg := excludeGateDiags(t)

	if ds := byPkg["loopback"]; len(ds) != 0 {
		t.Errorf("a constant-loopback dial/GET was refused as outbound I/O: %v", ds)
	}
	ds := byPkg["dynhost"]
	if !hasRule(ds, "forge-exclude-contract-outbound-io") {
		t.Fatalf("a dial to a caller-chosen host must still be refused: %v", rulesOf(ds))
	}
	for _, d := range ds {
		if d.Analyzer == "forge-exclude-contract-outbound-io" && !strings.Contains(d.FixHint, "LOOPBACK") {
			t.Errorf("a dynamic-target refusal must say how to exempt a genuine loopback dial: %s", d.FixHint)
		}
	}
}

// gofmt moves `//forge:` directive lines to the END of a doc comment. With
// mixed spellings (a spaced marker, an unspaced allowance) that puts the
// allowance BELOW the marker, and it must still apply. Red before: the gate
// only looked for allowances directly above the marker.
func TestExcludeGate_AllowanceBelowMarkerAfterGofmtReflow(t *testing.T) {
	byPkg := excludeGateDiags(t)
	if ds := byPkg["reflowed"]; len(ds) != 0 {
		t.Errorf("an allowance gofmt reflowed below the marker no longer applied: %v", ds)
	}
}

// With no forge.yaml there is no codegen, so the refusals whose payoff is a
// generated mock and decorator report as WARNINGS that say why; the reason
// requirement keeps gating. Red before: no such option, every refusal gated.
func TestExcludeGate_CodegenUnavailableDowngradesRefusals(t *testing.T) {
	byPkg := excludeGateDiagsWith(t, contract.ExcludeGateOptions{CodegenUnavailable: true})

	for _, pkg := range []string{"httpclient", "sqlstore", "stores", "dynhost"} {
		ds := byPkg[pkg]
		if len(ds) == 0 {
			t.Errorf("%s: refusal disappeared instead of being downgraded", pkg)
			continue
		}
		for _, d := range ds {
			if !d.Warning {
				t.Errorf("%s: %s should be a warning without codegen, got error", pkg, d.Analyzer)
			}
			if !strings.Contains(d.FixHint, "no forge.yaml") {
				t.Errorf("%s: the downgraded refusal must say why: %s", pkg, d.FixHint)
			}
		}
	}
	ds := byPkg["bare"]
	if !hasRule(ds, "forge-exclude-contract-reason") || ds[0].Warning {
		t.Errorf("a bare marker must still gate without codegen: %+v", ds)
	}
}

// And the grading is the CLI's decision: no forge.yaml and no --strict.
func TestCodegenUnavailable(t *testing.T) {
	if !codegenUnavailable(nil, false) {
		t.Error("no forge.yaml, no --strict: codegen is unavailable")
	}
	if codegenUnavailable(nil, true) {
		t.Error("--strict keeps the contract refusals gating")
	}
	if codegenUnavailable(&config.ProjectConfig{}, false) {
		t.Error("a forge project has codegen")
	}
}

// TestExcludeGate_AllowanceSurvivesGofmt pins the SPELLING the skill teaches
// for the allowance. gofmt moves `//forge:x` directive lines to the end of a
// doc comment and separates them from prose with a blank `//` — a SPACED
// `// forge:lint-disable-next-line` counts as prose, so gofmt would put a
// blank line between it and the marker and the "next line" it suppresses
// would no longer be the marker. The unspaced form stays glued to it.
func TestExcludeGate_AllowanceSurvivesGofmt(t *testing.T) {
	src := []byte("// Package allowed probes.\n//\n" +
		"//forge:lint-disable-next-line forge-exclude-contract-outbound-io: reviewed\n" +
		"//forge:exclude-contract: legacy probe\n//\n// Trailing prose.\npackage allowed\n")
	out, err := format.Source(src)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(out), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "//forge:lint-disable-next-line") {
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "//forge:exclude-contract:") {
				t.Fatalf("gofmt separated the allowance from the marker:\n%s", out)
			}
			return
		}
	}
	t.Fatalf("allowance line lost:\n%s", out)
}
