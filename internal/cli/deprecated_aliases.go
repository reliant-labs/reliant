// Package cli — the command-move table: forge's old top-level spellings and
// the nouns they now live under.
//
// WHY THIS EXISTS. forge's root grew to 36 top-level commands, several of them
// one-leaf nouns (`frontend sourcemaps push`) or near-synonyms (`release`,
// `ledger`, `gate`, `env releases` all read the release ledger). The owner
// approved grouping them under 14 nouns (proposal: forge-cli-consolidation,
// §4). forge is semver at major 1, so no spelling a script, skill, CI
// workflow or reliant's own exec sites type may stop working before v2. This
// file is the ONE place that says which old path moved where; everything that
// needs to know reads commandMoves:
//
//   - mountMovedCommands builds each New path from the SAME constructor the
//     Old path uses — one definition, mounted twice — so the two cannot
//     drift: same flags, same Args, same RunE.
//   - legacyRel / legacyArgs map a new spelling back to the old one wherever
//     a path is a key (the pin gate, the hand-off to the forge a project
//     pins). Every forge has the old spelling; only this one and later have
//     the new, so a command handed to an older pinned forge is handed over in
//     the spelling it knows.
//   - `forge project capabilities --json` lists the rows as
//     deprecated_aliases, so an agent can rewrite a stale spelling
//     mechanically.
//   - warnDeprecatedPath and hideDeprecatedPaths are phase 2, switched off by
//     deprecationWarningsEnabled.
//
// PHASES (proposal §6). 1 (this release): the new paths exist, the old ones
// stay visible and silent. 2: skills, templates and hint strings move to the
// new spellings in the same release that flips deprecationWarningsEnabled —
// the old paths are hidden and print one stderr line. v2 (owner's call):
// the old paths are deleted.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
)

// deprecationWarningsEnabled is phase 2's switch. While it is false the old
// spellings are exactly what they were: visible in --help and silent. When it
// is true, hideDeprecatedPaths hides every Old path (and a group left with
// nothing visible), and warnDeprecatedPath prints one line on stderr when one
// runs. A var, not a const, only so the phase-2 behaviour is tested today.
var deprecationWarningsEnabled = false

// noDeprecationWarningsEnv silences the deprecation line (any non-empty
// value), for a script that cannot be changed yet. It also rides on a
// hand-off whose arguments legacyArgs rewrote: the user typed the new
// spelling, so the pinned forge must not warn about the old one it was given.
const noDeprecationWarningsEnv = "FORGE_NO_DEPRECATION_WARNINGS"

// commandMove is one row: the command at Old (a path relative to forge's
// root) is also mounted at New.
type commandMove struct {
	Old, New string
	// build constructs the command New mounts — the constructor Old's own
	// parent calls. nil for a preexisting row.
	build func(*moveBuilder) *cobra.Command
	// preexisting: New was already its own command beside Old before this
	// table (`scaffold package` beside `package new`). Equivalent, not one
	// definition, so nothing is mounted and nothing is rewritten for it.
	preexisting bool
	// keepOld: Old is a documented alias for good (owner, Q4: `env
	// releases` beside `release`). Never hidden, never warned about, and not
	// listed as deprecated.
	keepOld bool
}

// moveBuilder is what a row's constructor may need: the factory the
// dir-nested command groups (internal/cli/<group>) are built from, and those
// groups' constructors by name.
type moveBuilder struct {
	f      *factory.Factory
	groups map[string]factory.CmdFactory
}

// group builds the dir-nested group registered under name.
func (b *moveBuilder) group(name string) *cobra.Command {
	makeCmd, ok := b.groups[name]
	if !ok {
		panic(fmt.Sprintf("commandMoves names the command group %q, which no internal/cli/<group> registers", name))
	}
	return makeCmd(b.f)
}

// same is a row constructor that needs nothing but its own.
func same(build func() *cobra.Command) func(*moveBuilder) *cobra.Command {
	return func(*moveBuilder) *cobra.Command { return build() }
}

// fromGroup is a row constructor for a dir-nested group.
func fromGroup(name string) func(*moveBuilder) *cobra.Command {
	return func(b *moveBuilder) *cobra.Command { return b.group(name) }
}

