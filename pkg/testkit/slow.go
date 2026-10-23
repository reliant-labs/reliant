package testkit

// Slow tests: the one declaration that splits a suite into a fast, blocking
// tier and a slow, non-blocking one.
//
// # THE TIERS
//
//	go test -short ./...            the FAST tier: the inner loop and the
//	                                blocking gate. Every unmarked test runs.
//	go test ./...                   the FULL tier (`task test`): everything,
//	                                slow tests included.
//	go test -tags=integration ./... the integration lane: the tagged tests.
//
// A test is fast unless it says otherwise. Slowness is the exception, and it
// is declared where it happens, with a reason:
//
//	func TestRendersProd(t *testing.T) {
//		testkit.Slow(t, "renders prod through `forge env render`",
//			testkit.Inputs("deploy/kcl/**"))
//		...
//	}
//
// It sits on Go's own -short flag rather than a custom one, so plain
// `go test`, an IDE's run button and Go's test cache keep working unchanged.
//
// # WHY MARK THE SLOW TESTS, NOT THE FAST ONES
//
// Marking the fast ones would make every new test slow (and blocking-tier
// silent) until someone remembered to opt it in. Marking the slow ones makes
// the default the cheap, correct one, and puts the cost where it is paid:
// the test that boots a container or execs `go build` names that work. Both
// directions are enforced by measurement, not trust — `forge lint
// --slow-tests` flags a test that execs a toolchain or starts a container
// without the marker, and `forge ci test-budget` flags an unmarked test that
// took longer than the budget in a -short run.
//
// # WHY THE INTEGRATION LANE SKIPS IT TOO
//
// -tags=integration only ADDS files: it compiles every untagged test of the
// package as well, and the full tier has already run those. So an untagged
// slow test skips there, and runs exactly once in the whole gate. (An
// integration-tagged test must never call Slow: it would skip in the only
// lane that compiles it.)
//
// # INPUTS
//
// A slow test can name the files it depends on, as globs relative to the
// module root. A tool that selects tests by what a change touched can then
// run it in the blocking tier exactly when one of those files changed — a
// KCL render when deploy/ moved — and leave it to the full tier otherwise.
// The globs are read from SOURCE, not from a test run: forge's slowtest
// analyzer extracts the literal arguments of Inputs, which is why it requires
// them to be string literals.

import (
	"errors"
	"path"
	"strings"
	"testing"
)

// SlowOption configures [Slow].
type SlowOption func(*slowConfig)

type slowConfig struct {
	inputs []string
}

// Inputs declares the files a slow test depends on: globs relative to the
// module root, slash-separated, where `*` matches within one path segment
// and `**` matches any number of segments ("deploy/kcl/**", "db/migrations/*.sql").
//
// The arguments must be string literals. They are read from source by
// forge's slowtest analyzer, without running the test; `forge lint
// --slow-tests` reports a non-literal argument. Slow validates them on every
// run — including -short runs, where it then skips — so a malformed glob
// fails in the fast tier rather than going unread.
func Inputs(globs ...string) SlowOption {
	return func(c *slowConfig) { c.inputs = append(c.inputs, globs...) }
}

// Slow marks t as a slow test: it skips under -short (the fast tier) and in
// the integration lane, and runs in the full tier (`go test` without -short,
// `task test` in a forge project). why names the work that makes it slow —
// "renders prod through `forge env render`", "builds the forge binary" —
// and is the skip message, so a reader of a -short run sees what was left
// out and where it runs instead.
//
// Call it first, before the slow work. A helper that does the slow work for
// several tests may call it instead, with the test's t.
func Slow(t testing.TB, why string, opts ...SlowOption) {
	t.Helper()
	var cfg slowConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if strings.TrimSpace(why) == "" {
		t.Fatalf("testkit.Slow: why is empty; name the work that makes this test slow")
	}
	for _, glob := range cfg.inputs {
		if err := validateInputGlob(glob); err != nil {
			t.Fatalf("testkit.Inputs(%q): %v", glob, err)
		}
	}
	if testing.Short() {
		t.Skip("slow test, skipped under -short: " + why + "; runs in the full tier (go test without -short, task test)")
	}
	if integrationLane {
		t.Skip("slow test, skipped in the integration lane: " + why + "; an untagged test, it runs in the full tier (go test without -short, task test)")
	}
}

// SlowSkipped reports whether [Slow] skips every test of this binary: under
// -short and in the integration lane. It is for work a package does before
// any test starts — a TestMain that builds a binary only slow tests use.
func SlowSkipped() bool {
	return testing.Short() || integrationLane
}

// validateInputGlob rejects what no selection tool could match against a
// module-relative path.
func validateInputGlob(glob string) error {
	switch {
	case glob == "":
		return errors.New("empty glob")
	case strings.Contains(glob, `\`):
		return errors.New("use forward slashes: globs are module-relative paths, not OS paths")
	case path.IsAbs(glob):
		return errors.New("absolute path; globs are relative to the module root")
	case path.Clean(glob) != glob && path.Clean(glob)+"/" != glob:
		return errors.New("not a clean path (no `.`, `..` or doubled slashes)")
	case glob == ".." || strings.HasPrefix(glob, "../"):
		return errors.New("escapes the module root")
	}
	for _, segment := range strings.Split(strings.TrimSuffix(glob, "/"), "/") {
		if segment == "**" {
			continue
		}
		if strings.Contains(segment, "**") {
			return errors.New("`**` must be a whole path segment (\"deploy/**\", not \"deploy**\")")
		}
		if _, err := path.Match(segment, ""); err != nil {
			return errors.New("malformed pattern segment " + segment + ": " + err.Error())
		}
	}
	return nil
}
