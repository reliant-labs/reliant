package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// funcPointer is the code pointer of a func value, nil-safe. Two closures
// from one func literal share it, which is what "built by the same
// constructor" means here.
func funcPointer(f any) uintptr {
	v := reflect.ValueOf(f)
	if !v.IsValid() || v.IsNil() {
		return 0
	}
	return v.Pointer()
}

// sameImplementation fails unless the two command subtrees were built by the
// same constructors: the same names, Use, Short and Hidden; the same flags
// (local and inherited); and the same Args, PreRun, Run and PostRun code, at
// every level.
func sameImplementation(t *testing.T, oldPath, newPath string, old, cur *cobra.Command) {
	t.Helper()
	if old.Use != cur.Use || old.Short != cur.Short || old.Hidden != cur.Hidden {
		t.Errorf("`%s` and `%s` differ: Use %q/%q, Short %q/%q, Hidden %v/%v", oldPath, newPath, old.Use, cur.Use, old.Short, cur.Short, old.Hidden, cur.Hidden)
	}
	if a, b := surfaceFlags(old), surfaceFlags(cur); !slices.Equal(a, b) {
		t.Errorf("`%s` and `%s` take different flags:\n  old %v\n  new %v", oldPath, newPath, a, b)
	}
	for _, fn := range []struct {
		name     string
		old, cur any
	}{
		{"Args", old.Args, cur.Args},
		{"PreRun", old.PreRun, cur.PreRun},
		{"PreRunE", old.PreRunE, cur.PreRunE},
		{"Run", old.Run, cur.Run},
		{"RunE", old.RunE, cur.RunE},
		{"PostRun", old.PostRun, cur.PostRun},
		{"PostRunE", old.PostRunE, cur.PostRunE},
		{"ValidArgsFunction", old.ValidArgsFunction, cur.ValidArgsFunction},
	} {
		if funcPointer(fn.old) != funcPointer(fn.cur) {
			t.Errorf("`%s` and `%s` run different %s code: the new path must be built by the old one's constructor", oldPath, newPath, fn.name)
		}
	}
	oldKids, curKids := map[string]*cobra.Command{}, map[string]*cobra.Command{}
	for _, c := range old.Commands() {
		oldKids[c.Name()] = c
	}
	for _, c := range cur.Commands() {
		curKids[c.Name()] = c
	}
	for name, oc := range oldKids {
		cc, ok := curKids[name]
		if !ok {
			t.Errorf("`%s %s` has no counterpart under `%s`", oldPath, name, newPath)
			continue
		}
		sameImplementation(t, oldPath+" "+name, newPath+" "+name, oc, cc)
	}
	for name := range curKids {
		if _, ok := oldKids[name]; !ok {
			t.Errorf("`%s %s` has no counterpart under `%s`", newPath, name, oldPath)
		}
	}
}

// Every row's new path is the old command, mounted again: one definition.
func TestCommandMovesMountTheSameImplementation(t *testing.T) {
	root := newTestRootCmd(t)
	for _, m := range commandMoves() {
		old, cur := findRel(root, m.Old), findRel(root, m.New)
		if old == nil || cur == nil {
			t.Errorf("row %q -> %q: old found %v, new found %v", m.Old, m.New, old != nil, cur != nil)
			continue
		}
		if m.preexisting {
			// Two implementations that predate the table: they must at
			// least accept the same command line.
			if a, b := surfaceFlags(old), surfaceFlags(cur); !slices.Equal(a, b) {
				t.Errorf("`%s` and its equivalent `%s` take different flags:\n  old %v\n  new %v", m.Old, m.New, a, b)
			}
			continue
		}
		sameImplementation(t, m.Old, m.New, old, cur)
	}
}

// Only forge's root has persistent hooks, so moving a command out from under
// its old parent loses none. A parent that gains one would make the two
// paths run differently; this fails first.
func TestNoParentOfAMovedCommandHasItsOwnHooks(t *testing.T) {
	root := newTestRootCmd(t)
	for _, m := range commandMoves() {
		for _, rel := range []string{m.Old, m.New} {
			c := findRel(root, rel)
			if c == nil {
				continue
			}
			for p := c.Parent(); p != nil && p != root; p = p.Parent() {
				if p.PersistentPreRun != nil || p.PersistentPreRunE != nil || p.PersistentPostRun != nil || p.PersistentPostRunE != nil {
					t.Errorf("`%s` has a persistent hook, which the other spelling of `%s` would not run", p.CommandPath(), rel)
				}
			}
		}
	}
}

