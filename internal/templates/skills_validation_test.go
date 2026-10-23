// skills_validation_test.go — fact-checks the shipped SKILL.md files
// against the forge binary they ship inside.
//
// Skills are LLM guidance; their value collapses when they reference
// commands or generated-file paths that the CURRENT forge no longer has.
// Nine validators run over every embedded SKILL.md:
//
//  1. `forge <subcommand>` references (in inline code spans and fenced
//     code blocks) must resolve against the real registered cobra
//     command tree (cli.NewRootCmd()).
//  2. Path-like references rooted at a directory a forge project has
//     (pkg/{app,config,middleware}/*, internal/{app,db,handlers}/*,
//     .forge/*) must correspond to something forge actually scaffolds
//     (a real ProjectGenerator run) or a known codegen output. The same
//     check rejects a reference to the MIRROR of a real directory under
//     the other root — internal/config when the generated typed config
//     is pkg/config — because that is the shape a stale skill drifts
//     into. Truth source: forgeOwnedDirs + the scaffold tree.
//  3. `(forge.v1.<ext>) = { ... }` annotation literals (and dotted
//     `(forge.v1.<ext>).<field>` references) must use field names that
//     exist on the real annotation payload messages. Truth source: the
//     protoreflect descriptors of pkg/forgepb (generated from
//     internal/assets/proto/forge/v1/forge.proto), so a schema change
//     that strands the skills fails here. Enum-valued fields (e.g.
//     `store:`) must use enum value names, bool fields must get
//     true/false, and message-valued fields (e.g. `validate:`) must get
//     `{ ... }` literals rather than scalars. This validator also covers
//     reliant.md.tmpl, which ships the same annotation guidance.
//  4. `forge skill load <path>` references — in SKILL.md AND in every
//     other shipped template (scaffolded code points readers at skills
//     from its header comments) — must name a skill forge actually ships.
//  5. `forge/pkg/<name>` references must name a real subpackage of the
//     forge/pkg module.
//  6. `.categories.<name>` references (the audit-json skill's jq recipes
//     and category table) must name a category `forge project audit
//     --json` actually emits. Truth source: audit.CategoryNames().
//  7. `features.<name>` / `features.experimental.<name>` references must
//     name a real feature flag. Truth source: EffectiveFeatures().
//  8. A proto `package ...;` declaration must be one forge can emit: a
//     service proto is `services.<svc>.v<n>` with no project prefix
//     (truth source: the package line on the scaffold's OWN proto stub),
//     and any declaration carrying a `// <dir>/<file>.proto` locator
//     comment must satisfy buf's PACKAGE_DIRECTORY_MATCH.
//  9. A top-level key in a block the prose introduces as forge.yaml must
//     be one forge.yaml accepts. Truth source: config.LoadProject over a
//     synthetic doc — removed keys (removedSchemaKeys) surface on the
//     config warning sink, keys that never existed as load errors.
//  10. A `--flag` spelled after a `forge <chain>` invocation must be one
//     that chain's command actually accepts (own, inherited, or global).
//     Validator 1 only proves the chain resolves; a flag forge dropped —
//     or whose sense inverted, as `--no-heal` did when healing became
//     opt-in `--heal` — sails straight through it.
//  11. A package-qualified auth identifier — `auth.X`, `authn.X`,
//     `apikey.X`, `middleware.X` — must be exported by that package.
//     Truth source: go/parser over forge/pkg/{auth,authn,apikey,middleware}
//     and the rendered project pkg/middleware templates. Its companion pins
//     every `Provider: "…"` an auth skill shows to the scaffolded
//     internal/app/auth.go.
//
// Legitimate exceptions live in testdata/skills_validation_allowlist.txt
// (one per line: "<skill rel path>|<claim>|<justification>"). The goal is
// that NEW drift fails CI with a message naming the skill file and the
// stale claim.
package templates_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/reliant-labs/forge/internal/cli"
	"github.com/reliant-labs/forge/internal/cli/audit"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// ─── shared fixtures ────────────────────────────────────────────────────

// shippedSkills returns rel-path → content for every embedded SKILL.md.
func shippedSkills(t *testing.T) map[string]string {
	t.Helper()
	files, err := templates.ProjectTemplates().List("skills")
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	out := map[string]string{}
	for _, rel := range files {
		if filepath.Base(rel) != "SKILL.md" {
			continue
		}
		content, err := templates.ProjectTemplates().Get("skills/" + rel)
		if err != nil {
			t.Fatalf("read skill %s: %v", rel, err)
		}
		out[rel] = string(content)
	}
	if len(out) == 0 {
		t.Fatal("no shipped SKILL.md files found")
	}
	return out
}

// allowlist returns skillRel → set of allowlisted claims.
func allowlist(t *testing.T) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	data, err := os.ReadFile(filepath.Join("testdata", "skills_validation_allowlist.txt"))
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("read allowlist: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
			t.Fatalf("allowlist line needs '<skill>|<claim>|<justification>': %q", line)
		}
		skill, claim := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if out[skill] == nil {
			out[skill] = map[string]bool{}
		}
		out[skill][claim] = true
	}
	return out
}

func allowed(allow map[string]map[string]bool, skillRel, claim string) bool {
	return allow[skillRel][claim] || allow["*"][claim]
}

// scaffoldTree generates a full-featured project once and returns the set
// of slash-separated relative paths it produces (files AND directories).
var scaffoldTreeOnce struct {
	sync.Once
	root  string
	paths map[string]bool
	err   error
}

func scaffoldTree(t *testing.T) map[string]bool {
	t.Helper()
	scaffoldOnce(t)
	return scaffoldTreeOnce.paths
}

// scaffoldRoot is the on-disk root of the shared demo project, for checks
// that need the scaffolded BYTES rather than just the path set.
func scaffoldRoot(t *testing.T) string {
	t.Helper()
	scaffoldOnce(t)
	return scaffoldTreeOnce.root
}

func scaffoldOnce(t *testing.T) {
	t.Helper()
	scaffoldTreeOnce.Do(func() {
		root, err := os.MkdirTemp("", "forge-skill-validate-*")
		if err != nil {
			scaffoldTreeOnce.err = err
			return
		}
		scaffoldTreeOnce.root = root
		// Not t.Cleanup: the tree is shared across tests via sync.Once, so
		// the first test to finish must not delete what the rest still read.
		// TestMain (main_test.go) removes it after the whole suite, and keeps
		// it on failure so a scaffold assertion can be diagnosed against the
		// bytes that were actually generated.
		templates.RegisterSharedTempDir(root)
		gen := generator.NewProjectGenerator("demo", root, "example.com/demo")
		gen.ServiceName = "users"
		gen.FrontendName = "web"
		if err := gen.Generate(); err != nil {
			scaffoldTreeOnce.err = err
			return
		}
		paths := map[string]bool{}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil || rel == "." {
				return nil
			}
			paths[filepath.ToSlash(rel)] = true
			return nil
		})
		scaffoldTreeOnce.paths = paths
	})
	if scaffoldTreeOnce.err != nil {
		t.Fatalf("scaffold demo project: %v", scaffoldTreeOnce.err)
	}
}

// ─── markdown extraction ────────────────────────────────────────────────

var inlineCodeRE = regexp.MustCompile("`([^`\n]+)`")

// codeRegions returns the inline code spans and fenced-code-block lines
// of a markdown document — the places where `forge <cmd>` references are
// commands rather than prose.
func codeRegions(md string) []string {
	var regions []string
	inFence := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			regions = append(regions, line)
			continue
		}
		for _, m := range inlineCodeRE.FindAllStringSubmatch(line, -1) {
			regions = append(regions, m[1])
		}
	}
	return regions
}

// ─── validator 1: forge subcommand references ───────────────────────────

// forgeCmdChainRE captures the lowercase word chain following "forge ".
var forgeCmdChainRE = regexp.MustCompile(`(^|[^./\w-])forge\s+([a-z][a-z0-9_-]*(?:\s+[a-z][a-z0-9_-]*)*)`)

// versionWordRE matches version-ish tokens ("v0", "v1") that follow
// "forge" in prose like "forge v0.2" — not command references.
var versionWordRE = regexp.MustCompile(`^v\d`)

func findChild(cmd *cobra.Command, token string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == token || c.HasAlias(token) {
			return c
		}
	}
	return nil
}

// validateCommandChain walks tokens down the cobra tree. Returns "" when
// the chain is plausible, else a human-readable reason.
func validateCommandChain(root *cobra.Command, tokens []string) string {
	cur := root
	for i, tok := range tokens {
		child := findChild(cur, tok)
		if child != nil {
			cur = child
			continue
		}
		if i == 0 {
			return "no such forge subcommand: " + tok
		}
		// Token didn't match a child. Ask the command itself whether it
		// would accept the remaining tokens as positional args — exactly
		// the question cobra asks at runtime, so this validator and the
		// real CLI can never disagree.
		//
		// Runnable() is NOT the discriminator. Every command group now
		// carries an Args validator + a help RunE (cmdutil.StrictGroup) so
		// that `forge <group> <typo>` exits non-zero instead of printing
		// help and reporting success — which makes every group Runnable()
		// and would have silently turned this check into a no-op.
		if err := cur.ValidateArgs(tokens[i:]); err != nil {
			return tok + " is not a subcommand of 'forge " + strings.Join(tokens[:i], " ") + "'"
		}
		break
	}
	return ""
}

func TestSkillsForgeCommandReferencesExist(t *testing.T) {
	skills := shippedSkills(t)
	allow := allowlist(t)
	root := cli.NewRootCmd()

	for rel, content := range skills {
		seen := map[string]bool{}
		for _, region := range codeRegions(content) {
			for _, m := range forgeCmdChainRE.FindAllStringSubmatch(region, -1) {
				chain := m[2]
				tokens := strings.Fields(chain)
				if len(tokens) == 0 || versionWordRE.MatchString(tokens[0]) {
					continue
				}
				claim := "forge " + chain
				if seen[claim] {
					continue
				}
				seen[claim] = true
				if reason := validateCommandChain(root, tokens); reason != "" {
					if allowed(allow, rel, claim) {
						continue
					}
					t.Errorf("skills/%s: stale command reference %q — %s\n  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
						rel, claim, reason)
				}
			}
		}
	}
}

