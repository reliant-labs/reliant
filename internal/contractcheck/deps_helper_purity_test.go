package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintDepsAreInterfaces_HelperCallDoesNotDisqualifyData is the
// regression for a false positive measured on control-plane, at
// internal/operators/workspace/operator.go:78.
//
// `*config.WorkspaceConfig` is a YAML-deserialized bag of storage defaults
// and probe shapes — the exact case the data-struct exemption exists for,
// and the case the exemption's own comment names. It fired anyway, for one
// reason: one of its accessors called a PACKAGE-LEVEL HELPER, and the
// syntactic purity walk answered "a call I cannot follow is impure".
// Inlining the helper made the finding disappear.
//
// That is the tell that the rule was measuring the wrong thing. Whether a
// pure expression has been given a name says nothing about whether the type
// is data, and a rule that demands authors inline their helpers to satisfy
// it is one they will suppress instead.
//
// A call through a FIELD is genuinely unfollowable — `c.pool.Query(q)`
// reaches whatever the field holds, and no syntax distinguishes a map from a
// database. A call to a package-level func reaches exactly one declaration,
// which is available to read. So it is read, recursively, with the same walk.
//
// The fixture is a faithful reduction of the real type: a tier ladder with a
// per-config override, falling back to a package-level helper over package
// defaults, which itself falls back to a package const — and a second helper
// called by the first, so the recursion is exercised rather than just the
// one hop.
func TestLintDepsAreInterfaces_HelperCallDoesNotDisqualifyData(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	cfgDir := filepath.Join(tmp, "internal", "config")
	must(t, mkdirAll(cfgDir))
	must(t, writeFile(filepath.Join(cfgDir, "workspace_config.go"), `package config

const (
	defaultStorageSize = "20Gi"
	largeStorageSize   = "80Gi"
)

// WorkspaceConfig is DATA. Its accessors index its own maps and fall back
// through package-level helpers — the shape that used to fire.
type WorkspaceConfig struct {
	DefaultStorageSize string
	DaemonStorageSizes map[string]string
}

// StorageSizeForTier calls a package-level helper for its fallback. This
// single call is what disqualified the whole type before the fix.
func (c *WorkspaceConfig) StorageSizeForTier(tier string) string {
	if size, ok := c.DaemonStorageSizes[tier]; ok && size != "" {
		return size
	}
	return DaemonStorageSizeForTier(tier)
}

// TotalTierSize exercises the RECURSION: a helper that calls another helper.
func (c *WorkspaceConfig) TotalTierSize(tier string) string {
	return combined(tier)
}

// DaemonStorageSizeForTier is a package-level func over package consts.
// Pure — nothing here reaches outside the package.
func DaemonStorageSizeForTier(tier string) string {
	if tier == "large" {
		return largeStorageSize
	}
	return defaultStorageSize
}

// combined calls DaemonStorageSizeForTier, so judging it requires following
// one helper into another.
func combined(tier string) string {
	return DaemonStorageSizeForTier(tier) + "/" + defaultStorageSize
}
`))

	opDir := filepath.Join(tmp, "internal", "workspaceop")
	must(t, mkdirAll(opDir))
	must(t, writeFile(filepath.Join(opDir, "contract.go"), `package workspaceop

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(opDir, "operator.go"), `package workspaceop

import "example.com/app/internal/config"

type Deps struct {
	WorkspaceConfig *config.WorkspaceConfig // config data whose accessors call helpers — silent
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := findingsForRule(fs, string(RuleDepsAreInterfaces)); len(got) != 0 {
		t.Fatalf("a config struct is not disqualified from the data exemption by calling a "+
			"package-level helper — the helper is right there and it is pure. Inlining it "+
			"must not change the verdict. got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestLintDepsAreInterfaces_ImpureHelperStillFires is the other half, and
// the one that decides whether following helpers is a discriminator or a
// loophole.
//
// Reading a helper's body is only safe if an IMPURE helper still condemns
// its caller. Otherwise the fix would hand every collaborator a trivial
// escape: move the I/O one frame down into a package-level func and the type
// is suddenly "data".
//
// Each type below reaches outside itself through a helper, and every one
// must still fire:
//
//   - Store — its accessor calls a helper that reaches a package-level VAR
//     holding a live connection. Package state is how a method reaches a
//     singleton, and moving the read into a helper does not change that.
//   - Fetcher — its helper calls into an I/O-boundary package (net/http).
//     Those bodies are thin wrappers that LOOK pure at the Go level, which
//     is exactly why the boundary is rejected at the call site rather than
//     read.
//   - Looper — mutually recursive helpers. A cycle is not a config
//     accessor's shape, and the guard answers impure rather than recursing
//     forever; this test is also what would hang if the guard regressed.
//   - Spawner — its helper starts a goroutine. Purity of the CALLER's own
//     body is not enough when the callee does work.
func TestLintDepsAreInterfaces_ImpureHelperStillFires(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	cfgDir := filepath.Join(tmp, "internal", "config")
	must(t, mkdirAll(cfgDir))
	must(t, writeFile(filepath.Join(cfgDir, "config.go"), `package config

import "net/http"

var sharedConn = map[string]string{}

// Store's accessor is pure in its own body; its helper reads package var
// state — a singleton reached one frame down.
type Store struct{ prefix string }

func (s *Store) Lookup(k string) string { return readShared(s.prefix + k) }

func readShared(k string) string { return sharedConn[k] }

// Fetcher's helper calls into net/http. An I/O boundary is rejected at the
// call site without reading it: those bodies look pure and are not.
type Fetcher struct{ base string }

func (f *Fetcher) URL(p string) string { return probe(f.base + p) }

func probe(u string) string {
	_, _ = http.Get(u)
	return u
}

// Looper's helpers are mutually recursive. The recursion guard must answer
// impure rather than recurse forever.
type Looper struct{ seed string }

func (l *Looper) Value(k string) string { return ping(k) }

func ping(k string) string { return pong(k) }
func pong(k string) string { return ping(k) }

// Spawner's helper starts a goroutine — work, one frame down.
type Spawner struct{ name string }

func (s *Spawner) Name(k string) string { return kick(k) }

func kick(k string) string {
	go func() { _ = k }()
	return k
}
`))

	appDir := filepath.Join(tmp, "internal", "orders")
	must(t, mkdirAll(appDir))
	must(t, writeFile(filepath.Join(appDir, "contract.go"), `package orders

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(appDir, "service.go"), `package orders

import "example.com/app/internal/config"

type Deps struct {
	Store   *config.Store   // helper reads package var state
	Fetcher *config.Fetcher // helper calls an I/O-boundary package
	Looper  *config.Looper  // mutually recursive helpers
	Spawner *config.Spawner // helper starts a goroutine
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 4 {
		t.Fatalf("following a helper must not become an escape hatch — a collaborator cannot "+
			"launder its I/O by moving it one frame down. expected 4, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
	var joined string
	for _, f := range got {
		joined += f.Message + "\n"
	}
	for _, want := range []string{`"Store"`, `"Fetcher"`, `"Looper"`, `"Spawner"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a finding for %s; got:\n%s", want, joined)
		}
	}
}