// The table is well-formed: no path appears twice, and no New sits under
// another row's Old (a move must not land inside a deprecated spelling).
func TestCommandMovesTableIsWellFormed(t *testing.T) {
	seenOld, seenNew := map[string]bool{}, map[string]bool{}
	moves := commandMoves()
	for _, m := range moves {
		if seenOld[m.Old] || seenNew[m.New] {
			t.Errorf("row %q -> %q repeats a path", m.Old, m.New)
		}
		seenOld[m.Old], seenNew[m.New] = true, true
		if m.preexisting != (m.build == nil) {
			t.Errorf("row %q -> %q: a row has a constructor exactly when it is not preexisting", m.Old, m.New)
		}
		for _, o := range moves {
			if !o.keepOld && relUnder(m.New, o.Old) {
				t.Errorf("row %q -> %q lands under %q, itself a deprecated spelling", m.Old, m.New, o.Old)
			}
		}
	}
}

// Phase 1: every old spelling is still listed, exactly as before.
func TestOldSpellingsStayVisible(t *testing.T) {
	setDeprecationPhase(t, false)
	root := newTestRootCmd(t)
	for _, m := range commandMoves() {
		for c := findRel(root, m.Old); c != nil && c != root; c = c.Parent() {
			if c.Hidden {
				t.Errorf("`%s` is hidden: phase 1 leaves every old spelling visible", c.CommandPath())
			}
		}
	}
}

// The 14 nouns, plus version, are the visible top level once the old nouns
// are hidden (phase 2), and every one of them exists today.
func TestTheFourteenNounsExist(t *testing.T) {
	root := newTestRootCmd(t)
	for _, noun := range []string{"scaffold", "project", "generate", "lint", "ci", "db", "env", "release", "obs", "cloud", "dev", "ui", "machine", "agent", "version"} {
		if c := findChildCommand(root, noun); c == nil || c.Hidden {
			t.Errorf("`forge %s` is missing or hidden", noun)
		}
	}
}

func TestLegacyRel(t *testing.T) {
	for in, want := range map[string]string{
		"cloud registry login":   "registry login",
		"cloud registry":         "registry",
		"cloud cluster connect":  "cluster connect",
		"dev cluster up":         "cluster up",
		"dev curl":               "api curl",
		"release ledger show":    "ledger show",
		"release list":           "env releases list",
		"ui sourcemaps push":     "frontend sourcemaps push",
		"machine slot status":    "slot status",
		"agent report-bug":       "report-bug",
		"env secret set":         "secret set",
		"lint fix narrow-deps":   "fix narrow-deps",
		"scaffold package":       "scaffold package", // preexisting: both spellings are old
		"cloud status":           "cloud status",
		"release verify":         "release verify",
		"dev":                    "dev",
		"registry login":         "registry login",
		"env releases":           "env releases",
		"project doctor parity":  "doctor parity",
		"cloud loginish":         "cloud loginish",
		"machine slots":          "machine slots",
		"release ledger-history": "release ledger-history",
	} {
		if got := legacyRel(in); got != want {
			t.Errorf("legacyRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLegacyArgs(t *testing.T) {
	for _, tc := range []struct {
		in, want  string
		rewritten bool
	}{
		{"cloud registry login --json", "registry login --json", true},
		// A command's own flag value is not a path word, wherever it sits.
		{"cloud registry login --username alice", "registry login --username alice", true},
		{"cloud registry login --username cloud", "registry login --username cloud", true},
		{"release list --env prod", "env releases list --env prod", true},
		{"dev cluster up --name dev", "cluster up --name dev", true},
		// A group's own persistent flag, between path words.
		{"machine storage --policy machine gc", "storage --policy machine gc", true},
		{"-C cloud cloud login", "-C cloud login", true},
		{"--project-dir=x machine slot status", "--project-dir=x slot status", true},
		{"cloud -C x registry ref web", "registry -C x ref web", true},
		{"dev cluster up dev", "cluster up dev", true},
		{"release list prod --json", "env releases list prod --json", true},
		{"registry login", "registry login", false},
		{"cloud status", "cloud status", false},
		{"generate -- cloud login", "generate -- cloud login", false},
		{"", "", false},
	} {
		got, rewritten := legacyArgs(strings.Fields(tc.in))
		if strings.Join(got, " ") != tc.want || rewritten != tc.rewritten {
			t.Errorf("legacyArgs(%q) = %q (rewritten %v), want %q (%v)", tc.in, strings.Join(got, " "), rewritten, tc.want, tc.rewritten)
		}
	}
}

// A moved command meets the project's pinned forge exactly as its old
// spelling does: the same hand-off mode, the same pin gate. The nouns that
// only hold moved commands run here: an older forge has no such command.
func TestMovedCommandsAreGatedAndHandedOffAsTheirOldSpelling(t *testing.T) {
	root := newTestRootCmd(t)
	for _, m := range commandMoves() {
		if m.preexisting {
			continue
		}
		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			rel := commandRel(c, root)
			raw := strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), root.CommandPath()))
			old := m.Old + raw[len(m.New):]
			if rel != old {
				t.Errorf("commandRel(`%s`) = %q, want %q", raw, rel, old)
			}
			if a, b := handOffModeFor(rel, false), handOffModeFor(old, false); a != b {
				t.Errorf("`%s` is handed off as %v, `%s` as %v", raw, a, old, b)
			}
			_, ga := pinGateFor(rel)
			_, gb := pinGateFor(old)
			if ga != gb {
				t.Errorf("`%s` pin-gated %v, `%s` %v", raw, ga, old, gb)
			}
			for _, k := range c.Commands() {
				walk(k)
			}
		}
		if c := findRel(root, m.New); c != nil {
			walk(c)
		}
	}
	for _, n := range commandNouns {
		if handOffModeFor(n.Path, false) != runHere {
			t.Errorf("`%s` holds only moved commands and must run as this forge", n.Path)
		}
	}
}