// ─── validator 2: repo-shape path references ────────────────────────────

// filePolicy says how precisely the file names under a forge-owned
// directory can be fact-checked.
type filePolicy int

const (
	// filesFixed: forge writes every file in the directory, so the base
	// name must appear in the directory's known-file set.
	filesFixed filePolicy = iota
	// filesGenOnly: mixed user/generated territory — only *_gen.go names
	// are claims about forge, the rest are user files or examples.
	filesGenOnly
	// filesMixed: the directory is real but its contents are the user's
	// (scaffold-once files they own and grow). Only the directory itself
	// is a checkable claim.
	filesMixed
)

// forgeOwnedDirs is the set of directories a forge project actually has,
// keyed by the two-segment path, with the policy for checking file names
// under each. It doubles as the truth source for "wrong home" claims:
// a reference to <other-root>/<name> where only <root>/<name> is real
// (e.g. internal/config when the generated typed config is pkg/config)
// fails with the real location named.
var forgeOwnedDirs = map[string]struct {
	policy filePolicy
	files  map[string]bool
}{
	// forge owns pkg/app: scaffolded files plus `forge generate` outputs.
	"pkg/app": {policy: filesFixed, files: knownCodegenPkgAppFiles},
	// pkg/config/config_gen.go is generated from the proto/config annotations
	// (stepGenerateConfig). config.go is the CLI-kind hand-owned stub.
	"pkg/config": {policy: filesFixed, files: map[string]bool{"config_gen.go": true, "config.go": true}},
	// pkg/middleware ships two scaffold-once files the user owns from line
	// one and grows — the directory is a claim, its contents are not.
	"pkg/middleware": {policy: filesMixed},
	// internal/app is the composition layer `forge generate` writes, which
	// is why none of it appears in a bare scaffold tree.
	"internal/app": {policy: filesFixed, files: knownCodegenInternalAppFiles},
	// internal/db holds the ORM projection (orm_shared.go, <entity>_orm.go)
	// alongside user-owned sibling query files.
	"internal/db": {policy: filesMixed},
	// internal/handlers/<svc>/ is mixed: generated CRUD projections next to
	// the user's handlers.go / contract.go / service.go.
	"internal/handlers": {policy: filesGenOnly},
}

// pathRefRE captures path-like references rooted at a directory a forge
// project has — or at the mirror of one under the other root, so a
// wrong-home claim is extracted rather than silently skipped.
//
// The alternation is ordered longest-first because Go's regexp is
// leftmost-FIRST: "internal/handlers" has to be tried before the bare
// "handlers" alternative, or the match would start mid-path and the
// internal/ root would be invisible to validatePathRef.
var pathRefRE = regexp.MustCompile(pathRefPattern())

func pathRefPattern() string {
	alts := map[string]bool{"handlers": true}
	for dir := range forgeOwnedDirs {
		alts[dir] = true
		alts[mirrorRoot(dir)] = true
	}
	ordered := sortedKeys(alts)
	// Longest first so a two-segment prefix always wins over "handlers".
	slices.SortStableFunc(ordered, func(a, b string) int { return len(b) - len(a) })
	quoted := make([]string, 0, len(ordered)+1)
	for _, a := range ordered {
		quoted = append(quoted, regexp.QuoteMeta(a))
	}
	quoted = append(quoted, `\.forge`)
	return `(?:` + strings.Join(quoted, "|") + `)/[A-Za-z0-9_\-./<>{}*]*`
}

// mirrorRoot flips a two-segment project path between the internal/ and
// pkg/ roots: internal/config ⇄ pkg/config.
func mirrorRoot(dir string) string {
	switch {
	case strings.HasPrefix(dir, "internal/"):
		return "pkg/" + strings.TrimPrefix(dir, "internal/")
	case strings.HasPrefix(dir, "pkg/"):
		return "internal/" + strings.TrimPrefix(dir, "pkg/")
	}
	return dir
}

// knownDotForgeEntries are the .forge/ children forge actually writes
// (harvested from filepath.Join(".forge", ...) call sites).
var knownDotForgeEntries = map[string]bool{
	// checksums.json is the DEAD legacy manifest — still referenced by
	// migration docs (forge reads + deletes it during the one-time
	// migration), so it stays a known entry.
	"checksums.json":        true,
	"disowned.json":         true,
	"hashes.json":           true,
	"scaffolded.json":       true, // the scaffold-once birth ledger (internal/checksums/scaffoldledger.go)
	"render":                true,
	"friction.jsonl":        true,
	"skills":                true,
	"state":                 true,
	"logs":                  true, // forge env up writes per-service logs to .forge/logs/<env>/<name>.log
	"ci":                    true, // forge ci gate's per-check logs, .forge/ci/<lane>/<check>.log when git does not ignore tmp/ (cli.gateLogDir)
	"observability":         true, // forge env up renders the local forwarder's pipeline into .forge/observability/forwarder.yaml (clickstack.ForwarderConfigDir)
	"identity":              true, // forge env up writes a host-run managed identity's non-secret outputs to .forge/identity/<name>.json (internal/cli/up_identity.go)
	"runtime-config":        true, // forge env up renders an out-of-tree frontend's config.js into .forge/runtime-config/<frontend>/config.js (internal/cli/frontend_config_refresh.go)
	"debug":                 true,
	"debug-session.json":    true,
	"migrations.json":       true,
	"forge.lock":            true,
	".scaffold-in-progress": true,
	".next":                 true,
}

// knownGeneratedHandlerFiles are the per-service generated files codegen
// emits into handlers/<svc>/ (beyond what the scaffold itself writes).
// A name that no emitter produces belongs OUT of this map: leaving one in
// licenses a skill to promise a file the user will never see.
var knownGeneratedHandlerFiles = map[string]bool{
	"handlers_crud_ops_gen.go":  true,
	"handlers_crud_gen.go":      true, // pre-split name; the migration skills that move projects off it still have to say it
	"handlers_crud_gen_test.go": true, // retired name; the migration skills that move projects off it still have to say it
	"webhook_routes_gen.go":     true,
	// The per-service test harness. It replaced the single pkg/app/testing.go,
	// whose `testing` import reached cmd/ through pkg/app and so shipped in
	// every scaffolded project's production binary.
	"helpers_gen_test.go": true,
	// The typed entity factories, emitted beside the handler package whose
	// CRUD RPCs own the entity. They replaced internal/testfactory — a
	// non-test package importing `testing`, kept out of the binary only by
	// nothing happening to import it.
	"factories_gen_test.go": true,
	// The per-service mock, renamed <svc>_mock.go → <svc>_mock_gen.go so its
	// NAME states that it is hash-guarded (the old spelling read like a
	// hand-written test double).
	"things_mock_gen.go": true,
	"order_mock_gen.go":  true,
}

// knownCodegenInternalAppFiles are the internal/app/ files forge writes:
// the forge-owned composition layer (compose.go / lifecycle.go /
// mounts_services_gen.go) plus the scaffold-once files the user then owns
// (providers.go / auth.go / login_broker.go). They are absent from a bare
// scaffold tree because `forge generate` — not `forge project new` — emits
// them.
var knownCodegenInternalAppFiles = map[string]bool{
	"compose.go":             true,
	"lifecycle.go":           true,
	"mounts_services_gen.go": true,
	"providers.go":           true,
	"auth.go":                true,
	// The native sign-in API: codegen.GenerateLoginBroker renders
	// app-login-broker.go.tmpl, so a scaffolded project has this beside
	// auth.go. The skills must be able to name it — it is where the whole
	// server-side OIDC flow lives.
	"login_broker.go": true,
	// The composition graph's forge-owned halves (codegen.GenerateGraph): the
	// memoized accessors, Build<Root>/Run<Root>, and the per-audience
	// routes. A graph-mode project has these instead of
	// mounts_services_gen.go, which GenerateGraph retires.
	"graph_gen.go":  true,
	"routes_gen.go": true,
}

// knownCodegenPkgAppFiles are pkg/app/ files written by `forge generate`
// emitters rather than the initial scaffold.
var knownCodegenPkgAppFiles = map[string]bool{
	"wire_gen.go":        true,
	"app_gen.go":         true,
	"diagnostics_gen.go": true,
	"migrate.go":         true,
	"setup.go":           true,
	"bootstrap.go":       true,
	"testing.go":         true,
	"app_extras.go":      true,
	// The user-owned service registry `forge generate` scaffolds and every
	// serve-decision site reads — see serviceRegistryRelPath in
	// internal/cli/generate_serve.go.
	"services.go": true,
}

// trimPathRef strips trailing punctuation a markdown sentence glues onto
// a path reference.
func trimPathRef(ref string) string {
	return strings.TrimRight(ref, ".,:;)('\"")
}

// pathRefs returns pathRefRE's matches in content, minus a .forge/ match
// nested under some other directory: `~/.forge/ledger` or
// `$FORGE_HOME/.forge/x` names a machine-wide directory, not the project's
// .forge/, so checking it against the scaffold tree is a false positive.
// `./.forge/x` stays a project reference.
func pathRefs(content string) []string {
	var out []string
	for _, loc := range pathRefRE.FindAllStringIndex(content, -1) {
		match := content[loc[0]:loc[1]]
		if strings.HasPrefix(match, ".forge/") && loc[0] > 0 && content[loc[0]-1] == '/' &&
			(loc[0] < 2 || content[loc[0]-2] != '.') {
			continue
		}
		out = append(out, match)
	}
	return out
}

// segmentsMatch reports whether ref (with <placeholder>/{placeholder}/*
// segments treated as single-segment wildcards) matches any path in tree.
func segmentsMatch(tree map[string]bool, ref string) bool {
	refSegs := strings.Split(strings.Trim(ref, "/"), "/")
	wild := func(s string) bool {
		return s == "*" || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "{")
	}
outer:
	for p := range tree {
		segs := strings.Split(p, "/")
		if len(segs) != len(refSegs) {
			continue
		}
		for i := range segs {
			if wild(refSegs[i]) {
				continue
			}
			if segs[i] != refSegs[i] {
				continue outer
			}
		}
		return true
	}
	return false
}

