// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	skillscatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBuiltinSkillResolver builds the resolver over the catalog a FORGE project
// surfaces, which is the right view for builtin charters: forge-one-shot and
// get-it-right exist to build forge apps, and the emit partition that decides
// whether a forge skill is addressable as "forge/db" or as bare "db" only
// produces the "forge/" half inside a project that has forge.yaml.
func newBuiltinSkillResolver(t *testing.T) *validation.SkillResolver {
	t.Helper()
	paths := skillscatalog.WorkflowSkillPaths("")

	require.NotEmpty(t, paths, "skill catalog resolved to nothing — the guard would pass every name vacuously")
	// Fail loudly if the forge half of the catalog disappears: without it,
	// every "forge/..." reference below would be judged against a set that
	// cannot contain it, and this test would report a flood of false errors
	// rather than the drift it is written to catch.
	var sawForge bool
	for _, p := range paths {
		if strings.HasPrefix(p, "forge/") {
			sawForge = true
			break
		}
	}
	require.True(t, sawForge, "no forge-namespaced skills discovered; forge catalog integration is broken, not the workflows")

	return &validation.SkillResolver{
		Names: paths,
		Resolve: func(p string) bool {
			return skillscore.ResolveSkillPathIndex(paths, p) >= 0
		},
	}
}

// TestBuiltinWorkflowSkillsResolve is the drift guard for defect class
// "a charter asks for a skill that does not exist".
//
// A `skills:` name that does not resolve is not an error at run time, and
// cannot be: a warm catalog that lacks the path lacks it on every retry too, so
// the preloader (buildSeededSkillMessages) warns, tells the model what it did
// not get, and the phase runs on whatever the model already knew. The guidance
// the charter was written to deliver never arrives, on every run. Nothing
// downstream can turn that back into guidance, so the name has to be checked
// here, before the run.
//
// This mirrors TestPresetRecommendedSkillsExist, which validates preset
// params.skills the same way — the two together cover both surfaces that name
// a skill statically.
func TestBuiltinWorkflowSkillsResolve(t *testing.T) {
	resolver := newBuiltinSkillResolver(t)

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(name)
			require.NoError(t, err)

			result, err := runtime.ValidateYAMLResultWithOptions(data, nil, &validation.ValidationOptions{
				SkillResolver: resolver,
			})
			require.NoError(t, err)

			for _, e := range result.ByCategory(validation.CategorySkillRef) {
				assert.Fail(t, "unresolvable skill reference", "%s: %s", name, e.Error())
			}
		})
		checked++
	}
	require.NotZero(t, checked, "no builtin workflows were checked")
}

// A charter names a skill in two places, and only one of them is structured.
// `skills:` is a proto field the validator can walk; a PROSE instruction ("Load
// the `forge/forge-libraries` skill") is free text inside a prompt string, and
// nothing checked it. The two spellings drifted apart exactly as you'd expect —
// a name that had to carry the synthetic "forge/" prefix in the structured list
// was written with that prefix in prose too, for a skill that is surfaced bare.
//
// The failure is worse in prose than in the structured list: an unresolvable
// `skills:` entry is silently skipped at preload, but a prose instruction is
// obeyed, so the agent BURNS a turn on a `skill not found` before it recovers —
// or loads the wrong thing and never notices.
//
// These two expressions are the anchor. A skill reference in prose is a
// backticked name written next to the word "skill"/"skills", on either side —
// which is the convention every builtin already follows, not one invented here:
//
//	Load the `frontend/design` skill
//	brief each BACKEND unit to load the `service-layer`, `api`, `auth`, and `testing` skills
//	the shape-specific skill (`migration-service` / `migration-cli`)
//
// A run of names joined by commas / "and" / "or" / "/" (and the parens or
// newline around a parenthetical list) is one reference, so every member of a
// list gets checked, not just the one touching the word.
//
// What this deliberately does NOT cover, so nobody reads it as more than it is:
// an unbackticked name ("per the migration skill"), and a name separated from
// the word "skill" by prose ("Follow those skills exactly" three sentences
// later). Widening to a whole sentence would sweep in every backticked
// identifier in these prompts — `forge.yaml`, `_gen`, `total_count` — and a
// guard that cries wolf gets deleted. Tight and honest beats broad and noisy.
var (
	proseSkillTrailing = regexp.MustCompile("(" + backtickRunPattern + `)[\s)]*skills?\b`)
	proseSkillLeading  = regexp.MustCompile(`\bskills?\b[\s:(]*(` + backtickRunPattern + ")")
)

