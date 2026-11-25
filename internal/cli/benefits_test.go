package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/benefits"
	"github.com/reliant-labs/forge/internal/cli/lint"
)

// benefitsRepoRoot is the forge checkout, relative to this package's
// directory (the working directory of `go test`).
const benefitsRepoRoot = "../.."

// TestBenefitsEvidenceExists is what makes the registry documentation rather
// than marketing: every evidence item an entry names resolves against this
// binary and this repo. A command (and its --flag) must be in the real cobra
// tree, an analyzer (and a rule's lane) a `forge lint` flag, a marker in markerSpecs(), a skill
// one this binary ships, a library or path a file in the checkout. `external`
// items name sibling repos that are not checked out here, so the registry's
// own Validate checks only their format.
//
// Cache-sound: the registry is embedded, the tree and the markers are
// compiled in, the skills are embedded, and every file is os.Stat-ed by this
// process, so Go's test cache sees a removed file.
func TestBenefitsEvidenceExists(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	root := newTestRootCmd(t)
	lintCmd := findChildCommand(root, "lint")
	if lintCmd == nil {
		t.Fatal("no `forge lint` in the command tree")
	}
	markers := map[string]bool{}
	for _, m := range markerSpecs() {
		markers[m.Name] = true
	}

	checkEvidence := func(owner string, evidence []benefits.Evidence) {
		for _, e := range evidence {
			kind, v := e.Kind()
			switch kind {
			case benefits.KindCommand:
				if msg := resolveCommandEvidence(root, v); msg != "" {
					t.Errorf("%s: command %q: %s", owner, v, msg)
				}
			case benefits.KindAnalyzer:
				if lintCmd.Flags().Lookup(strings.TrimPrefix(v, "--")) == nil {
					t.Errorf("%s: analyzer %q is not a `forge lint` flag", owner, v)
				}
			case benefits.KindMarker:
				if !markers[v] {
					t.Errorf("%s: marker %q is not in markerSpecs() (`forge project capabilities` lists them)", owner, v)
				}
			case benefits.KindSkill:
				if _, err := loadForgeShippedSkill(v); err != nil {
					t.Errorf("%s: skill %q does not load from this binary: %v", owner, v, err)
				}
			case benefits.KindLibrary:
				fi, err := os.Stat(filepath.Join(benefitsRepoRoot, v))
				if err != nil || !fi.IsDir() {
					t.Errorf("%s: library %q is not a package directory in this repo", owner, v)
				}
			case benefits.KindPath:
				if _, err := os.Stat(filepath.Join(benefitsRepoRoot, v)); err != nil {
					t.Errorf("%s: path %q does not exist in this repo", owner, v)
				}
			case benefits.KindExternal:
				// Another repo; benefits.Validate already checked <repo>:<path>.
			default:
				t.Errorf("%s: evidence item with no kind passed Validate", owner)
			}
		}
	}
	// A limit is held to the same evidence as a benefit: the comparison an
	// agent quotes is only as honest as its losses are checked.
	for _, l := range reg.Limits {
		checkEvidence(fmt.Sprintf("limit %q", l.ID), l.Evidence)
	}

	for _, b := range reg.Benefits {
		checkEvidence(fmt.Sprintf("benefit %q", b.ID), b.Evidence)
		// A rule's lane is the flag that runs it alone; "" is a rule only
		// a bare `forge lint` runs; "forge <command>" a rule another
		// command reports.
		for _, r := range b.Rules {
			if cmdPath, ok := strings.CutPrefix(r.Lane, "forge "); ok {
				if msg := resolveCommandEvidence(root, cmdPath); msg != "" {
					t.Errorf("benefit %q: rule %q: lane %q: %s", b.ID, r.ID, r.Lane, msg)
				}
				continue
			}
			if r.Lane != "" && lintCmd.Flags().Lookup(strings.TrimPrefix(r.Lane, "--")) == nil {
				t.Errorf("benefit %q: rule %q: lane %q is not a `forge lint` flag", b.ID, r.ID, r.Lane)
			}
		}
	}
}

// resolveCommandEvidence walks "env deploy --from" down the real tree: every
// word before the first flag is a subcommand (by name or alias), and the flag,
// if any, must exist on the command it reaches (its own or inherited). It
// returns what is wrong, or "".
func resolveCommandEvidence(root *cobra.Command, spec string) string {
	cmd := root
	words := strings.Fields(spec)
	i := 0
	for ; i < len(words) && !strings.HasPrefix(words[i], "-"); i++ {
		var next *cobra.Command
		for _, c := range cmd.Commands() {
			if c.Name() == words[i] || c.HasAlias(words[i]) {
				next = c
				break
			}
		}
		if next == nil {
			return "`" + cmd.CommandPath() + "` has no subcommand " + words[i]
		}
		cmd = next
	}
	switch rest := words[i:]; len(rest) {
	case 0:
		return ""
	case 1:
		if cmd.Flag(strings.TrimPrefix(rest[0], "--")) == nil {
			return "`" + cmd.CommandPath() + "` has no flag " + rest[0]
		}
		return ""
	default:
		return "name one command and at most one --flag"
	}
}

// TestEveryGradableRuleIsABenefit holds the registry's lint families equal to
// the rules `lint.rules` can grade: a rule nobody wrote a bug class for fails,
// and so does a `rules:` id that is not a real rule (a rename, a typo). With
// TestEveryGradableRuleDeclaresItsRollout beside it, an analyzer cannot merge
// without both its rollout date and its entry.
//
// The forge-internal families (`banner-*`, `cross-tier-derived-symbol`) are
// gradable, and the catalog lists them as forge-internal rather than leaving
// them out, so there is no allowlist.
func TestEveryGradableRuleIsABenefit(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, b := range reg.Benefits {
		for _, r := range b.Rules {
			listed[r.ID] = b.ID
		}
	}
	gradable := map[string]bool{}
	var missing []string
	for _, r := range lint.GradableRules() {
		gradable[r] = true
		if _, ok := listed[r]; !ok {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d gradable lint rule(s) appear in no benefits.yaml entry's rules: %s\n"+
			"Add each to the lint-family entry for the bug class it prevents (`{id, lane, bug_class}`), "+
			"then run `task docs:benefits`.", len(missing), strings.Join(missing, ", "))
	}
	var unknown []string
	for id, owner := range listed {
		if !gradable[id] {
			unknown = append(unknown, id+" (in "+owner+")")
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("benefits.yaml lists rule(s) `lint.rules` cannot grade — renamed, removed or misspelled: %s",
			strings.Join(unknown, ", "))
	}
}

// The resolver itself: a test that only ever sees passing evidence would pass
// with a resolver that accepts everything.
func TestResolveCommandEvidence(t *testing.T) {
	root := newTestRootCmd(t)
	for spec, wantOK := range map[string]bool{
		"lint":                      true,
		"lint --migration-safety":   true,
		"env deploy --from":         true,
		"lint --no-such-lane":       false,
		"env no-such-verb":          false,
		"lint --scope --quiet":      false,
		"project capabilities --x":  false,
		"skill load":                true,
		"project capabilities":      true,
		"no-such-command --verbose": false,
	} {
		msg := resolveCommandEvidence(root, spec)
		if (msg == "") != wantOK {
			t.Errorf("resolveCommandEvidence(%q) = %q, want ok=%v", spec, msg, wantOK)
		}
	}
}