// validatePathRef returns "" when the reference is plausible, else a
// reason. Validation is deliberately scoped to claims that are cheap and
// unambiguous to check:
//   - .forge/<entry>: entry must be something forge writes.
//   - <root>/<dir>/...: the two-segment directory must be one a forge
//     project has. A reference to the mirror of a real directory under
//     the other root (internal/config for pkg/config) fails with the real
//     location named — that is the shape a stale skill drifts into.
//   - files under a filesFixed directory (pkg/app, pkg/config,
//     internal/app): forge writes every file there, so the base name must
//     be scaffolded or a known emitter output.
//   - handlers/.../<x>_gen.go: generated-file names must match a real
//     emitter (the rest of handlers/<svc>/ is mixed user territory, so
//     non-_gen references are treated as examples, not claims).
func validatePathRef(tree map[string]bool, ref string) string {
	if strings.HasPrefix(ref, ".forge/") {
		rest := strings.TrimPrefix(ref, ".forge/")
		first := strings.SplitN(rest, "/", 2)[0]
		if first == "" || placeholder(first) {
			return ""
		}
		if !knownDotForgeEntries[first] {
			return ".forge/" + first + " is not something forge writes"
		}
		return ""
	}

	segs := strings.Split(strings.Trim(ref, "/"), "/")
	// A bare "handlers/..." reference is the same territory as
	// internal/handlers/... — the skills use both spellings.
	if segs[0] == "handlers" {
		segs = append([]string{"internal"}, segs...)
	}
	if len(segs) < 2 {
		return ""
	}
	dir := segs[0] + "/" + segs[1]
	owned, ok := forgeOwnedDirs[dir]
	if !ok {
		if _, mirrored := forgeOwnedDirs[mirrorRoot(dir)]; mirrored {
			return dir + "/ is not a directory forge projects have — it lives at " + mirrorRoot(dir) + "/"
		}
		return ""
	}

	rest := strings.Join(segs[2:], "/")
	if rest == "" {
		return "" // bare directory reference
	}
	switch owned.policy {
	case filesMixed:
		return ""
	case filesFixed:
		if !strings.Contains(rest, ".") {
			return "" // a subdirectory, not a file claim
		}
		if placeholder(rest) {
			return "" // placeholder file reference, can't check precisely
		}
		if owned.files[rest] || segmentsMatch(tree, dir+"/"+rest) {
			return ""
		}
		return dir + "/" + rest + " is not scaffolded or emitted by forge (" +
			dir + "/ holds: " + strings.Join(sortedKeys(owned.files), ", ") + ")"
	case filesGenOnly:
		base := filepath.Base(rest)
		if !strings.HasSuffix(base, "_gen.go") && !strings.HasSuffix(base, "_gen_test.go") {
			return "" // user-territory example file or directory — not a codegen claim
		}
		if placeholder(base) {
			return ""
		}
		if !knownGeneratedHandlerFiles[base] {
			return dir + "/.../" + base + " is not a file forge generates"
		}
	}
	return ""
}

// placeholder reports whether a path segment is a doc stand-in
// (<svc>, {name}, *) rather than a literal forge emits.
func placeholder(s string) bool { return strings.ContainsAny(s, "<{*") }

// bareHandlerGenRE catches an unqualified per-service generated-handler
// file name — skills name `handlers_crud_ops_gen.go` mid-sentence far more
// often than they spell the whole path, and a bare name is exactly as
// wrong when no emitter produces it.
var bareHandlerGenRE = regexp.MustCompile(`(?:^|[^/\w-])(handlers[a-z0-9_]*_gen(?:_test)?\.go)`)

func TestSkillsPathReferencesExist(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every shipped skill against a scaffolded project; runs in task test")
	}
	skills := shippedSkills(t)
	allow := allowlist(t)
	tree := scaffoldTree(t)

	for rel, content := range skills {
		seen := map[string]bool{}
		report := func(ref, reason string) {
			if reason == "" || seen[ref] || allowed(allow, rel, ref) {
				return
			}
			seen[ref] = true
			t.Errorf("skills/%s: stale path reference %q — %s\n  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
				rel, ref, reason)
		}
		for _, raw := range pathRefs(content) {
			ref := trimPathRef(raw)
			if ref == "" {
				continue
			}
			report(ref, validatePathRef(tree, ref))
		}
		for _, m := range bareHandlerGenRE.FindAllStringSubmatch(content, -1) {
			if knownGeneratedHandlerFiles[m[1]] {
				continue
			}
			report(m[1], m[1]+" is not a file forge generates (per-service generated files: "+
				strings.Join(sortedKeys(knownGeneratedHandlerFiles), ", ")+")")
		}
	}
}

// ─── validator 3: forge.v1 annotation field references ──────────────────

// annotationDescriptors maps the extension names skills use —
// (forge.v1.entity) etc. — to the protoreflect descriptors of their real
// payload messages. These descriptors come from the generated package
// pkg/forgepb, itself generated from
// internal/assets/proto/forge/v1/forge.proto, so the schema is the truth:
// renaming/removing a proto field makes any skill that still documents
// the old name fail here.
func annotationDescriptors() map[string]protoreflect.MessageDescriptor {
	return map[string]protoreflect.MessageDescriptor{
		"entity":          (&forgev1.EntityOptions{}).ProtoReflect().Descriptor(),
		"field":           (&forgev1.FieldOptions{}).ProtoReflect().Descriptor(),
		"service":         (&forgev1.ServiceOptions{}).ProtoReflect().Descriptor(),
		"method":          (&forgev1.MethodOptions{}).ProtoReflect().Descriptor(),
		"config":          (&forgev1.ConfigFieldOptions{}).ProtoReflect().Descriptor(),
		"binary_config":   (&forgev1.BinaryConfigOptions{}).ProtoReflect().Descriptor(),
		"frontend_config": (&forgev1.FrontendConfigOptions{}).ProtoReflect().Descriptor(),
	}
}

// annotationRegions returns each fenced code block as ONE string (so
// multi-line annotation literals stay intact) plus every inline code
// span. Compare codeRegions, which is line-oriented.
func annotationRegions(md string) []string {
	var regions []string
	var fence []string
	inFence := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence && len(fence) > 0 {
				regions = append(regions, strings.Join(fence, "\n"))
				fence = nil
			}
			inFence = !inFence
			continue
		}
		if inFence {
			fence = append(fence, line)
			continue
		}
		for _, m := range inlineCodeRE.FindAllStringSubmatch(line, -1) {
			regions = append(regions, m[1])
		}
	}
	if inFence && len(fence) > 0 {
		regions = append(regions, strings.Join(fence, "\n"))
	}
	return regions
}

// annotationLiteralRE matches the head of an annotation literal:
// "(forge.v1.entity) = {". The trailing brace index anchors the parser.
var annotationLiteralRE = regexp.MustCompile(`\(forge\.v1\.([a-z_]+)\)\s*=\s*\{`)

// annotationDottedRE matches dotted field access like
// "(forge.v1.method).auth_required".
var annotationDottedRE = regexp.MustCompile(`\(forge\.v1\.([a-z_]+)\)((?:\.[A-Za-z_][A-Za-z0-9_]*)+)`)

type annViolation struct {
	claim  string // e.g. "(forge.v1.entity).table_name" — the allowlist key
	reason string
}

func joinFieldPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func fieldNames(desc protoreflect.MessageDescriptor) string {
	fields := desc.Fields()
	names := make([]string, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		names = append(names, string(fields.Get(i).Name()))
	}
	return strings.Join(names, ", ")
}

func enumValueNames(ed protoreflect.EnumDescriptor) string {
	vals := ed.Values()
	names := make([]string, 0, vals.Len())
	for i := 0; i < vals.Len(); i++ {
		names = append(names, string(vals.Get(i).Name()))
	}
	return strings.Join(names, ", ")
}

// annLitParser walks a proto-text-format-ish `{ key: value ... }` literal
// (nesting via {} and [], `//` and `#` comments, optional commas, colon
// optional before message values) and validates every field key against
// the descriptor of its enclosing message. It is deliberately small and
// lenient: anything it can't tokenize (e.g. a `...` ellipsis in a doc)
// aborts validation of that literal rather than guessing.
type annLitParser struct {
	ext string
	src string
	pos int
	out []annViolation
}

func (p *annLitParser) violation(path, reason string) {
	p.out = append(p.out, annViolation{claim: "(forge.v1." + p.ext + ")." + path, reason: reason})
}

// skipSep skips whitespace, pair separators (commas/semicolons), and
// comments — the stuff allowed BETWEEN key/value pairs.
func (p *annLitParser) skipSep() {
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == ';':
			p.pos++
		case c == '#' || (c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/'):
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

// skipSpace skips only horizontal whitespace — used between a field name
// and its (potential) value, where a comma/newline instead means the doc
// mentioned the field name without a value (shorthand like
// `{ sensitive, category }`), which is fine.
func (p *annLitParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *annLitParser) ident() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			p.pos++
			continue
		}
		break
	}
	return p.src[start:p.pos]
}

// scalarToken consumes one scalar value: a quoted string or a bare
// identifier/number token. Returns the token and whether it was quoted.
// Consumes one character on unparseable input so callers always progress.
func (p *annLitParser) scalarToken() (tok string, quoted bool) {
	if p.pos >= len(p.src) {
		return "", false
	}
	if q := p.src[p.pos]; q == '"' || q == '\'' {
		p.pos++
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] != q {
			if p.src[p.pos] == '\\' {
				p.pos++
			}
			p.pos++
		}
		tok = p.src[start:min(p.pos, len(p.src))]
		if p.pos < len(p.src) {
			p.pos++ // closing quote
		}
		return tok, true
	}
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '_' || c == '.' || c == '+' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			p.pos++
			continue
		}
		break
	}
	if p.pos == start {
		p.pos++ // unparseable char — consume so the caller progresses
		return "", false
	}
	return p.src[start:p.pos], false
}