// commandNoun is a parent that exists only to hold moved commands. Parents
// that already existed (project, lint, env, release, cloud) are not listed.
type commandNoun struct {
	Path, Short, Long string
}

// commandNouns are created, in order, before the rows mount under them.
var commandNouns = []commandNoun{
	{Path: "dev", Short: "The local inner loop: the k3d dev cluster, a debugger, and curl for an rpc"},
	{Path: "dev cluster", Short: "Manage the local k3d cluster and inspect dev state",
		Long: `Manage the local k3d development cluster and inspect dev-loop state: k3d
cluster lifecycle, ingress URLs, status and logs. The cluster config is read
from deploy/k3d.yaml (override via --config). A cluster you operate, connected
to the control plane as a deploy target, is 'cloud cluster'.`},
	{Path: "cloud cluster", Short: "Connect a cluster you operate to the control plane, as a deploy target"},
	{Path: "ui", Short: "Frontend tooling: the component library and frontend source maps"},
	{Path: "machine", Short: "This machine's housekeeping: heavy-job slots, storage, and forge's pinned tools"},
	{Path: "agent", Short: "For coding agents: skills, the MCP server, and filing a forge bug"},
}

// commandMoves returns THE table (commandMoveTable). It is reached through a
// var set in init() rather than called directly because the rows name
// command constructors, and those reach back into this file (the hand-off
// reads the table): referenced from a package-level var initializer such as
// pipelineSteps, that loop is an initialization cycle Go refuses. init()
// functions are outside that analysis.
var commandMoves func() []commandMove

func init() { commandMoves = commandMoveTable }

// commandMoveTable is the table. Order is the order capabilities lists the
// rows in.
func commandMoveTable() []commandMove {
	return []commandMove{
		// project
		{Old: "doctor", New: "project doctor", build: same(newDoctorCmd)},
		{Old: "start", New: "project start", build: same(newStartCmd)},
		// scaffold
		{Old: "package new", New: "scaffold package", preexisting: true},
		// lint
		{Old: "fix", New: "lint fix", build: fromGroup("fix")},
		// Not rows (proposal §4 places them, but neither is a move; the
		// owner decides how they merge):
		//   build [env] [receipt] → env build: `env build <env>` already
		//     exists and is a different command (env required, publishes,
		//     records releases); `build` is compile-only, env optional.
		//   ci migration-safety → lint --migration-safety: a flag of another
		//     command, with its own output and feature checks, not a path.
		// env
		{Old: "kcl", New: "env kcl", build: same(newKCLCmd)},
		{Old: "secret", New: "env secret", build: same(newSecretCmd)},
		// release: the ledger's readers, under one noun.
		{Old: "env releases list", New: "release list", build: releasesLeaf(newEnvReleasesListCmd), keepOld: true},
		{Old: "env releases show", New: "release show", build: releasesLeaf(newEnvReleasesShowCmd), keepOld: true},
		{Old: "env releases at", New: "release at", build: releasesLeaf(newEnvReleasesAtCmd), keepOld: true},
		{Old: "env releases backfill-migrations", New: "release backfill-migrations", build: same(newEnvReleasesBackfillMigrationsCmd), keepOld: true},
		{Old: "env releases interrupted", New: "release interrupted", build: same(newEnvReleasesInterruptedCmd), keepOld: true},
		{Old: "ledger", New: "release ledger", build: same(newLedgerCmd)},
		{Old: "gate", New: "release gate", build: same(newGateCmd)},
		// cloud: the control plane and its credential.
		{Old: "login", New: "cloud login", build: same(newLoginCmd)},
		{Old: "logout", New: "cloud logout", build: same(newLogoutCmd)},
		{Old: "registry", New: "cloud registry", build: same(newRegistryCmd)},
		{Old: "domain", New: "cloud domain", build: same(newDomainCmd)},
		{Old: "cluster connect", New: "cloud cluster connect", build: same(newClusterConnectCmd)},
		{Old: "cluster disconnect", New: "cloud cluster disconnect", build: same(newClusterDisconnectCmd)},
		{Old: "cluster list", New: "cloud cluster list", build: same(newClusterListCmd)},
		{Old: "cluster show", New: "cloud cluster show", build: same(newClusterShowCmd)},
		// dev: the local inner loop.
		{Old: "cluster up", New: "dev cluster up", build: same(newDevClusterUpCmd)},
		{Old: "cluster down", New: "dev cluster down", build: same(newDevClusterDownCmd)},
		{Old: "cluster reset", New: "dev cluster reset", build: same(newDevClusterResetCmd)},
		{Old: "cluster reload", New: "dev cluster reload", build: same(newDevClusterReloadCmd)},
		{Old: "cluster status", New: "dev cluster status", build: same(newDevStatusCmd)},
		{Old: "cluster recover-chart-lock", New: "dev cluster recover-chart-lock", build: same(newChartLockRecoverCmd)},
		{Old: "cluster logs", New: "dev cluster logs", build: same(newDevLogsCmd)},
		{Old: "cluster info", New: "dev cluster info", build: same(newDevInfoCmd)},
		{Old: "cluster urls", New: "dev cluster urls", build: same(newDevUrlsCmd)},
		{Old: "cluster instances", New: "dev cluster instances", build: same(newDevInstancesCmd)},
		{Old: "debug", New: "dev debug", build: fromGroup("debug")},
		{Old: "api curl", New: "dev curl", build: same(newAPICurlCmd)},
		// ui
		{Old: "component", New: "ui component", build: fromGroup("component")},
		{Old: "frontend sourcemaps", New: "ui sourcemaps", build: same(newFrontendSourcemapsCmd)},
		// machine
		{Old: "slot", New: "machine slot", build: same(newSlotCmd)},
		{Old: "storage", New: "machine storage", build: same(newStorageCmd)},
		{Old: "tools", New: "machine tools", build: same(newToolsCmd)},
		// agent
		{Old: "skill", New: "agent skill", build: same(newSkillCmd)},
		{Old: "mcp", New: "agent mcp", build: same(newMCPCmd)},
		{Old: "report-bug", New: "agent report-bug", build: same(newReportBugCmd)},
	}
}

