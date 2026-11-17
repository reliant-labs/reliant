// format_utils_test_isolation_test.go — the scaffolded format-utils Vitest
// suite must pass in ANY test order.
//
// registerStatusVariants writes into a module-level map and nothing
// un-registers, so a test that registers into the module the rest of the file
// imported changes the answer for every test that runs after it. The suite
// used to do exactly that and carry a comment saying the registration test
// "Runs last". Under `vitest --sequence.shuffle` the "is neutral for an
// unregistered domain status" test then failed in 13 of 30 runs, because
// sent_to_pharmacy was already registered by the time it ran.
//
// The file is scaffold-once, so a consumer runs whatever forge shipped on the
// day it was created. The order-dependence has to be gone at the source.

package templates

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// staticFormatUtilsImportRe matches the file's STATIC import of
// @/lib/format-utils (the module instance every other test shares), capturing
// the imported names.
var staticFormatUtilsImportRe = regexp.MustCompile(`(?s)import\s*\{([^}]*)\}\s*from\s*"@/lib/format-utils"`)

func TestFormatUtilsSuiteNeverRegistersIntoTheSharedModule(t *testing.T) {
	b, err := FrontendTemplates().Get(filepath.Join("shared-web", "src", "lib", "format-utils.test.ts"))
	if err != nil {
		t.Fatalf("read shared-web/src/lib/format-utils.test.ts: %v", err)
	}
	s := string(b)

	m := staticFormatUtilsImportRe.FindStringSubmatch(s)
	if m == nil {
		t.Fatal("format-utils.test.ts no longer statically imports @/lib/format-utils — update this guard to follow it")
	}
	for _, name := range strings.Split(m[1], ",") {
		if strings.TrimSpace(name) == "registerStatusVariants" {
			t.Errorf("format-utils.test.ts imports registerStatusVariants statically. A registration through " +
				"that import mutates the module every other test in the file shares, so the suite passes " +
				"only in the order it was written. Register into a fresh copy: vi.resetModules() then " +
				"`await import(\"@/lib/format-utils\")`.")
		}
	}

	// The registration must go through a module instance of its own…
	for _, want := range []string{
		"vi.resetModules()",
		`await import("@/lib/format-utils")`,
		"fresh.registerStatusVariants(",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("format-utils.test.ts is missing %q — the registration test must run against a fresh "+
				"module instance, not the shared one", want)
		}
	}
	// …and prove that it did, so the isolation cannot quietly regress into
	// a test that passes only when it happens to run last.
	if !strings.Contains(s, `expect(enumBadgeVariant("sent_to_pharmacy")).toBe("neutral");`) {
		t.Error("format-utils.test.ts no longer asserts the shared module is still unregistered after the " +
			"fresh copy registered — without it an isolation regression passes whenever the test runs last")
	}
	if strings.Contains(s, "Runs last") {
		t.Error("format-utils.test.ts still documents an ordering dependency (\"Runs last\"); Vitest does not " +
			"promise file order under --sequence.shuffle, and the suite must not need one")
	}
}