// parseMessage validates the `{ ... }` literal at p.pos against desc.
// A nil desc means "unknown message" (the field key was already flagged);
// structure is consumed but keys aren't checked.
func (p *annLitParser) parseMessage(desc protoreflect.MessageDescriptor, prefix string) {
	if p.pos >= len(p.src) || p.src[p.pos] != '{' {
		return
	}
	p.pos++
	for {
		p.skipSep()
		if p.pos >= len(p.src) {
			return // unterminated literal (doc elided the rest) — lenient
		}
		if p.src[p.pos] == '}' {
			p.pos++
			return
		}
		name := p.ident()
		if name == "" {
			// Unparseable content (e.g. a "..." ellipsis). Stop validating
			// this literal rather than guessing.
			p.pos = len(p.src)
			return
		}
		path := joinFieldPath(prefix, name)
		var fd protoreflect.FieldDescriptor
		if desc != nil {
			fd = desc.Fields().ByName(protoreflect.Name(name))
			if fd == nil {
				p.violation(path, fmt.Sprintf("forge.v1.%s has no field %q (fields: %s)", desc.Name(), name, fieldNames(desc)))
			}
		}
		p.skipSpace()
		switch {
		case p.pos < len(p.src) && p.src[p.pos] == ':':
			p.pos++
			p.skipSep()
			p.parseValue(fd, path)
		case p.pos < len(p.src) && (p.src[p.pos] == '{' || p.src[p.pos] == '['):
			// text format allows `auth { ... }` with no colon
			p.parseValue(fd, path)
		default:
			// Name-only mention (doc shorthand) — no value to validate.
		}
	}
}

func (p *annLitParser) parseValue(fd protoreflect.FieldDescriptor, path string) {
	if p.pos >= len(p.src) {
		return
	}
	switch p.src[p.pos] {
	case '{':
		var sub protoreflect.MessageDescriptor
		if fd != nil {
			if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
				sub = fd.Message()
			} else {
				p.violation(path, fmt.Sprintf("%s is a %s field — it does not take a { ... } message literal", path, fd.Kind()))
			}
		}
		p.parseMessage(sub, path)
	case '[':
		p.pos++
		for {
			p.skipSep()
			if p.pos >= len(p.src) {
				return
			}
			if p.src[p.pos] == ']' {
				p.pos++
				return
			}
			p.parseValue(fd, path)
		}
	default:
		tok, quoted := p.scalarToken()
		p.checkScalar(fd, path, tok, quoted)
	}
}

func (p *annLitParser) checkScalar(fd protoreflect.FieldDescriptor, path, tok string, quoted bool) {
	if fd == nil || (tok == "" && !quoted) {
		return
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		p.violation(path, fmt.Sprintf("%s is a message (%s) — use %s: { ... } with fields %s, not a scalar value",
			path, fd.Message().FullName(), path, fieldNames(fd.Message())))
	case protoreflect.EnumKind:
		if quoted || fd.Enum().Values().ByName(protoreflect.Name(tok)) == nil {
			p.violation(path, fmt.Sprintf("%s is enum %s — value must be one of: %s (got %q)",
				path, fd.Enum().Name(), enumValueNames(fd.Enum()), tok))
		}
	case protoreflect.BoolKind:
		if tok != "true" && tok != "false" {
			p.violation(path, fmt.Sprintf("%s is a bool field — value must be true or false (got %q)", path, tok))
		}
	}
}

// scanAnnotationRegion finds every (forge.v1.<ext>) reference in one code
// region and returns the violations.
func scanAnnotationRegion(region string, descs map[string]protoreflect.MessageDescriptor) []annViolation {
	var out []annViolation
	for _, m := range annotationLiteralRE.FindAllStringSubmatchIndex(region, -1) {
		ext := region[m[2]:m[3]]
		desc, ok := descs[ext]
		if !ok {
			out = append(out, annViolation{
				claim:  "(forge.v1." + ext + ")",
				reason: "no such forge.v1 extension (valid: entity, field, service, method, config, binary_config, frontend_config)",
			})
			continue
		}
		p := &annLitParser{ext: ext, src: region, pos: m[1] - 1} // m[1]-1 = the '{'
		p.parseMessage(desc, "")
		out = append(out, p.out...)
	}
	for _, m := range annotationDottedRE.FindAllStringSubmatch(region, -1) {
		ext, chain := m[1], m[2]
		desc, ok := descs[ext]
		if !ok {
			out = append(out, annViolation{
				claim:  "(forge.v1." + ext + ")",
				reason: "no such forge.v1 extension (valid: entity, field, service, method, config, binary_config, frontend_config)",
			})
			continue
		}
		cur, path := desc, ""
		for _, seg := range strings.Split(strings.TrimPrefix(chain, "."), ".") {
			if cur == nil {
				break
			}
			path = joinFieldPath(path, seg)
			fd := cur.Fields().ByName(protoreflect.Name(seg))
			if fd == nil {
				out = append(out, annViolation{
					claim:  "(forge.v1." + ext + ")." + path,
					reason: fmt.Sprintf("forge.v1.%s has no field %q (fields: %s)", cur.Name(), seg, fieldNames(cur)),
				})
				break
			}
			if fd.Kind() == protoreflect.MessageKind {
				cur = fd.Message()
			} else {
				cur = nil
			}
		}
	}
	return out
}

// annotationDocs returns the docs this validator covers: every shipped
// SKILL.md (keyed as in the allowlist) plus reliant.md.tmpl, which ships
// the same annotation guidance.
func annotationDocs(t *testing.T) map[string]string {
	t.Helper()
	docs := map[string]string{}
	for rel, content := range shippedSkills(t) {
		docs[rel] = content
	}
	content, err := templates.ProjectTemplates().Get("reliant.md.tmpl")
	if err != nil {
		t.Fatalf("read reliant.md.tmpl: %v", err)
	}
	docs["reliant.md.tmpl"] = string(content)
	return docs
}

func TestSkillsAnnotationFieldReferencesExist(t *testing.T) {
	docs := annotationDocs(t)
	allow := allowlist(t)
	descs := annotationDescriptors()

	for rel, content := range docs {
		seen := map[string]bool{}
		for _, region := range annotationRegions(content) {
			for _, v := range scanAnnotationRegion(region, descs) {
				key := v.claim + "|" + v.reason
				if seen[key] {
					continue
				}
				seen[key] = true
				if allowed(allow, rel, v.claim) {
					continue
				}
				t.Errorf("%s: invalid forge.v1 annotation reference %q — %s\n  (schema truth: internal/assets/proto/forge/v1/forge.proto; fix the doc, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
					rel, v.claim, v.reason)
			}
		}
	}
}

// TestSkillsValidatorsCatchKnownBadClaims is a self-test: if the
// extraction or validation logic regresses to a no-op, this trips before
// the suite silently stops catching drift.
func TestSkillsValidatorsCatchKnownBadClaims(t *testing.T) {
	root := cli.NewRootCmd()
	tree := scaffoldTree(t)

	// Command validator.
	if reason := validateCommandChain(root, []string{"frobnicate"}); reason == "" {
		t.Error("validateCommandChain accepted a nonexistent top-level subcommand")
	}
	if reason := validateCommandChain(root, []string{"skill", "frobnicate"}); reason == "" {
		t.Error("validateCommandChain accepted a nonexistent subcommand of a pure group")
	}
	if reason := validateCommandChain(root, []string{"generate"}); reason != "" {
		t.Errorf("validateCommandChain rejected `forge generate`: %s", reason)
	}
	if reason := validateCommandChain(root, []string{"skill", "load", "db"}); reason != "" {
		t.Errorf("validateCommandChain rejected positional arg after runnable cmd: %s", reason)
	}

	// Extraction: a fenced block and an inline span must both surface.
	md := "prose\n```bash\nforge frobnicate now\n```\nand `forge bogus-cmd` inline\n"
	var found []string
	for _, region := range codeRegions(md) {
		for _, m := range forgeCmdChainRE.FindAllStringSubmatch(region, -1) {
			found = append(found, m[2])
		}
	}
	if len(found) != 2 {
		t.Errorf("codeRegions+regex extracted %d command refs from synthetic doc, want 2: %v", len(found), found)
	}

	// Path validator: claims forge cannot honour.
	for _, bad := range []string{
		"pkg/app/no_such_file_ever.go",
		".forge/not-a-real-thing.json",
		"handlers/users/imaginary_gen.go",
		// handlers_gen.go is the file codegen states it emits "never
		// again" (internal/codegen/service_stub_gen.go): missing RPC stubs
		// are scaffolded straight into the user-owned handlers.go.
		"internal/handlers/users/handlers_gen.go",
		// The composition layer is forge's; testing.go lives in pkg/app.
		"internal/app/testing.go",
		// Wrong-home claims: the real homes are pkg/config, pkg/middleware.
		"internal/config/config.go",
		"internal/middleware/middleware.go",
	} {
		if reason := validatePathRef(tree, bad); reason == "" {
			t.Errorf("validatePathRef accepted %q, which forge never produces", bad)
		}
	}
	// ...and the real ones it must keep accepting.
	for _, good := range []string{
		"pkg/app/bootstrap.go",
		"pkg/app/testing.go",
		"pkg/config/config_gen.go",
		"pkg/middleware/middleware.go",
		"internal/app/compose.go",
		"internal/app/providers.go",
		"internal/db/user_orm_gen.go",
		"internal/handlers/<svc>/handlers_crud_ops_gen.go",
		"handlers/<svc>/handlers_crud_gen.go",
		"handlers/users/service.go",
	} {
		if reason := validatePathRef(tree, good); reason != "" {
			t.Errorf("validatePathRef rejected %q, which forge does produce: %s", good, reason)
		}
	}
	// The regex has to REACH the internal/ roots — an unreachable claim is
	// the exact hole that let internal/config/ and internal/app/testing.go
	// ship unnoticed.
	for _, want := range []string{"internal/app/testing.go", "internal/config/config.go", "pkg/middleware/x.go"} {
		if !slices.Contains(pathRefRE.FindAllString("see "+want+" here", -1), want) {
			t.Errorf("pathRefRE does not extract %q — the claim would be structurally invisible", want)
		}
	}
	// A .forge/ under another directory is machine state, not the project's.
	for text, want := range map[string]bool{
		"default `~/.forge/ledger`":      false,
		"under $FORGE_HOME/.forge/cache": false,
		"see .forge/logs/dev/x.log":      true,
		"see ./.forge/logs/dev/x.log":    true,
		"(`.forge/hostinfra/idp/z.log`)": true,
	} {
		if got := len(pathRefs(text)) > 0; got != want {
			t.Errorf("pathRefs(%q) extracted=%v, want %v", text, got, want)
		}
	}

	// Annotation validator: known-bad literals must be flagged.
	descs := annotationDescriptors()
	badMD := "```proto\n" +
		"message Task {\n" +
		"  option (forge.v1.entity) = {\n" +
		"    table_name: \"tasks\"    // no such field (real name: table)\n" +
		"    timestamps: true\n" +
		"  };\n" +
		"  string title = 1 [(forge.v1.field) = { validate: \"url\" }];\n" + // message field given a scalar
		"  string email = 2 [(forge.v1.field) = { store: true }];\n" + // enum field given a bool
		"}\n" +
		"```\n"
	var claims []string
	for _, region := range annotationRegions(badMD) {
		for _, v := range scanAnnotationRegion(region, descs) {
			claims = append(claims, v.claim)
		}
	}
	for _, want := range []string{
		"(forge.v1.entity).table_name",
		"(forge.v1.field).validate",
		"(forge.v1.field).store",
	} {
		if !slices.Contains(claims, want) {
			t.Errorf("annotation validator missed known-bad claim %s (got %v)", want, claims)
		}
	}
	if len(claims) != 3 {
		t.Errorf("annotation validator reported unexpected extra violations: %v", claims)
	}

	// Correct literals — including nested messages, lists, enum values,
	// colon-less message fields, and dotted references — must pass.
	goodMD := "```proto\n" +
		"message Task {\n" +
		"  option (forge.v1.entity) = {\n" +
		"    table: \"tasks\"\n" +
		"    soft_delete: true\n" +
		"    indexes: [{ name: \"by_org\", fields: [\"org_id\", \"status\"], unique: true }]\n" +
		"    middleware: [\"tracing\", \"metrics\"]\n" +
		"  };\n" +
		"  string url = 1 [(forge.v1.field) = { store: STORE_AS_JSONB, validate: { format: \"url\", min_length: 3 } }];\n" +
		"}\n" +
		"```\n" +
		"and `(forge.v1.service) = { name: \"tasks\" version: \"v1\" auth { auth_required: true } }`\n" +
		"and `(forge.v1.method) = { timeout: { seconds: 30 }, errors: [\"NotFound\"] }`\n" +
		"and `(forge.v1.method).auth_required = false` plus shorthand `(forge.v1.config) = { sensitive, category }`\n"
	for _, region := range annotationRegions(goodMD) {
		for _, v := range scanAnnotationRegion(region, descs) {
			t.Errorf("annotation validator flagged a correct reference: %s — %s", v.claim, v.reason)
		}
	}

	// Dotted references to nonexistent fields must be flagged.
	if vs := scanAnnotationRegion("(forge.v1.method).nonexistent_field", descs); len(vs) == 0 {
		t.Error("annotation validator accepted dotted reference to nonexistent field (forge.v1.method).nonexistent_field")
	}
}

