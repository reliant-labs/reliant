package config

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// lint.lanes switches whole `forge lint` lanes off for a project:
//
//	lint:
//	  lanes:
//	    frontend: off          # the frontend is linted by its own toolchain
//	    migration-safety: off  # migrations are another tool's
//
// It is the lane-level twin of lint.rules (one rule's severity) and of
// lint.allow_skip (a lane that COULD NOT run). A lane switched off does not
// run, and the verdict still names it — `off (forge.yaml lint.lanes)` — so a
// green run never hides what it did not check. Which names are lanes is the
// lint command's to say (internal/cli/lint/lint_lanes.go): it refuses an
// unknown one before running anything, as it does for lint.allow_skip.

// LaneOff reports whether the value of a lint.lanes entry switches its lane
// off. "on" (or true) is accepted too, so a lane can be listed while it is
// being worked on.
func LaneOff(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "false":
		return true
	}
	return false
}

// validateLintLanes rejects a lint.lanes value that is neither off nor on: a
// typo must not leave the lane silently running (or silently off).
func validateLintLanes(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	keys := make([]string, 0, len(cfg.Lint.Lanes))
	for k := range cfg.Lint.Lanes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []validationIssue
	for _, k := range keys {
		switch strings.ToLower(strings.TrimSpace(cfg.Lint.Lanes[k])) {
		case "off", "on", "false", "true":
			continue
		}
		line, col := findNodePos(root, []string{"lint", "lanes", k})
		if line == 0 {
			line, col = findNodePos(root, []string{"lint", "lanes"})
		}
		out = append(out, validationIssue{line: line, column: col,
			msg: fmt.Sprintf("lint.lanes.%s is %q; a lane is off or on", k, cfg.Lint.Lanes[k]),
			fix: fmt.Sprintf("write `%s: off` to switch the lane off, or delete the key.", k)})
	}
	return out
}
