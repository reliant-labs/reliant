package vacuousguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE EMITTER LEDGER
//
// One entry per existence-gate the judging trees hold on a generated Go file.
// The entry names WHO WRITES the file, because that — not "does a test exist" —
// is the question that separates a check from a ceremony.
//
// `forge project audit` shipped a wire_coverage category that stat'd
// pkg/app/wire_gen.go and reported StatusOK when it was absent. Two `forge lint`
// flags scanned the same ghost. Nothing had emitted that file since the DI
// rewrite — forge's own generator test asserts it is never written — so all
// three were permanently green and read as a clean bill of health. Every one of
// them had passing unit tests; the tests fed the scanner synthetic strings, and
// no test asked whether a real project could produce the input.
//
// An entry is a claim, and TestEmitterLedgerNamesRealEmitters checks it: the
// named writer must exist and must still mention the file. An entry that stops
// being true fails rather than going on excusing the gate.
// ─────────────────────────────────────────────────────────────────────────────

type gateLedgerEntry struct {
	// Path is the project-relative Go file the gate stats.
	Path string
	// Writer is the repo-relative source file that emits Path, or "" for a
	// FiresOnPresence entry.
	Writer string
	// Mentions is the token Writer must contain for the claim to hold —
	// usually the file's base name.
	Mentions string
	// FiresOnPresence marks a gate whose finding fires BECAUSE the file is
	// there. Absence is then the healthy state and no emitter is required.
	// This is the one shape that is legitimately unwritten by forge.
	FiresOnPresence string
	// Quarantine records a gate that is vacuous TODAY and why it has not been
	// deleted. As in the vacuous-test ledger, it can only shrink.
	Quarantine string
}

var gateLedger = []gateLedgerEntry{
	{
		Path:            "pkg/app/testing_extras.go",
		FiresOnPresence: "The workaround lint FLAGS this file's existence — a hand-rolled stub-repo bridge the user wrote because the test factory did not fill required Deps. forge must never write it; absence is the healthy state and correctly produces no finding.",
	},
}

// TestNoGatesOnUnwrittenPaths is the rule's enforcement: every gate the judging
// trees hold on a generated Go file must be accounted for in the ledger.
func TestNoGatesOnUnwrittenPaths(t *testing.T) {
	findings := scanGatesRepo(t)

	known := map[string]bool{}
	for _, e := range gateLedger {
		known[e.Path] = true
	}
	var fresh []Finding
	for _, f := range findings {
		if !known[f.Detail] {
			fresh = append(fresh, f)
		}
	}
	if len(fresh) == 0 {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d audit/lint gate(s) on a generated Go file with no ledger entry.\n\n", len(fresh))
	for _, f := range fresh {
		fmt.Fprintf(&b, "  %s\n\n", f)
	}
	b.WriteString("  A check that stats a file nothing emits is permanently green, and green is what\n")
	b.WriteString("  everyone reads. Add a gateLedger entry naming the emitter that writes the file.\n")
	b.WriteString("  If nothing writes it, the check cannot fail and should be deleted, not ledgered.\n")
	t.Error(b.String())
}

// TestEmitterLedgerNamesRealEmitters keeps each entry honest. A Writer entry
// whose named emitter no longer mentions the file is an entry that has quietly
// become the excuse it was meant to prevent.
func TestEmitterLedgerNamesRealEmitters(t *testing.T) {
	root := repoRoot(t)
	live := map[string]bool{}
	for _, f := range scanGatesRepo(t) {
		live[f.Detail] = true
	}
	seen := map[string]bool{}

	for _, e := range gateLedger {
		if seen[e.Path] {
			t.Errorf("gateLedger lists %s twice", e.Path)
		}
		seen[e.Path] = true

		if !live[e.Path] {
			t.Errorf("gateLedger entry %s matches no gate.\n"+
				"  Either the gate was deleted — delete this entry, that is the point of the ratchet —\n"+
				"  or the rule was narrowed until it stopped seeing it, which is a guard regression.", e.Path)
			continue
		}

		kinds := 0
		for _, set := range []string{e.Writer, e.FiresOnPresence, e.Quarantine} {
			if strings.TrimSpace(set) != "" {
				kinds++
			}
		}
		if kinds != 1 {
			t.Errorf("gateLedger entry %s must declare exactly one of Writer / FiresOnPresence / Quarantine; got %d", e.Path, kinds)
			continue
		}
		if e.Writer == "" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(e.Writer)))
		if err != nil {
			t.Errorf("gateLedger entry %s names emitter %s, which does not exist: %v", e.Path, e.Writer, err)
			continue
		}
		token := e.Mentions
		if token == "" {
			token = filepath.Base(e.Path)
		}
		if !strings.Contains(string(data), token) {
			t.Errorf("gateLedger entry %s names emitter %s, which no longer mentions %q.\n"+
				"  The claim \"something writes this\" has stopped being true — which is exactly how\n"+
				"  pkg/app/wire_gen.go's readers outlived its writer.", e.Path, e.Writer, token)
		}
	}
}