// TestSkillsAllowlistEntriesStillNeeded keeps the allowlist from rotting:
// an entry whose claim no longer appears in the named skill (or whose
// skill no longer exists) must be removed.
func TestSkillsAllowlistEntriesStillNeeded(t *testing.T) {
	docs := annotationDocs(t) // every SKILL.md + reliant.md.tmpl
	allow := allowlist(t)
	for skillRel, claims := range allow {
		if skillRel == "*" {
			continue
		}
		content, ok := docs[skillRel]
		if !ok {
			t.Errorf("allowlist names skill %q which no longer ships", skillRel)
			continue
		}
		for claim := range claims {
			// The claim text is a substring of the skill in all three
			// validators' extraction paths. Annotation claims look like
			// "(forge.v1.entity).table_name" — the doc contains the final
			// field name, not the synthesized dotted form.
			needle := strings.TrimPrefix(claim, "forge ")
			if strings.HasPrefix(claim, "(forge.v1.") {
				if i := strings.LastIndex(claim, "."); i >= 0 {
					needle = claim[i+1:]
				}
			}
			if !strings.Contains(content, needle) {
				t.Errorf("allowlist entry %q|%q no longer matches anything in the skill — remove it", skillRel, claim)
			}
		}
	}
}

// ─── validator 4: `forge skill load <path>` targets ─────────────────────

// shippedSkillPaths returns the set of paths `forge skill load` resolves
// against the embedded skills. The derivation mirrors
// listForgeShippedSkills in internal/cli/skill.go: skills/forge/<rest>
// and skills/general/<rest> both collapse to "<rest>", and
// skills/forge/SKILL.md is the synthetic "forge" parent.
func shippedSkillPaths(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for rel := range shippedSkills(t) {
		switch {
		case rel == "forge/SKILL.md":
			out["forge"] = true
		case strings.HasPrefix(rel, "forge/"):
			out[strings.TrimSuffix(strings.TrimPrefix(rel, "forge/"), "/SKILL.md")] = true
		case strings.HasPrefix(rel, "general/"):
			out[strings.TrimSuffix(strings.TrimPrefix(rel, "general/"), "/SKILL.md")] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("derived no skill paths from the embedded skills")
	}
	return out
}

// templateTreeDocs returns rel-path → content for every file under the
// on-disk template tree (the same bytes //go:embed ships). Files at the
// package root are skipped: those are this package's own .go sources, not
// templates. Scanning the whole tree matters because scaffolded code
// points readers at skills from its header comments, and those pointers
// rot exactly like the ones inside SKILL.md.
func templateTreeDocs(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel := filepath.ToSlash(p)
		if !strings.Contains(rel, "/") {
			return nil // package-root .go files, including this test
		}
		if strings.HasPrefix(rel, "testdata/") {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk template tree: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("template tree walk found no files")
	}
	return out
}

var skillLoadRE = regexp.MustCompile(`forge skill load ([a-z0-9][a-z0-9/_.-]*)`)

func TestSkillLoadReferencesResolve(t *testing.T) {
	known := shippedSkillPaths(t)
	for rel, content := range templateTreeDocs(t) {
		seen := map[string]bool{}
		for _, m := range skillLoadRE.FindAllStringSubmatch(content, -1) {
			ref := strings.TrimRight(m[1], ".,;:)")
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			// `forge skill load` accepts an optional "forge/" prefix.
			if known[ref] || known[strings.TrimPrefix(ref, "forge/")] {
				continue
			}
			t.Errorf("%s: `forge skill load %s` names a skill forge does not ship\n"+
				"  (fix the reference, or add the skill under internal/templates/project/skills/)", rel, ref)
		}
	}
}

// ─── validator 5: forge/pkg subpackage references ───────────────────────

// forgePkgSubpackages returns the real subpackage names of the forge/pkg
// module, read off disk (pkg/ is a sibling module, so there is nothing to
// import-reflect).
func forgePkgSubpackages(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "..", "pkg"))
	if err != nil {
		t.Fatalf("read forge/pkg: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out[e.Name()] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("forge/pkg has no subpackages — truth source is wrong")
	}
	return out
}

var forgePkgRefRE = regexp.MustCompile(`forge/pkg/([a-z][a-z0-9_]*)`)

func TestSkillsForgePkgReferencesExist(t *testing.T) {
	pkgs := forgePkgSubpackages(t)
	allow := allowlist(t)
	for rel, content := range shippedSkills(t) {
		seen := map[string]bool{}
		for _, m := range forgePkgRefRE.FindAllStringSubmatch(content, -1) {
			claim := "forge/pkg/" + m[1]
			if seen[claim] {
				continue
			}
			seen[claim] = true
			if pkgs[m[1]] || allowed(allow, rel, claim) {
				continue
			}
			t.Errorf("skills/%s: %s is not a forge/pkg subpackage\n"+
				"  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)", rel, claim)
		}
	}
}

// ─── validator 11: package-qualified auth identifiers ───────────────────

// authSymbolRefRE matches a package-qualified exported identifier in the
// auth surface a skill teaches: forge/pkg/{auth,authn,apikey} and the
// middleware package (the project's scaffolded pkg/middleware, or forge's
// own pkg/middleware). Validator 5 proves the PACKAGE exists; this proves
// the SYMBOL does. The auth/api-keys skill once told readers to implement
// auth.KeyValidator and pass Provider: "both" into a SetupAuth that had no
// place for either (issue #746) — symbols are the cheap half of that drift.
var authSymbolRefRE = regexp.MustCompile(`\b(auth|authn|apikey|middleware)\.([A-Z][A-Za-z0-9_]*)`)

// exportedIdents returns every exported top-level identifier (funcs, types,
// vars, consts) declared by the non-test Go files of dir.
func exportedIdents(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files under %s (err %v) — truth source is wrong", dir, err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		addExportedDecls(file, out)
	}
	return out
}

// addExportedDecls records the exported top-level names file declares.
// Methods are skipped: `pkg.Name` never names one.
func addExportedDecls(file *ast.File, into map[string]bool) {
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				into[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						into[s.Name.Name] = true
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							into[n.Name] = true
						}
					}
				}
			}
		}
	}
}

// scaffoldedMiddlewareIdents returns what the project's pkg/middleware
// declares: the owned policy file plus the generated allow-list.
func scaffoldedMiddlewareIdents(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	for name, data := range map[string]any{
		"middleware.go.tmpl":            struct{ Name string }{Name: "demo"},
		"middleware-procedures.go.tmpl": struct{ Imports, Open, Deadlines []struct{} }{},
	} {
		src, err := templates.ProjectTemplates().Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse rendered %s: %v", name, err)
		}
		addExportedDecls(file, out)
	}
	return out
}