// releasesLeaf builds an `env releases` reader with the deps its parent gives
// it in production.
func releasesLeaf(build func(releasesDeps) *cobra.Command) func(*moveBuilder) *cobra.Command {
	return func(*moveBuilder) *cobra.Command { return build(defaultReleasesDeps()) }
}

// mountMovedCommands creates commandNouns under root and mounts every row's
// New path. It panics on a table that does not fit the tree — a parent that
// is missing, or a New that is already taken — which every test run catches.
func mountMovedCommands(root *cobra.Command, b *moveBuilder) {
	for _, n := range commandNouns {
		parentPath, name := splitRel(n.Path)
		parent := mustFindRel(root, parentPath, "noun "+n.Path)
		if findChildCommand(parent, name) != nil {
			panic(fmt.Sprintf("commandNouns creates %q, which already exists", n.Path))
		}
		parent.AddCommand(cmdutil.StrictGroup(&cobra.Command{Use: name, Short: n.Short, Long: n.Long}))
	}
	for _, m := range commandMoves() {
		if m.preexisting {
			continue
		}
		parentPath, name := splitRel(m.New)
		parent := mustFindRel(root, parentPath, "row "+m.Old+" -> "+m.New)
		if findChildCommand(parent, name) != nil {
			panic(fmt.Sprintf("commandMoves mounts %q, which already exists", m.New))
		}
		c := m.build(b)
		if c.Name() != name {
			panic(fmt.Sprintf("commandMoves row %q -> %q builds a command named %q", m.Old, m.New, c.Name()))
		}
		parent.AddCommand(c)
	}
}

// splitRel splits "a b c" into ("a b", "c").
func splitRel(rel string) (parent, name string) {
	if i := strings.LastIndex(rel, " "); i >= 0 {
		return rel[:i], rel[i+1:]
	}
	return "", rel
}