// The incident this guards against: a project pinned to a forge older than
// the nouns, and an agent typing the new spelling. The pinned forge is given
// the old spelling, told not to warn about it, and a noun alone stays here.
func TestHandOffGivesThePinnedForgeTheOldSpelling(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")

	h, _ := armDelegation(t)
	handed, _, err := handOffForTest(t, "release", "ledger", "show", "--json")
	if err != nil || !handed {
		t.Fatalf("handed %v, err %v", handed, err)
	}
	if got := strings.Join(h.args, " "); got != "ledger show --json" {
		t.Errorf("handed over as %q, want the old spelling", got)
	}
	if !slices.Contains(h.env, noDeprecationWarningsEnv+"=1") {
		t.Errorf("a respelled hand-off must silence the pinned forge's deprecation line; env %v", h.env)
	}

	h, _ = armDelegation(t)
	if handed, _, err := handOffForTest(t, "ledger", "show"); err != nil || !handed {
		t.Fatalf("handed %v, err %v", handed, err)
	}
	if slices.Contains(h.env, noDeprecationWarningsEnv+"=1") {
		t.Errorf("an old spelling typed as is must reach the pinned forge as is, warnings and all; env %v", h.env)
	}

	for _, args := range [][]string{{"dev"}, {"machine", "slot", "status"}, {"agent", "mcp"}} {
		h, _ := armDelegation(t)
		if handed, _, err := handOffForTest(t, args...); err != nil || handed || h.path != "" {
			t.Errorf("forge %v was handed over (handed %v, err %v); it runs as this forge", args, handed, err)
		}
	}
}