func TestSkillsAuthSymbolReferencesExist(t *testing.T) {
	pkgDir := filepath.Join("..", "..", "pkg")
	known := map[string]map[string]bool{
		"auth":   exportedIdents(t, filepath.Join(pkgDir, "auth")),
		"authn":  exportedIdents(t, filepath.Join(pkgDir, "authn")),
		"apikey": exportedIdents(t, filepath.Join(pkgDir, "apikey")),
	}
	// middleware.X is the project's own pkg/middleware in handler code and
	// forge's pkg/middleware in serve.go; either declaring it is honest.
	known["middleware"] = scaffoldedMiddlewareIdents(t)
	for name := range exportedIdents(t, filepath.Join(pkgDir, "middleware")) {
		known["middleware"][name] = true
	}

	allow := allowlist(t)
	for rel, content := range shippedSkills(t) {
		seen := map[string]bool{}
		for _, m := range authSymbolRefRE.FindAllStringSubmatch(content, -1) {
			claim := m[1] + "." + m[2]
			if seen[claim] {
				continue
			}
			seen[claim] = true
			if known[m[1]][m[2]] || allowed(allow, rel, claim) {
				continue
			}
			t.Errorf("skills/%s: %s names no exported symbol of package %s\n"+
				"  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
				rel, claim, m[1])
		}
	}
}

// TestAuthSkillsProviderClaimsMatchTheScaffold pins the exact claim issue
// #746 found stale. The auth skills describe internal/app/auth.go, and the
// api-keys skill said to pass `Provider: "both"` to the SetupAuth that file
// declares — which builds a JWT validator only and has no place for a key
// validator. Every `Provider: "<name>"` an auth skill shows must therefore be
// one the scaffolded file actually sets.
func TestAuthSkillsProviderClaimsMatchTheScaffold(t *testing.T) {
	authGo, err := templates.ProjectTemplates().Render("app-auth.go.tmpl", struct {
		Module             string
		Graph, HasDatabase bool
		NoJWTConfig        bool
		AuthConfigType     string
	}{Module: "example.com/demo"})
	if err != nil {
		t.Fatalf("render app-auth.go.tmpl: %v", err)
	}
	providerRE := regexp.MustCompile(`Provider:\s*"[a-z_]*"`)
	scaffolded := map[string]bool{}
	for _, m := range providerRE.FindAllString(string(authGo), -1) {
		scaffolded[strings.Join(strings.Fields(m), " ")] = true
	}
	if len(scaffolded) == 0 {
		t.Fatal("the scaffolded internal/app/auth.go sets no Provider — truth source is wrong")
	}
	for rel, content := range shippedSkills(t) {
		if !strings.HasPrefix(rel, "forge/auth/") {
			continue
		}
		for _, m := range providerRE.FindAllString(content, -1) {
			if claim := strings.Join(strings.Fields(m), " "); !scaffolded[claim] {
				t.Errorf("skills/%s shows %s, which the scaffolded internal/app/auth.go never sets (it sets %v) — "+
					"describe the wiring forge generates", rel, claim, scaffolded)
			}
		}
	}
}

// ─── validator 6: audit --json category references ──────────────────────

var auditCategoryRefRE = regexp.MustCompile(`\.categories\.([a-z][a-z0-9_]*)`)

func TestSkillsAuditCategoryReferencesExist(t *testing.T) {
	known := map[string]bool{}
	for _, name := range audit.CategoryNames() {
		known[name] = true
	}
	allow := allowlist(t)
	for rel, content := range shippedSkills(t) {
		seen := map[string]bool{}
		for _, m := range auditCategoryRefRE.FindAllStringSubmatch(content, -1) {
			claim := ".categories." + m[1]
			if seen[claim] {
				continue
			}
			seen[claim] = true
			if known[m[1]] || allowed(allow, rel, claim) {
				continue
			}
			t.Errorf("skills/%s: %s — `forge project audit --json` emits no such category (real set: %s)\n"+
				"  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
				rel, claim, strings.Join(audit.CategoryNames(), ", "))
		}
	}
}

// TestSkillsAuditCategoryDocsMatchEmittedSet closes the hole the jq-path
// regex above leaves open. That regex only sees `.categories.<name>`
// spellings, so a category named in the SHAPE EXAMPLE or in the category
// reference table slipped past it — which is how `wire_coverage` survived
// its own deletion, documented as "always `ok` here" when the emitter had
// stopped producing it at all. A category that is documented as always-ok
// and never emitted is exactly the vacuous green internal/vacuousguard
// exists to kill, one level up.
//
// Both sets are derived from structure the document itself carries — the
// keys of the `"categories": { ... }` object, and the first cell of the
// category table's rows — never from a hand-listed set, and the test fails
// loudly if either derivation comes back empty.
func TestSkillsAuditCategoryDocsMatchEmittedSet(t *testing.T) {
	known := map[string]bool{}
	for _, name := range audit.CategoryNames() {
		known[name] = true
	}
	const skill = "forge/audit-json/SKILL.md"
	content, ok := shippedSkills(t)[skill]
	if !ok {
		t.Fatalf("%s is not a shipped skill — this validator has lost its subject", skill)
	}

	documented := map[string]string{} // category → where it was found
	for _, name := range auditShapeExampleCategories(t, content) {
		documented[name] = `the "categories" shape example`
	}
	for _, name := range auditCategoryTableRows(t, content) {
		if _, seen := documented[name]; !seen {
			documented[name] = "the category reference table"
		}
	}
	if len(documented) == 0 {
		t.Fatalf("%s: found no documented categories at all — the shape example or the "+
			"category table was restructured and this validator now checks nothing", skill)
	}
	for name, where := range documented {
		if !known[name] {
			t.Errorf("skills/%s documents category %q in %s, but `forge project audit --json` emits no such category (real set: %s)",
				skill, name, where, strings.Join(audit.CategoryNames(), ", "))
		}
	}
}

// auditShapeExampleCategories returns the keys of the `"categories": { ... }`
// object in the skill's JSON shape example. The example is illustrative
// JSON (`{ ... }` placeholders), so it is read by brace depth rather than
// unmarshalled.
func auditShapeExampleCategories(t *testing.T, content string) []string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), `"categories": {`) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf(`audit-json skill: no "categories": { block in the shape example`)
	}
	var out []string
	keyRE := regexp.MustCompile(`^"([a-z][a-z0-9_]*)":`)
	for _, l := range lines[start+1:] {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "}") {
			break
		}
		if m := keyRE.FindStringSubmatch(trimmed); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// auditCategoryTableRows returns the root category named in the first cell
// of each row of the skill's category reference table — the table whose
// header cell is "Category". Anchoring on that header, rather than on any
// row that looks like one, keeps the other tables in the document (jq
// recipes, status values) out of the set. `shape.services[]` documents the
// `shape` category, so only the root before the first `.` is a name.
func auditCategoryTableRows(t *testing.T, content string) []string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "| Category |") {
			start = i + 2 // skip the header and its separator row
			break
		}
	}
	if start < 0 || start >= len(lines) {
		t.Fatal(`audit-json skill: no "| Category |" table header — the category reference table was restructured`)
	}
	cellRE := regexp.MustCompile("^`([a-z][a-z0-9_.\\[\\]]*)`$")
	var out []string
	for _, l := range lines[start:] {
		if !strings.HasPrefix(l, "|") {
			break // end of the table
		}
		cells := strings.Split(strings.Trim(l, "|"), "|")
		if len(cells) == 0 {
			continue
		}
		m := cellRE.FindStringSubmatch(strings.TrimSpace(cells[0]))
		if m == nil {
			continue
		}
		root, _, _ := strings.Cut(m[1], ".")
		out = append(out, root)
	}
	if len(out) == 0 {
		t.Fatal("audit-json skill: the category reference table matched no rows — it was restructured")
	}
	return out
}

// ─── validator 7: forge.yaml feature-flag references ────────────────────

// featureFlagRefRE matches `features.<name>` and
// `features.experimental.<name>` — the two shapes forge.yaml accepts and
// the two shapes the disabled-feature error messages print.
var featureFlagRefRE = regexp.MustCompile(`features\.(?:experimental\.)?([a-z][a-z0-9_]*)`)

func TestSkillsFeatureFlagReferencesExist(t *testing.T) {
	known := map[string]bool{}
	for name := range (config.FeaturesConfig{}).EffectiveFeatures() {
		known[name] = true
	}
	// `features.experimental:` is the block itself, and
	// `.categories.features.details` is the audit category, not a flag.
	known["experimental"] = true
	known["details"] = true

	allow := allowlist(t)
	for rel, content := range shippedSkills(t) {
		seen := map[string]bool{}
		for _, region := range codeRegions(content) {
			for _, m := range featureFlagRefRE.FindAllStringSubmatch(region, -1) {
				claim := "features." + m[1]
				if seen[claim] {
					continue
				}
				seen[claim] = true
				if known[m[1]] || allowed(allow, rel, claim) {
					continue
				}
				t.Errorf("skills/%s: %s is not a forge.yaml feature flag (real set: %s)\n"+
					"  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
					rel, claim, strings.Join(sortedKeys(known), ", "))
			}
		}
	}
}

// ─── validator 10: `forge <cmd> --flag` references ──────────────────────
//
// Validator 1 proves a command chain resolves; it says nothing about the
// flags spelled after it. A skill that names a flag forge does not have
// sends the reader to a hard "unknown flag" failure, and — worse — a flag
// whose SENSE has flipped (`--no-heal` after healing became opt-in
// `--heal`) documents the exact opposite of what forge does.

// flagInvocationRE captures a `forge <chain> <rest-of-line>` invocation:
// the lowercase word chain, then everything after it on the (joined) line
// where the flags live.
var flagInvocationRE = regexp.MustCompile(
	`(^|[^./\w-])forge\s+((?:[a-z][a-z0-9_-]*\s+)*[a-z][a-z0-9_-]*)((?:\s+(?:--?[A-Za-z][A-Za-z0-9_-]*|[^\s|&;]+))*)`)

// longFlagRE matches a long flag token. Short flags are skipped: a bare
// `-f` is as often a shell argument in prose as a forge flag.
var longFlagRE = regexp.MustCompile(`(^|\s)--([a-z][a-z0-9-]*)`)

