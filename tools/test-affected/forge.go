// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ForgeDecision is forge's affected-mode decision for a change set: the
// `affected` object `forge ci gate --plan --json --files <changed>` prints.
// forge.yaml's ci.gate (affected_rules, affected_readers, affected_reach,
// affected_modules) says how forge reaches it. The Go lane, the sdk/go
// module's tests, and the path-gated rows (electron, docs-generate) are
// forge's; this tool adds only the lanes forge does not have.
type ForgeDecision struct {
	Full   bool   `json:"full"`
	Reason string `json:"reason"`
	// Packages, Build and Deferred are import paths.
	Packages      []string        `json:"packages"`
	Build         []string        `json:"build"`
	Deferred      []string        `json:"deferred"`
	Why           []forgeWhy      `json:"why"`
	Modules       []forgeModule   `json:"modules"`
	Checks        []forgeCheck    `json:"checks"`
	Reach         string          `json:"reach"`
	TransitiveCap int             `json:"transitive_cap"`
	Files         []forgeFileNote `json:"files"`
}

type forgeWhy struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Edge    string `json:"edge"`
}

type forgeModule struct {
	Dir      string   `json:"dir"`
	Path     string   `json:"path"`
	Full     bool     `json:"full"`
	Reason   string   `json:"reason"`
	Packages []string `json:"packages"`
}

type forgeCheck struct {
	Name string `json:"name"`
	Runs bool   `json:"runs"`
	Note string `json:"note"`
}

type forgeFileNote struct {
	Path    string `json:"path"`
	Package string `json:"package"`
	Note    string `json:"note"`
}

// failSafe reports whether forge ran the whole suite because a changed file is
// one it cannot place (no package owns it, no rule claims it, affected_ignore
// does not list it): then nothing proves what else reads it, and every lane
// here runs whole too. A rule's `full: true` (go.mod, forge.yaml) is not that:
// it names a cause that reaches the Go code only.
func (d *ForgeDecision) failSafe() bool {
	return d.Full && !strings.Contains(d.Reason, "ci.gate.affected_rules[")
}

// check is the named path-gated row's decision.
func (d *ForgeDecision) check(name string) (forgeCheck, bool) {
	for _, c := range d.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return forgeCheck{}, false
}

// forgeDecide asks the pinned forge (scripts/forge.sh) what the change set
// selects.
func forgeDecide(ctx context.Context, root string, files []string) (*ForgeDecision, error) {
	args := []string{"ci", "gate", "--plan", "--json", "--files", strings.Join(files, ",")}
	cmd := exec.CommandContext(ctx, filepath.Join(root, "scripts", "forge.sh"), args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("forge %s: %w\n%s", strings.Join(args[:4], " "), err, tail(stderr.String(), 20))
	}
	var d ForgeDecision
	if err := json.Unmarshal(out, &d); err != nil {
		return nil, fmt.Errorf("forge ci gate --plan --json: %w", err)
	}
	return &d, nil
}