const (
	backtickTokenPattern = "`[^`\n]+`"
	backtickSepPattern   = `[\s,/()+]*(?:and|or)?[\s,/()+]*`
	backtickRunPattern   = backtickTokenPattern + "(?:" + backtickSepPattern + backtickTokenPattern + ")*"
)

// backtickToken pulls the individual names back out of a matched run.
var backtickToken = regexp.MustCompile(backtickTokenPattern)

// skillNameShape is the spelling a catalog path can actually have: lowercase
// components of letters/digits/hyphens, separated by "/". Everything else a
// charter backticks — `forge.yaml`, `.proto`, `_gen`, `ALL_ROUTES`,
// `reliant forge skill load migration` — fails this and is never treated as a
// skill name, which is what keeps the anchor above from producing noise.
var skillNameShape = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:/[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

// proseSkillNames returns every skill name a charter's PROSE tells an agent to
// load, deduplicated and sorted so failures read the same run to run.
func proseSkillNames(text string) []string {
	seen := map[string]struct{}{}
	for _, re := range []*regexp.Regexp{proseSkillTrailing, proseSkillLeading} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			for _, tok := range backtickToken.FindAllString(m[1], -1) {
				name := strings.Trim(tok, "`")
				if skillNameShape.MatchString(name) {
					seen[name] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestBuiltinWorkflowProseSkillsResolve is TestBuiltinWorkflowSkillsResolve for
// the other half of the surface: the skill names a charter names in prose.
//
// It runs over the raw YAML rather than the parsed workflow because prose can
// sit in any string field — a node prompt, an inject block, a comment — and a
// guard that enumerated the fields it knows about would go stale the first time
// a charter grew a new one. The anchor is the text convention, so the text is
// what it reads.
func TestBuiltinWorkflowProseSkillsResolve(t *testing.T) {
	resolver := newBuiltinSkillResolver(t)

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)

	total := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(name)
			require.NoError(t, err)

			for _, skill := range proseSkillNames(string(data)) {
				total++
				assert.True(t, resolver.Resolve(skill),
					"%s: prose tells the agent to load the %q skill, which does not resolve — "+
						"the agent will burn a turn on `skill not found`", name, skill)
			}
		})
	}

	// An anchor that matched nothing would pass every charter vacuously, which
	// is the failure mode this whole file exists to prevent.
	require.NotZero(t, total, "no prose skill references were found; the anchor no longer matches how charters name skills")
}

// TestBuiltinSkillResolverRejectsNamespacedBareSkill pins the exact drift this
// guard exists to catch, so the guard itself cannot rot into one that passes
// everything.
//
// Forge surfaces emit:general and emit:both skills at their BARE path.
// Reliant's synthetic "forge/" namespace covers only emit:forge skills, and
// the addressing rule accepts a shorter spelling of a longer path, never a
// longer spelling of a shorter one. So "service-layer" resolves and
// "forge/service-layer" does not — which is not obvious, is exactly what the
// charter got wrong, and is the property the guard depends on.
func TestBuiltinSkillResolverRejectsNamespacedBareSkill(t *testing.T) {
	resolver := newBuiltinSkillResolver(t)

	// emit:both — addressable bare, NOT under the synthetic namespace.
	for _, bare := range []string{"db", "service-layer", "testing"} {
		assert.True(t, resolver.Resolve(bare), "bare %q should resolve", bare)
		assert.False(t, resolver.Resolve("forge/"+bare),
			"%q must NOT resolve: forge surfaces it at its bare path, so nothing carries the forge/ prefix", "forge/"+bare)
	}

	// emit:forge — addressable under the namespace, and (via the
	// component-aligned suffix rule) by the bare path forge's own CLI prints.
	for _, namespaced := range []string{"forge/architecture", "forge/frontend/design", "forge/proto"} {
		assert.True(t, resolver.Resolve(namespaced), "%q should resolve", namespaced)
	}
}