// continuedRegions is codeRegions with backslash-continued shell lines
// folded together, so the flags on the tail lines of a multi-line
// invocation are attributed to the command that opens it.
func continuedRegions(md string) []string {
	var (
		out     []string
		pending string
	)
	for _, region := range codeRegions(md) {
		if trimmed := strings.TrimRight(region, " \t"); strings.HasSuffix(trimmed, `\`) {
			pending += strings.TrimSuffix(trimmed, `\`) + " "
			continue
		}
		out = append(out, pending+region)
		pending = ""
	}
	if pending != "" {
		out = append(out, pending)
	}
	return out
}

// resolveCommandChain walks as far down the cobra tree as the tokens go
// and returns the deepest command reached. Trailing tokens that name no
// child are positional args, which is exactly where the flags follow.
func resolveCommandChain(root *cobra.Command, tokens []string) *cobra.Command {
	cur := root
	for _, tok := range tokens {
		child := findChild(cur, tok)
		if child == nil {
			break
		}
		cur = child
	}
	return cur
}

// commandHasFlag asks the real command whether it would accept the flag —
// its own, its parents' persistent flags, and the root's global ones.
//
// InitDefaultHelpFlag first: cobra adds --help lazily at Execute time, so a
// tree built but never run reports no --help on any command and every
// `forge <cmd> --help` in the corpus would look stale.
func commandHasFlag(cmd *cobra.Command, name string) bool {
	cmd.InitDefaultHelpFlag()
	return cmd.Flags().Lookup(name) != nil ||
		cmd.InheritedFlags().Lookup(name) != nil ||
		cmd.PersistentFlags().Lookup(name) != nil
}

func TestSkillsForgeFlagReferencesExist(t *testing.T) {
	root := cli.NewRootCmd()
	allow := allowlist(t)

	for rel, content := range shippedSkills(t) {
		seen := map[string]bool{}
		for _, region := range continuedRegions(content) {
			for _, m := range flagInvocationRE.FindAllStringSubmatch(region, -1) {
				tokens := strings.Fields(m[2])
				if len(tokens) == 0 || versionWordRE.MatchString(tokens[0]) {
					continue
				}
				cmd := resolveCommandChain(root, tokens)
				if cmd == root {
					continue // chain resolved to nothing; validator 1 owns that
				}
				for _, fm := range longFlagRE.FindAllStringSubmatch(m[2]+" "+m[3], -1) {
					flag := fm[2]
					claim := "forge " + strings.Join(tokens, " ") + " --" + flag
					if seen[claim] {
						continue
					}
					seen[claim] = true
					if commandHasFlag(cmd, flag) || allowed(allow, rel, claim) {
						continue
					}
					t.Errorf("skills/%s: stale flag reference %q — `forge %s` has no --%s flag\n"+
						"  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
						rel, claim, cmd.CommandPath(), flag)
				}
			}
		}
	}
}

// TestFlagValidatorCatchesKnownBadClaims is the self-test for validator 10.
func TestFlagValidatorCatchesKnownBadClaims(t *testing.T) {
	root := cli.NewRootCmd()

	// A flag forge really has, on the command that really has it.
	gen := resolveCommandChain(root, []string{"generate"})
	if gen == root {
		t.Fatal("`forge generate` no longer resolves — the validator would go blind")
	}
	if !commandHasFlag(gen, "heal") {
		t.Error("`forge generate --heal` is gone; re-check the skills that document healing")
	}
	// ...and the inverted spelling that shipped in a skill.
	if commandHasFlag(gen, "no-heal") {
		t.Error("`forge generate --no-heal` exists again — the checksum-history skill's wording is back in play")
	}
	// A flag on a deep chain, reached through positional args.
	ent := resolveCommandChain(root, []string{"scaffold", "entity", "bookmark"})
	if ent.CommandPath() != "forge scaffold entity" {
		t.Errorf("resolveCommandChain stopped at %q, want `forge scaffold entity` (positional args must not derail it)", ent.CommandPath())
	}
	if !commandHasFlag(ent, "soft-delete") {
		t.Error("`forge scaffold entity --soft-delete` is gone; the db skill's flag table is now wrong")
	}
	// Global flags are inherited, not misses.
	if !commandHasFlag(ent, "project-dir") {
		t.Error("root persistent flags are not visible to the validator — every skill using one would false-positive")
	}
	// ...and so is cobra's lazily-added --help, on a command with no flags
	// of its own.
	if !commandHasFlag(resolveCommandChain(cli.NewRootCmd(), []string{"skill", "list"}), "help") {
		t.Error("--help is invisible to the validator — every `forge <cmd> --help` in the corpus would false-positive")
	}

	// Extraction: a flag on a backslash-continued tail line has to reach
	// the command that opened the invocation.
	md := "```bash\nforge project new my-app \\\n  --no-such-flag-at-all\n```\n"
	var found []string
	for _, region := range continuedRegions(md) {
		for _, m := range flagInvocationRE.FindAllStringSubmatch(region, -1) {
			for _, fm := range longFlagRE.FindAllStringSubmatch(m[2]+" "+m[3], -1) {
				found = append(found, fm[2])
			}
		}
	}
	if !slices.Contains(found, "no-such-flag-at-all") {
		t.Errorf("continued-line extraction lost the flag (got %v) — multi-line invocations would go unchecked", found)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestNewSkillValidatorsCatchKnownBadClaims is the self-test for
// validators 4-7: if an extraction regex or a truth source regresses to a
// no-op, this trips before the suite silently stops catching drift.
func TestNewSkillValidatorsCatchKnownBadClaims(t *testing.T) {
	extract := func(re *regexp.Regexp, doc string) []string {
		var out []string
		for _, m := range re.FindAllStringSubmatch(doc, -1) {
			out = append(out, m[1])
		}
		return out
	}

	// 4. skill-load targets.
	skills := shippedSkillPaths(t)
	got := extract(skillLoadRE, "see `forge skill load db` and `forge skill load nope/nope`")
	if len(got) != 2 {
		t.Errorf("skillLoadRE extracted %v, want 2 refs", got)
	}
	if !skills["db"] {
		t.Error("shippedSkillPaths lost the `db` skill — truth source is wrong")
	}
	if skills["nope/nope"] {
		t.Error("shippedSkillPaths accepted a nonexistent skill path")
	}

	// 5. forge/pkg subpackages.
	pkgs := forgePkgSubpackages(t)
	if got := extract(forgePkgRefRE, "import `forge/pkg/crud`"); len(got) != 1 || got[0] != "crud" {
		t.Errorf("forgePkgRefRE extracted %v, want [crud]", got)
	}
	if !pkgs["crud"] {
		t.Error("forgePkgSubpackages lost pkg/crud — truth source is wrong")
	}
	if pkgs["no_such_pkg"] {
		t.Error("forgePkgSubpackages accepted a nonexistent subpackage")
	}

	// 6. audit categories.
	cats := map[string]bool{}
	for _, n := range audit.CategoryNames() {
		cats[n] = true
	}
	if got := extract(auditCategoryRefRE, "jq '.categories.file_sizes.details'"); len(got) != 1 || got[0] != "file_sizes" {
		t.Errorf("auditCategoryRefRE extracted %v, want [file_sizes]", got)
	}
	if !cats["file_sizes"] || !cats["migration_safety"] {
		t.Error("audit.CategoryNames() lost a real category — truth source is wrong")
	}
	for _, retired := range []string{"composition", "size_limits", "proto_migration_alignment"} {
		if cats[retired] {
			t.Errorf("audit.CategoryNames() reports %q, which audit does not emit", retired)
		}
	}

	// 7. feature flags.
	feats := (config.FeaturesConfig{}).EffectiveFeatures()
	if got := extract(featureFlagRefRE, "set `features.experimental.ingress: true` and `features.codegen: false`"); len(got) != 2 {
		t.Errorf("featureFlagRefRE extracted %v, want 2 refs", got)
	}
	if _, ok := feats["codegen"]; !ok {
		t.Error("EffectiveFeatures() lost `codegen` — truth source is wrong")
	}
	for _, retired := range []string{"packs", "starters"} {
		if _, ok := feats[retired]; ok {
			t.Errorf("EffectiveFeatures() reports a retired feature %q", retired)
		}
	}
}

// ─── validator 8: proto package declarations ────────────────────────────

// fencedBlock is one fenced code block plus its info string and the
// nearest non-blank prose line above it (the sentence that introduces it).
type fencedBlock struct {
	info  string
	body  string
	intro string
}

// fencedBlocks returns every fenced code block in a markdown document.
func fencedBlocks(md string) []fencedBlock {
	var out []fencedBlock
	lines := strings.Split(md, "\n")
	var cur *fencedBlock
	var body []string
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "```") {
			if cur != nil {
				body = append(body, line)
			}
			continue
		}
		if cur != nil {
			cur.body = strings.Join(body, "\n")
			out = append(out, *cur)
			cur, body = nil, nil
			continue
		}
		intro := ""
		for j := i - 1; j >= 0 && j >= i-4; j-- {
			if s := strings.TrimSpace(lines[j]); s != "" {
				intro = s
				break
			}
		}
		cur = &fencedBlock{info: strings.TrimPrefix(trimmed, "```"), intro: intro}
	}
	return out
}

// emittedServiceProtoPackage reads the package line off the proto stub the
// scaffold actually wrote. Both proto emitters (service_gen.go and
// plan_proto_gen.go) hardcode the same `services.<svc>.v1` form, so this
// one file is the whole truth: change the emitter and this changes with it.
func emittedServiceProtoPackage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(scaffoldRoot(t), "proto", "services", "users", "v1", "users.proto"))
	if err != nil {
		t.Fatalf("read scaffolded service proto: %v", err)
	}
	m := protoPackageRE.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatal("scaffolded service proto declares no package — truth source is wrong")
	}
	return m[1]
}

var (
	protoPackageRE = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_.]+)\s*;`)
	protoServiceRE = regexp.MustCompile(`(?m)^\s*service\s+[A-Za-z0-9_]+\s*\{`)
	// protoPathCommentRE matches a leading `// proto/<...>.proto` locator
	// comment — the convention the skills use to say where a block's file
	// lives. buf's PACKAGE_DIRECTORY_MATCH (on under the emitted buf.yaml's
	// DEFAULT lint) makes that path and the package the same statement.
	protoPathCommentRE = regexp.MustCompile(`(?m)^\s*//\s*(?:proto/)?([A-Za-z0-9_/<>.-]+)/[A-Za-z0-9_.-]+\.proto\s*$`)
)

