// retired_claims_test.go — a ratchet against guidance claims forge has
// already retired once.
//
// Shipped prose is the map an agent reads before it reads any code: the
// project memory file (reliant.md.tmpl, rendered as reliant.md / CLAUDE.md /
// AGENTS.md / .cursorrules / copilot-instructions.md) is auto-loaded into
// every session, and the skills are loaded the moment a task touches their
// topic. When two of those documents disagree, the always-loaded one wins
// attention — and in the roofers dogfood run it was the wrong one. It
// described a thin-handler + `internal/<svc>/contract.go` business layer that
// `forge scaffold service` has never written, while the `forge` skill
// (correctly) said the handler methods ARE the business logic. Agents
// followed the memory file, and the fix for one document was always one
// careless paste away from coming back in another.
//
// So each retired claim is registered ONCE, with the truth that replaced it
// and the skill that carries that truth, and every shipped guidance file is
// scanned for it. The registry is the contract, in the style of
// skills_drift_test.go's allowedGenFiles: retiring a claim means adding a row
// here in the same change that removes it from the prose.
//
// The patterns are deliberately narrow. Each matches the CLAIM, not the
// subject: "there is no `contract.go` inside a handler package" is the
// correction and must stay legal, so a row matches the affirmative shape
// only. Every row carries a known-bad sample it must catch and the corrected
// phrasing it must not (TestRetiredClaimRegistryIsSelfConsistent), because a
// guard that cries wolf on correct prose gets deleted, and one that matches
// nothing reports green forever.
package templates_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// retiredClaim is one statement forge's guidance used to make and no longer
// may.
type retiredClaim struct {
	// id is the stable handle a failure names.
	id string
	// pattern matches the affirmative claim. It never matches the correction.
	pattern *regexp.Regexp
	// truth is what is true instead, in one line — what the failure tells
	// the author to write.
	truth string
	// see is the shipped skill (path under skills/, without /SKILL.md) that
	// carries the truth in depth. Checked to exist, so the pointer cannot rot.
	see string
	// bad is a sample of the retired prose; pattern must match it.
	bad string
	// good is the corrected prose; pattern must not match it.
	good string
}