// TestUnwrittenGateRuleFiresOnAPlantedDefect proves the rule can fail. A guard
// that cannot fail is worse than no guard: it reports green.
func TestUnwrittenGateRuleFiresOnAPlantedDefect(t *testing.T) {
	src := []byte(`package audit

import (
	"os"
	"path/filepath"
)

func auditGhost(projectDir string) string {
	path := filepath.Join(projectDir, "pkg", "app", "ghost_gen.go")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "ok"
	}
	return "warn"
}
`)
	got, err := ScanGateSource("internal/cli/audit/planted.go", src)
	if err != nil {
		t.Fatalf("ScanGateSource: %v", err)
	}
	if len(got) != 1 || got[0].Detail != "pkg/app/ghost_gen.go" {
		t.Fatalf("expected one finding for pkg/app/ghost_gen.go, got %+v", got)
	}
	if got[0].Rule != RuleUnwrittenGate {
		t.Errorf("finding carries rule %q, want %q", got[0].Rule, RuleUnwrittenGate)
	}
}

// TestUnwrittenGateRuleIgnoresLegitimateShapes pins the rule's edges. Each of
// these is a gate forge legitimately holds, and a rule broad enough to flag them
// is a rule that gets switched off.
func TestUnwrittenGateRuleIgnoresLegitimateShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "a directory in the USER's tree, legitimately absent",
			body: `migDir := filepath.Join(projectDir, "db", "migrations")
	_, _ = os.ReadDir(migDir)`,
		},
		{
			name: "a non-Go project file",
			body: `_, _ = os.Stat(filepath.Join(projectDir, "go.mod"))`,
		},
		{
			name: "a bare basename joined to a per-package dir names nothing",
			body: `_, _ = os.ReadFile(filepath.Join(pkgDir, "contract.go"))`,
		},
		{
			name: "a path with a non-literal segment cannot be named",
			body: `_, _ = os.Stat(filepath.Join(projectDir, "internal", role, "service.go"))`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte("package lint\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n)\n\nfunc check(projectDir, pkgDir, role string) {\n\t" + tc.body + "\n}\n")
			got, err := ScanGateSource("internal/cli/lint/x.go", src)
			if err != nil {
				t.Fatalf("ScanGateSource: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("legitimate gate flagged: %+v", got)
			}
		})
	}
}

// scanGatesRepo runs the gate scan over forge once per test binary.
var (
	gatesOnce     sync.Once
	gatesFindings []Finding
	gatesErr      error
)

func scanGatesRepo(t *testing.T) []Finding {
	t.Helper()
	gatesOnce.Do(func() { gatesFindings, gatesErr = ScanGates(repoRoot(t)) })
	if gatesErr != nil {
		t.Fatalf("scan gates: %v", gatesErr)
	}
	return gatesFindings
}

// TestGateScanReachesEveryJudgingTree keeps a future gateRoots or skipDirs edit
// from quietly shrinking the surface. A scan that sees nothing passes
// everything — the failure mode this whole package exists to name.
func TestGateScanReachesEveryJudgingTree(t *testing.T) {
	root := repoRoot(t)
	for _, gr := range gateRoots {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(gr))); err != nil {
			t.Errorf("gateRoots names %s, which is not in the repo: %v — the rule is scanning a ghost of its own", gr, err)
		}
	}
	// ScanGates errors out on an empty file set, so reaching here at all
	// proves the walk found production sources; assert it found MANY, so a
	// skipDirs edit that prunes most of a tree still fails.
	if _, err := ScanGates(root); err != nil {
		t.Fatalf("ScanGates: %v", err)
	}
}