// findRel walks rel's words down from root by name; "" is root itself.
func findRel(root *cobra.Command, rel string) *cobra.Command {
	c := root
	for _, w := range strings.Fields(rel) {
		if c = findChildCommand(c, w); c == nil {
			return nil
		}
	}
	return c
}

func mustFindRel(root *cobra.Command, rel, what string) *cobra.Command {
	c := findRel(root, rel)
	if c == nil {
		panic(fmt.Sprintf("commandMoves: %s needs the parent %q, which the tree does not have", what, rel))
	}
	return c
}

// relUnder reports whether rel is path or a command under it.
func relUnder(rel, path string) bool {
	return rel == path || strings.HasPrefix(rel, path+" ")
}

// legacyRel maps a command path at a new noun to the spelling every forge
// has: "cloud registry login" → "registry login". Any other path is returned
// as is. The longest matching row wins.
func legacyRel(rel string) string {
	if m, ok := movedRowFor(rel); ok {
		return m.Old + rel[len(m.New):]
	}
	return rel
}

// movedRowFor is the row whose New path rel is, or lies under.
func movedRowFor(rel string) (commandMove, bool) {
	var best commandMove
	found := false
	for _, m := range commandMoves() {
		if m.preexisting || !relUnder(rel, m.New) {
			continue
		}
		if !found || len(m.New) > len(best.New) {
			best, found = m, true
		}
	}
	return best, found
}

// isMovedNounGroup reports whether rel is one of commandNouns: a group that
// holds only moved commands and does nothing but print its help, so it runs
// as this forge whatever forge the project pins (an older one does not have
// it).
func isMovedNounGroup(rel string) bool {
	for _, n := range commandNouns {
		if rel == n.Path {
			return true
		}
	}
	return false
}

// commandRel is cmd's path relative to root, in its legacy spelling: the key
// every path-keyed table in this package is written in.
func commandRel(cmd, root *cobra.Command) string {
	return legacyRel(strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), root.CommandPath())))
}

// legacyArgs rewrites a command line (the arguments after forge's own name)
// that types a new spelling into the old one, for a forge that may predate
// the new nouns: `-C x cloud registry login --json` becomes
// `-C x registry login --json`. Only the command words change; every other
// argument keeps its place. rewritten is false, and args comes back as is,
// when the line names no moved command.
func legacyArgs(args []string) (out []string, rewritten bool) {
	idx := commandWordIndexes(args)
	words := make([]string, len(idx))
	for i, j := range idx {
		words[i] = args[j]
	}
	var best commandMove
	found := false
	for _, m := range commandMoves() {
		if m.preexisting {
			continue
		}
		newWords := strings.Fields(m.New)
		if len(newWords) > len(words) || strings.Join(words[:len(newWords)], " ") != m.New {
			continue
		}
		if !found || len(m.New) > len(best.New) {
			best, found = m, true
		}
	}
	if !found {
		return args, false
	}
	n := len(strings.Fields(best.New))
	drop := map[int]bool{}
	for _, j := range idx[:n] {
		drop[j] = true
	}
	for i, a := range args {
		if i == idx[0] {
			out = append(out, strings.Fields(best.Old)...)
		}
		if !drop[i] {
			out = append(out, a)
		}
	}
	return out, true
}

// commandWordIndexes are the positions in args of the command path's words,
// resolved against this forge's command tree the way cobra resolves it: at
// each level a flag (the command's own or one it inherits) that takes a value
// consumes the next argument, and the path ends at `--` or at the first word
// that is not a subcommand of the command reached so far. So neither a flag's
// value (`--username alice`) nor an operand is mistaken for a path word, and
// a path word after a flag (`cloud -C x registry`) is still found. A flag the
// tree does not know is taken to stand alone: this forge cannot know its
// arity.
func commandWordIndexes(args []string) []int {
	c := commandTree()
	flagsOf := func(c *cobra.Command) *pflag.FlagSet {
		fs := pflag.NewFlagSet(c.Name(), pflag.ContinueOnError)
		fs.AddFlagSet(c.Flags())
		fs.AddFlagSet(c.PersistentFlags())
		fs.AddFlagSet(c.InheritedFlags())
		return fs
	}
	fs := flagsOf(c)
	takesValue := func(f *pflag.Flag) bool { return f != nil && f.NoOptDefVal == "" }
	var idx []int
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return idx
		case strings.HasPrefix(a, "--"):
			if !strings.Contains(a, "=") && takesValue(fs.Lookup(strings.TrimPrefix(a, "--"))) {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if len(a) == 2 && takesValue(fs.ShorthandLookup(a[1:])) {
				i++
			}
		default:
			next := subcommandNamed(c, a)
			if next == nil {
				return idx
			}
			idx = append(idx, i)
			c, fs = next, flagsOf(next)
		}
	}
	return idx
}