// servicePkgFormRE is the shape a forge service proto package must have.
// TestSkillsProtoPackagesMatchEmitter asserts the SCAFFOLD's own package
// satisfies it, so the two can never drift apart silently.
var servicePkgFormRE = regexp.MustCompile(`^services\.[a-z][a-z0-9_]*\.v\d+$`)

// protoPkgClaim is one `package ...;` declaration inside a proto block,
// paired with the locator comment (if any) that precedes it. One fenced
// block routinely shows two files, so the pairing has to be positional.
type protoPkgClaim struct {
	pkg     string
	locator string // e.g. "services/users/v1", "" when the block has none
}

func protoPkgClaims(body string) []protoPkgClaim {
	var out []protoPkgClaim
	locator := ""
	for _, line := range strings.Split(body, "\n") {
		if m := protoPathCommentRE.FindStringSubmatch(line); m != nil {
			locator = strings.Trim(m[1], "/")
			continue
		}
		if m := protoPackageRE.FindStringSubmatch(line); m != nil {
			out = append(out, protoPkgClaim{pkg: m[1], locator: locator})
			locator = ""
		}
	}
	return out
}

// validateProtoPackage returns "" when a package declaration is one forge
// can actually produce, else a reason. Two independent rules:
//
//  1. A block that declares an RPC `service`, or a package that mentions
//     the `services` root at all, is a forge SERVICE proto — its package
//     is `services.<svc>.v<n>`, with no project prefix. Truth: the
//     scaffold's own emitted stub (emittedServiceProtoPackage).
//  2. A declaration carrying a `// <dir>/<file>.proto` locator comment
//     must satisfy buf's PACKAGE_DIRECTORY_MATCH: package == directory
//     path with dots for slashes. This is what catches a project-prefixed
//     package on a NON-service proto, which rule 1 cannot see.
func validateProtoPackage(block fencedBlock, c protoPkgClaim) string {
	isService := protoServiceRE.MatchString(block.body) ||
		slices.Contains(strings.Split(c.pkg, "."), "services")
	if isService && !servicePkgFormRE.MatchString(c.pkg) {
		return "forge service protos are `services.<svc>.v<n>` with no project prefix"
	}
	if c.locator != "" && !placeholder(c.locator) {
		if want := strings.ReplaceAll(c.locator, "/", "."); want != c.pkg {
			return "buf's PACKAGE_DIRECTORY_MATCH requires package " + want +
				" for a file at " + c.locator + "/ (the declaration's own locator comment)"
		}
	}
	return ""
}

func TestSkillsProtoPackagesMatchEmitter(t *testing.T) {
	emitted := emittedServiceProtoPackage(t)
	if !servicePkgFormRE.MatchString(emitted) {
		t.Fatalf("scaffold emits package %q, which no longer matches %s — update the rule, then the skills",
			emitted, servicePkgFormRE)
	}

	allow := allowlist(t)
	for rel, content := range shippedSkills(t) {
		seen := map[string]bool{}
		for _, block := range fencedBlocks(content) {
			if !strings.Contains(block.info, "proto") && !strings.Contains(block.body, "syntax = \"proto3\"") {
				continue
			}
			for _, c := range protoPkgClaims(block.body) {
				claim := "package " + c.pkg
				if seen[claim] {
					continue
				}
				seen[claim] = true
				if reason := validateProtoPackage(block, c); reason != "" {
					if allowed(allow, rel, claim) {
						continue
					}
					t.Errorf("skills/%s: `%s;` is not a package forge emits — %s (the scaffold emits `%s`)\n"+
						"  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
						rel, claim, reason, emitted)
				}
			}
		}
	}
}

// ─── validator 9: forge.yaml key claims ─────────────────────────────────

// forgeYAMLTopKeyRE matches a column-0 mapping key inside a YAML block.
var forgeYAMLTopKeyRE = regexp.MustCompile(`(?m)^([a-z][a-z0-9_]*):`)

// forgeYAMLKeyVerdict runs the REAL loader over a minimal forge.yaml that
// declares `key`, and reports why forge refuses it (or "" when it is a
// legitimate key). Removed keys surface as warnings on the config warning
// sink; keys that never existed surface as `unknown key` load errors.
// Routing through config.LoadProject keeps removedSchemaKeys — the
// authoritative list — as the single source of truth without copying it.
func forgeYAMLKeyVerdict(key string) string {
	var warnings strings.Builder
	prev := config.SetConfigWarningSink(&warnings)
	_, err := config.LoadProject(
		[]byte("name: demo\nmodule_path: example.com/demo\n"+key+": []\n"), "forge.yaml")
	config.SetConfigWarningSink(prev)

	// Match on the QUOTED KEY alone, never on the sentence around it. This
	// grepped `"<key>" was removed` until config reworded the warning to
	// `"<key>" is no longer a forge.yaml key` — a strictly better message,
	// since it states the current model rather than forge's own history —
	// and five keys silently began reporting "not rejected", which is the
	// opposite of the truth this helper exists to establish. An assertion
	// coupled to a phrase dies when the phrase improves.
	quoted := `"` + key + `"`
	for _, line := range strings.Split(warnings.String(), "\n") {
		if i := strings.Index(line, quoted); i >= 0 {
			return strings.TrimSpace(line[i:])
		}
	}
	if err != nil && strings.Contains(err.Error(), `unknown key "`+key+`"`) {
		return "forge.yaml has no " + key + " key"
	}
	return ""
}

func TestSkillsForgeYAMLKeysExist(t *testing.T) {
	allow := allowlist(t)
	for rel, content := range shippedSkills(t) {
		seen := map[string]bool{}
		for _, block := range fencedBlocks(content) {
			info := strings.TrimSpace(block.info)
			if info != "yaml" && info != "yml" {
				continue
			}
			// Only blocks the prose introduces AS forge.yaml are claims
			// about the forge.yaml schema; a bare yaml block could be a
			// GitHub workflow, a KCL values file, or anything else.
			if !strings.Contains(block.intro, "forge.yaml") &&
				!strings.Contains(firstLine(block.body), "forge.yaml") {
				continue
			}
			for _, m := range forgeYAMLTopKeyRE.FindAllStringSubmatch(block.body, -1) {
				key := m[1]
				if seen[key] {
					continue
				}
				seen[key] = true
				if reason := forgeYAMLKeyVerdict(key); reason != "" {
					if allowed(allow, rel, key+":") {
						continue
					}
					t.Errorf("skills/%s: the forge.yaml block declares `%s:` — %s\n"+
						"  (fix the skill, or add to internal/templates/testdata/skills_validation_allowlist.txt with a justification)",
						rel, key, reason)
				}
			}
		}
	}
}

// TestProtoAndForgeYAMLValidatorsCatchKnownBadClaims is the self-test for
// validators 8-9. Each case is a claim that really shipped in a skill
// before this file could see it.
func TestProtoAndForgeYAMLValidatorsCatchKnownBadClaims(t *testing.T) {
	// 8. proto packages.
	svcBlock := fencedBlock{info: "proto", body: "service UserService {\n  rpc GetUser(Req) returns (Resp);\n}"}
	for _, bad := range []string{
		"myproject.services.users.v1", // project-prefixed
		"users.v1",                    // missing the services root
		"services.users",              // missing the version
	} {
		if reason := validateProtoPackage(svcBlock, protoPkgClaim{pkg: bad}); reason == "" {
			t.Errorf("validateProtoPackage accepted %q for a block declaring an RPC service", bad)
		}
	}
	if reason := validateProtoPackage(svcBlock, protoPkgClaim{pkg: "services.users.v1"}); reason != "" {
		t.Errorf("validateProtoPackage rejected the package the scaffold emits: %s", reason)
	}
	// A non-service proto is only checkable through its locator comment.
	plain := fencedBlock{info: "proto", body: "message Money { string currency = 1; }"}
	if reason := validateProtoPackage(plain, protoPkgClaim{pkg: "myproject.shared.v1", locator: "shared/v1"}); reason == "" {
		t.Error("validateProtoPackage accepted a package that contradicts its own locator comment")
	}
	if reason := validateProtoPackage(plain, protoPkgClaim{pkg: "shared.v1", locator: "shared/v1"}); reason != "" {
		t.Errorf("validateProtoPackage rejected a package matching its locator comment: %s", reason)
	}
	// Two files in ONE fence must each be paired with their own locator.
	claims := protoPkgClaims("// proto/services/users/v1/users.proto\npackage services.users.v1;\n\n" +
		"// proto/services/users/v2/users.proto\npackage services.users.v2;\n")
	if len(claims) != 2 || claims[0].locator != "services/users/v1" || claims[1].locator != "services/users/v2" {
		t.Errorf("protoPkgClaims mis-paired locators: %+v", claims)
	}

	// 9. forge.yaml keys.
	for _, removed := range []string{"services", "components", "binaries", "packs", "test"} {
		if v := forgeYAMLKeyVerdict(removed); v == "" {
			t.Errorf("forgeYAMLKeyVerdict(%q) is empty — forge.yaml REJECTS that key", removed)
		}
	}
	if v := forgeYAMLKeyVerdict("no_such_key_at_all"); v == "" {
		t.Error("forgeYAMLKeyVerdict accepted a key forge.yaml has never had")
	}
	for _, real := range []string{"name", "database", "features", "contracts", "frontends"} {
		if v := forgeYAMLKeyVerdict(real); v != "" {
			t.Errorf("forgeYAMLKeyVerdict(%q) = %q, but it is a real forge.yaml key", real, v)
		}
	}
	// The fence must actually be recognised as forge.yaml, or every claim
	// inside it goes unchecked.
	blocks := fencedBlocks("Cron workers are tracked in `forge.yaml`:\n```yaml\nservices:\n  - name: x\n```\n")
	if len(blocks) != 1 || !strings.Contains(blocks[0].intro, "forge.yaml") {
		t.Fatalf("fencedBlocks lost the introducing prose line: %+v", blocks)
	}
	if got := forgeYAMLTopKeyRE.FindAllStringSubmatch(blocks[0].body, -1); len(got) != 1 || got[0][1] != "services" {
		t.Errorf("forgeYAMLTopKeyRE extracted %v, want [services]", got)
	}
}
