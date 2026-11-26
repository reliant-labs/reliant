// File: internal/cli/lint/lint_lanes.go
//
// forge.yaml's lint.lanes: a project switches whole lanes off.
//
//	lint:
//	  lanes:
//	    frontend: off
//	    migration-safety: off
//	    db-grants: off
//
// A repository adopting forge often lints its frontend with its own
// toolchain, migrates its schema with another tool, or has no database
// grants for forge to check. Before this knob the only levers were
// `lint.rules: {"*": off}` (forge's rules, not its lanes) and lint.allow_skip
// (which accepts a lane that COULD NOT run — every run, with a reason, as an
// exception). A lane switched off is a standing decision, so it is declared
// once, here.
//
// Off is never silent: the lane gets a line of its own and the verdict
// counts it — `2 off (forge.yaml lint.lanes): frontend lint, migration
// safety lint` — and --json carries it as an info finding. Naming the lane on
// the command line (`forge lint --migration-safety`) still runs it: a
// targeted flag is an explicit request, and it wins over the file, as
// --allow-skip does.

package lint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
)

// laneOffNote is how an off lane is named everywhere it is reported.
const laneOffNote = "off (forge.yaml lint.lanes)"

// laneAliases are the lint.lanes keys that name more than one lane. The
// frontend's three lanes are one decision for a project whose frontend is
// linted elsewhere.
var laneAliases = map[string][]string{
	"frontend": {"frontend lint", "frontend typecheck", "frontend lockfile"},
}

// laneSwitchNames lists every lane lint.lanes can name: each pipeline lane,
// the format lane, and the aliases.
func laneSwitchNames() []string {
	names := []string{formatLaneName}
	for _, step := range lintPipeline() {
		names = append(names, laneFlagName(step.name))
	}
	for alias := range laneAliases {
		names = append(names, alias)
	}
	sort.Strings(names)
	return names
}

// resolveLaneSwitch maps a lint.lanes key to the lanes it names: an alias,
// a lane's verdict or flag spelling ("migration safety lint",
// "migration-safety-lint"), or that spelling without its "-lint" suffix
// ("migration-safety").
func resolveLaneSwitch(key string) ([]string, bool) {
	want := laneFlagName(key)
	if lanes, ok := laneAliases[want]; ok {
		return lanes, true
	}
	if want == formatLaneName {
		return []string{formatLaneName}, true
	}
	for _, step := range lintPipeline() {
		flag := laneFlagName(step.name)
		if flag == want || strings.TrimSuffix(flag, "-lint") == want {
			return []string{step.name}, true
		}
	}
	return nil, false
}

// lanesOff resolves lint.lanes to the set of lane names switched off. An
// unknown key is refused before anything runs: a typo'd lane would leave the
// lane running while the project believes it opted out.
func lanesOff(cfg *config.ProjectConfig) (map[string]bool, error) {
	if cfg == nil || len(cfg.Lint.Lanes) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(cfg.Lint.Lanes))
	for k := range cfg.Lint.Lanes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	off := map[string]bool{}
	for _, k := range keys {
		lanes, ok := resolveLaneSwitch(k)
		if !ok {
			return nil, cliutil.UserErr("forge.yaml lint.lanes", fmt.Sprintf("%q is not a forge lint lane", k), "",
				"name one of: "+strings.Join(laneSwitchNames(), ", "))
		}
		if !config.LaneOff(cfg.Lint.Lanes[k]) {
			continue
		}
		for _, l := range lanes {
			off[l] = true
		}
	}
	return off, nil
}

// offOutcome is the outcome of a lane lint.lanes switched off.
func offOutcome(name string, gates bool) lintStepOutcome {
	return lintStepOutcome{name: name, gates: gates, off: true}
}

// frontendSkips is what --skip-frontends and lint.lanes mean for the two
// frontend consumers, the same in text and --json mode. The run context's
// skip drops the whole frontend lane (eslint/stylelint AND the gating
// typecheck), so only --skip-frontends sets it; `frontend-lint: off` turns off
// the lint lane alone, through lanesOff. Formatting (the fix pre-pass and the
// format lane) also leaves a frontend whose lint lane is off alone.
func frontendSkips(flagSkip bool, off map[string]bool) (run, format bool) {
	return flagSkip, flagSkip || off["frontend lint"]
}