// commandTree builds this forge's command tree for commandWordIndexes. It is
// assigned in init: NewRootCmd reaches commandWordIndexes through the
// commands' package-level run tables, so a direct reference would be an
// initialization cycle.
var commandTree func() *cobra.Command

func init() { commandTree = NewRootCmd }

// subcommandNamed is c's subcommand called, or aliased, name; nil if none.
func subcommandNamed(c *cobra.Command, name string) *cobra.Command {
	for _, child := range c.Commands() {
		if child.Name() == name || child.HasAlias(name) {
			return child
		}
	}
	return nil
}

// deprecatedPathFor is the row whose Old path rel is, or lies under, when
// that path is deprecated (not a kept alias, not a preexisting equivalent's).
// The longest matching row wins.
func deprecatedPathFor(rel string) (commandMove, bool) {
	var best commandMove
	found := false
	for _, m := range commandMoves() {
		if m.keepOld || !relUnder(rel, m.Old) {
			continue
		}
		if !found || len(m.Old) > len(best.Old) {
			best, found = m, true
		}
	}
	return best, found
}

// warnDeprecatedPath prints the one deprecation line for a run of an old
// spelling (phase 2), on stderr: stdout, --json and the exit code are
// untouched. Silent while deprecationWarningsEnabled is off, and under
// FORGE_NO_DEPRECATION_WARNINGS.
func warnDeprecatedPath(cmd, root *cobra.Command, w io.Writer) {
	if !deprecationWarningsEnabled || os.Getenv(noDeprecationWarningsEnv) != "" {
		return
	}
	rel := strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), root.CommandPath()))
	m, ok := deprecatedPathFor(rel)
	if !ok {
		return
	}
	name := Name()
	_, _ = fmt.Fprintf(w, "forge: %q is deprecated; use %q (removal: v2)\n",
		name+" "+rel, name+" "+m.New+rel[len(m.Old):])
}

// hideDeprecatedPaths hides every deprecated Old path (phase 2), and then any
// group that is left with no visible subcommand (`cluster` once all of its
// leaves have moved). A no-op while
// deprecationWarningsEnabled is off.
func hideDeprecatedPaths(root *cobra.Command) {
	if !deprecationWarningsEnabled {
		return
	}
	for _, m := range commandMoves() {
		if m.keepOld {
			continue
		}
		c := findRel(root, m.Old)
		if c == nil {
			continue
		}
		c.Hidden = true
		for p := c.Parent(); p != nil && p != root && !hasVisibleChild(p); p = p.Parent() {
			p.Hidden = true
		}
	}
}

// hasVisibleChild reports whether c has a subcommand --help would list.
func hasVisibleChild(c *cobra.Command) bool {
	for _, child := range c.Commands() {
		if !child.Hidden && child.Name() != "help" && child.Name() != "completion" {
			return true
		}
	}
	return false
}

// DeprecatedAlias is one row of capabilities' deprecated_aliases: Old still
// runs, New is the spelling to write. Both are full paths ("forge registry").
type DeprecatedAlias struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// deprecatedAliases lists the table for capabilities, kept aliases left out.
func deprecatedAliases(root *cobra.Command) []DeprecatedAlias {
	var out []DeprecatedAlias
	for _, m := range commandMoves() {
		if m.keepOld {
			continue
		}
		out = append(out, DeprecatedAlias{Old: root.CommandPath() + " " + m.Old, New: root.CommandPath() + " " + m.New})
	}
	return out
}