// retiredClaims is the registry. Add a row when guidance retires a claim;
// never delete one to make a failure go away — fix the prose.
var retiredClaims = []retiredClaim{
	{
		id: "handler-package-contract-go",
		pattern: regexp.MustCompile(
			"internal/handlers/<[^>]+>/contract\\.go" +
				"|service's `contract\\.go`" +
				"|contract\\.go, impl,? and generated handlers"),
		truth: "a handler package (internal/handlers/<svc>/) has no contract.go: its *Service methods ARE the rpc " +
			"logic, and `forge scaffold package <name>` writes internal/<name>/contract.go for logic that needs isolation",
		see:  "forge/api",
		bad:  "Internal services (`internal/handlers/<svc>/contract.go`) probably already split cleanly",
		good: "Domain packages behind the handlers (`internal/<name>/contract.go`, from `forge scaffold package`)",
	},
	{
		id:      "per-service-business-layer",
		pattern: regexp.MustCompile(`internal/<svc>/(?:contract\.go|\s*←\s*BUSINESS)`),
		truth: "there is no internal/<svc>/ business layer behind every service; logic lives on the handler's " +
			"*Service methods, and a standalone internal/<name>/ package is opt-in via `forge scaffold package`",
		see:  "forge/service-layer",
		bad:  "| Business decisions, DB calls | `internal/<svc>/contract.go` (interface) + `service.go` (impl) |",
		good: "a standalone package — `forge scaffold package <name>` writes `internal/<name>/contract.go`",
	},
	{
		id:      "thin-translation-handler",
		pattern: regexp.MustCompile(`(?i)THIN translation layer|handler grows past six steps`),
		truth: "the handler's *Service methods hold the rpc logic; move it into a `forge scaffold package` when it " +
			"outgrows the method (shared state machine, transport-free tests, a worker also calls it), not by step count",
		see:  "forge/api",
		bad:  "If a handler grows past six steps (validate / auth / convert / call service / convert / map error)",
		good: "Move an rpc's logic into a package when it outgrows the method",
	},
	{
		id:      "handler-error-mapping-file",
		pattern: regexp.MustCompile(`errors\.go\s+domain error\s*(?:→|->)\s*connect\.Code`),
		truth: "a handler returns svcerr.Wrap(err); a per-service connect.Code switch is what " +
			"forgeconv-no-handler-error-mapping flags",
		see:  "forge/api",
		bad:  "  errors.go       domain error → connect.Code",
		good: "`return nil, svcerr.Wrap(err)` — never a per-service `connect.Code` switch",
	},
	{
		id: "compose-forge-owned",
		pattern: regexp.MustCompile(
			"(?i)compose\\.go`?\\s+is\\s+(?:forge-owned|generated|regenerated)" +
				"|compose\\.go`?[^.\\n]{0,30}regenerated every run"),
		truth: "internal/app/compose.go is yours: written once, then forge reconciles only the component set and " +
			"Deps keys and keeps your value expressions (`forge project disown` stops even that)",
		see:  "forge/architecture",
		bad:  "`internal/app/compose.go` is forge-owned and regenerated every run.",
		good: "`internal/app/compose.go` are both yours to wire — forge reconciles compose.go's component set and Deps keys",
	},
	{
		id: "auth-required-informational",
		pattern: regexp.MustCompile(
			"(?i)auth_required[^\\n]{0,160}gates nothing at runtime" +
				"|informational metadata[^\\n]{0,80}auth_required" +
				"|auth_required`? annotation\\*\\*\\s*→\\s*informational"),
		truth: "auth_required is enforced fail-closed by the auth interceptor; `auth_required: false` publishes " +
			"that one rpc (projected into pkg/middleware/procedures_gen.go)",
		see:  "forge/auth",
		bad:  "- `(forge.v1.method)` may carry informational metadata (timeouts, error codes, `auth_required`); it gates nothing at runtime.",
		good: "- `auth_required` is enforced, fail-closed and per-rpc: unset or `true` needs a valid token",
	},
	{
		id:      "service-mock-file-name",
		pattern: regexp.MustCompile(`<svc>_mock\.go`),
		truth:   "the generated service mock is internal/handlers/mocks/<svc>_mock_gen.go (`_gen` in the name states the tier)",
		see:     "forge/service-layer",
		bad:     "(`internal/handlers/mocks/<svc>_mock.go`)",
		good:    "`internal/handlers/mocks/<svc>_mock_gen.go` follows the service proto",
	},
	{
		id:      "deps-resolved-by-name",
		pattern: regexp.MustCompile(`(?i)resolves added fields by name`),
		truth:   "composition (internal/app/compose.go) fills every Deps field BY TYPE, never by field name",
		see:     "forge/architecture",
		bad:     "// supplies Logger/Config and resolves added fields by name.",
		good:    "// (internal/app/compose.go) supplies Logger/Config and resolves added fields\n// by type.",
	},
}

// guidanceFile is one shipped document the registry is checked against.
type guidanceFile struct {
	where string // a path a reader can open
	body  string
}