// capabilities --json lists the table, kept aliases left out, beside the
// unchanged commands/analyzers/markers.
func TestCapabilitiesListsDeprecatedAliases(t *testing.T) {
	newTestRootCmd(t) // restores the recorded route buildCapabilitiesSpec's root leaves
	var buf bytes.Buffer
	if err := runCapabilities(&buf, true); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Commands          []CommandSpec     `json:"commands"`
		DeprecatedAliases []DeprecatedAlias `json:"deprecated_aliases"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(doc.DeprecatedAliases, DeprecatedAlias{Old: "forge registry", New: "forge cloud registry"}) {
		t.Errorf("deprecated_aliases lacks registry -> cloud registry: %v", doc.DeprecatedAliases)
	}
	for _, a := range doc.DeprecatedAliases {
		if strings.HasPrefix(a.Old, "forge env releases") {
			t.Errorf("deprecated_aliases lists %q, a kept alias (owner Q4)", a.Old)
		}
	}
	paths := map[string]bool{}
	for _, c := range doc.Commands {
		paths[c.Path] = true
	}
	for _, p := range []string{"forge registry login", "forge cloud registry login", "forge env releases list", "forge release list"} {
		if !paths[p] {
			t.Errorf("commands lacks %q", p)
		}
	}
}

// --version and `version` both print the identity.
func TestVersionFlagAndCommand(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}} {
		root := newTestRootCmd(t)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("forge %v: %v", args, err)
		}
		if !strings.Contains(out.String(), versionLine()) {
			t.Errorf("forge %v printed %q, want the identity %q", args, out.String(), versionLine())
		}
	}
}

// env's help is grouped, and every visible subcommand is in a group.
func TestEnvHelpGroupsEverySubcommand(t *testing.T) {
	env := findChildCommand(newTestRootCmd(t), "env")
	for _, c := range env.Commands() {
		if c.Hidden || c.Name() == "help" {
			continue
		}
		if c.GroupID == "" {
			t.Errorf("`forge env %s` is in no help group: add it to envCommandGroups", c.Name())
		}
	}
}

// Phase 2, rehearsed: old spellings hidden (and a group left empty), one
// stderr line naming the replacement, silenced by the opt-out, and nothing
// for the new spelling or a kept alias.
func TestPhaseTwoDeprecation(t *testing.T) {
	setDeprecationPhase(t, true)
	t.Setenv(noDeprecationWarningsEnv, "")

	root := newTestRootCmd(t)
	cmdutil.RecordCmdRoute(root) // standalone: the warning names this binary
	for _, rel := range []string{"registry", "cluster", "cluster up", "api", "frontend", "package", "skill"} {
		if c := findRel(root, rel); c == nil || !c.Hidden {
			t.Errorf("`forge %s` should be hidden in phase 2", rel)
		}
	}
	for _, rel := range []string{"env releases", "env releases list", "cloud registry", "dev cluster up", "scaffold package", "release list"} {
		if c := findRel(root, rel); c == nil || c.Hidden {
			t.Errorf("`forge %s` should stay visible in phase 2", rel)
		}
	}

	warn := func(rel string) string {
		var buf bytes.Buffer
		warnDeprecatedPath(findRel(root, rel), root, &buf)
		return buf.String()
	}
	if got, want := warn("registry login"), `forge: "`+Name()+` registry login" is deprecated; use "`+Name()+` cloud registry login" (removal: v2)`+"\n"; got != want {
		t.Errorf("warning = %q, want %q", got, want)
	}
	if got := warn("package new"); !strings.Contains(got, `use "`+Name()+` scaffold package"`) {
		t.Errorf("package new warning = %q", got)
	}
	for _, rel := range []string{"cloud registry login", "env releases list", "release list", "generate"} {
		if got := warn(rel); got != "" {
			t.Errorf("`forge %s` warned: %q", rel, got)
		}
	}
	t.Setenv(noDeprecationWarningsEnv, "1")
	if got := warn("registry login"); got != "" {
		t.Errorf("%s=1 did not silence the warning: %q", noDeprecationWarningsEnv, got)
	}
}

// Phase 1 prints nothing for an old spelling.
func TestPhaseOneIsSilent(t *testing.T) {
	setDeprecationPhase(t, false)
	t.Setenv(noDeprecationWarningsEnv, "")
	root := newTestRootCmd(t)
	var buf bytes.Buffer
	warnDeprecatedPath(findRel(root, "registry login"), root, &buf)
	if buf.Len() != 0 {
		t.Errorf("phase 1 warned: %q", buf.String())
	}
}

// setDeprecationPhase runs the test in phase 2 (warnings on, old spellings
// hidden) or phase 1, whichever deprecationWarningsEnabled ships as, so both
// phases' contracts are checked in every release.
func setDeprecationPhase(t *testing.T, phaseTwo bool) {
	t.Helper()
	prev := deprecationWarningsEnabled
	deprecationWarningsEnabled = phaseTwo
	t.Cleanup(func() { deprecationWarningsEnabled = prev })
}