// shippedGuidance returns every document forge ships or publishes as
// guidance: each project template (the skills, both agent-memory templates,
// the scaffolded READMEs and conventions files, and the Go templates whose
// comments a user reads inside their own project), the service templates,
// and forge's own README and docs/.
//
// Skill templates are read raw — `{{.CLI}}` and friends never occur inside a
// pattern, so rendering would only add a failure mode.
func shippedGuidance(t *testing.T) []guidanceFile {
	t.Helper()
	var out []guidanceFile
	for _, cat := range []struct {
		root string
		set  templates.TemplateCategory
	}{
		{"internal/templates/project/", templates.ProjectTemplates()},
		{"internal/templates/service/", templates.ServiceTemplates()},
	} {
		names, err := cat.set.List("")
		if err != nil {
			t.Fatalf("list %s: %v", cat.root, err)
		}
		for _, name := range names {
			body, err := cat.set.Get(name)
			if err != nil {
				t.Fatalf("read %s%s: %v", cat.root, name, err)
			}
			out = append(out, guidanceFile{where: cat.root + name, body: string(body)})
		}
	}

	repoRoot := filepath.Join("..", "..")
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	out = append(out, guidanceFile{where: "README.md", body: string(readme)})
	docs := filepath.Join(repoRoot, "docs")
	err = filepath.WalkDir(docs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return walkErr
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(repoRoot, path)
		out = append(out, guidanceFile{where: filepath.ToSlash(rel), body: string(body)})
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs/: %v", err)
	}
	return out
}

// TestShippedGuidanceMakesNoRetiredClaim is the ratchet.
func TestShippedGuidanceMakesNoRetiredClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every shipped skill against a scaffolded project; runs in task test")
	}
	t.Parallel()

	files := shippedGuidance(t)
	sawMemory, sawSkill := false, false
	for _, f := range files {
		switch {
		case strings.HasSuffix(f.where, "/reliant.md.tmpl"):
			sawMemory = true
		case strings.HasSuffix(f.where, "/skills/forge/SKILL.md"):
			sawSkill = true
		}
	}
	// The two documents this guard exists for. Missing either means the walk
	// silently stopped covering them, and every assertion below goes vacuous.
	if !sawMemory || !sawSkill {
		t.Fatalf("guidance walk covered reliant.md.tmpl=%v, skills/forge/SKILL.md=%v — both must be scanned", sawMemory, sawSkill)
	}

	type hit struct{ where, id, line, truth, see string }
	var hits []hit
	for _, f := range files {
		for i, line := range strings.Split(f.body, "\n") {
			for _, c := range retiredClaims {
				if c.pattern.MatchString(line) {
					hits = append(hits, hit{
						where: f.where + ":" + itoaRC(i+1), id: c.id,
						line: strings.TrimSpace(line), truth: c.truth, see: c.see,
					})
				}
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].where < hits[j].where })
	for _, h := range hits {
		t.Errorf("%s repeats retired claim %q:\n  %s\n\n  The truth: %s.\n  Depth: `forge skill load %s`.",
			h.where, h.id, h.line, h.truth, strings.TrimPrefix(h.see, "forge/"))
	}
}

// TestRetiredClaimRegistryIsSelfConsistent keeps every row honest: it must
// catch the prose it retired, must leave the correction alone, and must point
// at a skill that exists.
func TestRetiredClaimRegistryIsSelfConsistent(t *testing.T) {
	t.Parallel()

	skills := shippedSkills(t)
	seen := map[string]bool{}
	for _, c := range retiredClaims {
		if seen[c.id] {
			t.Errorf("duplicate retired-claim id %q", c.id)
		}
		seen[c.id] = true
		if c.truth == "" || c.bad == "" || c.good == "" {
			t.Errorf("%s: every row needs truth, bad and good — a row without samples proves nothing", c.id)
		}
		if !c.pattern.MatchString(c.bad) {
			t.Errorf("%s: pattern %q does not match its own retired sample %q — it guards nothing", c.id, c.pattern, c.bad)
		}
		for _, line := range strings.Split(c.good, "\n") {
			if c.pattern.MatchString(line) {
				t.Errorf("%s: pattern %q matches the CORRECTED prose %q — it would fail the fix", c.id, c.pattern, line)
			}
		}
		if _, ok := skills[c.see+"/SKILL.md"]; !ok {
			t.Errorf("%s: points readers at skill %q, which forge does not ship", c.id, c.see)
		}
	}
}

func itoaRC(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}
