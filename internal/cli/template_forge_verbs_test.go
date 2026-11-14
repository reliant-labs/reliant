package cli

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/templates"
)

// TestScaffoldTemplatesRunOnlyReleasedForgeCommands: every forge command a
// scaffold template RUNS — a Taskfile task, a CI step, an air pre-build hook —
// exists in the forge the scaffolded project pins.
//
// The pin is not always this binary. A forge built from a checkout is on no
// module proxy, so `forge project new` pins forge_version to the newest
// release its source descends from (buildinfo.PublishedFloor: the VERSION
// file), and the project's Taskfile (`go run …/cmd/forge@<forge_version>`)
// and CI run that release. A template that calls a command newer than the
// release breaks every project such a forge scaffolds: the Taskfile once ran
// `forge project frontends`, which the pinned release did not have (#1374).
//
// So each invocation is checked twice, offline:
//
//  1. against THIS binary's command tree (NewRootCmd, hidden commands and
//     aliases included): the command exists, and a command group is given
//     one of its subcommands;
//  2. against testdata/released_commands.txt, the command tree of a release
//     (scripts/released-commands.sh writes it from the release's own source):
//     the command existed in the release a checkout build pins.
//
// The released file may name an OLDER release than VERSION (it is refreshed
// by hand, after a release a template wants to use): that only makes check 2
// stricter. It may never name a newer one.
//
// Not checked: flags (the trees record commands only); commands named in
// prose — comments, docs, skills — which nothing executes; and a `forge
// <word>` whose first word is no forge command at all, which in a run file is
// prose too (a YAML desc, a step name).
func TestScaffoldTemplatesRunOnlyReleasedForgeCommands(t *testing.T) {
	tree := forgeCommandTree(newTestRootCmd(t))
	releasedVersion, released := readReleasedCommands(t)

	if problem := releasedSnapshotProblem(releasedVersion, buildinfo.PublishedFloor()); problem != "" {
		t.Fatal(problem)
	}

	invocations := scaffoldTemplateForgeInvocations(t)
	if len(invocations) < 20 {
		t.Fatalf("found only %d forge invocations in the scaffold templates; the scan has stopped seeing them", len(invocations))
	}
	var problems []string
	for _, inv := range invocations {
		// A first word that is no forge command at all is prose in a
		// string ("everything forge generates", "Name: forge setup
		// client"), not an invocation. What a template gets wrong is the
		// path below a real command — `forge project frontends`.
		if _, ok := tree["forge "+inv.words[0]]; !ok {
			continue
		}
		if problem := releasedInvocationProblem(inv.words, tree, released, releasedVersion); problem != "" {
			problems = append(problems, fmt.Sprintf("%s:%d: %s\n      %s", inv.file, inv.line, problem, strings.TrimSpace(inv.text)))
		}
	}
	if len(problems) > 0 {
		t.Errorf("scaffold templates run forge commands the pinned forge does not have:\n  %s", strings.Join(problems, "\n  "))
	}
}

// The resolver itself, on the case that motivated the test and its kin.
func TestResolveForgeInvocation(t *testing.T) {
	leaf, group := forgeCommand{runs: true, takesArgs: true}, forgeCommand{}
	tree := map[string]forgeCommand{
		"forge project":     {runs: true}, // runnable, but its Use declares no argument
		"forge project new": leaf, "forge generate": leaf, "forge obs": group,
		"forge obs issue": leaf, "forge obs issue resolve": leaf, // issue <id> [env] has subcommands too
		"forge cluster": group,
	}
	for _, c := range []struct {
		words      string
		path, want string // want: a substring of the problem; "" is none
	}{
		{"generate --no-cache", "forge generate", ""},
		{"project new demo", "forge project new", ""},
		{"obs issue ISS-1 prod", "forge obs issue", ""},
		{"obs issue resolve ISS-1", "forge obs issue resolve", ""},
		{"project frontends", "forge project", "has no subcommand \"frontends\""},
		{"cluster", "forge cluster", "is a command group"},
		{"obs nope", "forge obs", "has no subcommand \"nope\""},
		{"frobnicate now", "forge", "is not a forge command"},
	} {
		got, problem := resolveForgeInvocation(strings.Fields(c.words), tree)
		if got != c.path || (c.want == "") != (problem == "") || !strings.Contains(problem, c.want) {
			t.Errorf("resolve(%q) = %q, %q; want %q, a problem containing %q", c.words, got, problem, c.path, c.want)
		}
	}
}

// The release check rejects a command this binary has but the release lacks,
// and a snapshot newer than the release this source descends from.
func TestReleasedCommandChecks(t *testing.T) {
	leaf := forgeCommand{runs: true, takesArgs: true}
	tree := map[string]forgeCommand{
		"forge project": {runs: true}, "forge project new": leaf, "forge project frontends": leaf,
	}
	released := map[string]bool{"forge project": true, "forge project new": true}
	for _, c := range []struct {
		words, want string // want: a substring of the problem; "" is none
	}{
		{"project new demo", ""},
		{"project frontends", "`forge project frontends` is not in forge v1.0.15"},
		{"project nope", "has no subcommand \"nope\""}, // the resolver's problem wins
	} {
		problem := releasedInvocationProblem(strings.Fields(c.words), tree, released, "v1.0.15")
		if (c.want == "") != (problem == "") || !strings.Contains(problem, c.want) {
			t.Errorf("releasedInvocationProblem(%q) = %q; want a problem containing %q", c.words, problem, c.want)
		}
	}

	for _, c := range []struct {
		snapshot, floor string
		bad             bool
	}{
		{"v1.0.15", "v1.0.15", false},
		{"v1.0.14", "v1.0.15", false}, // older: only stricter
		{"v1.0.16", "v1.0.15", true},  // newer than the pinned release
		{"", "v1.0.15", true},
		{"1.0.15", "v1.0.15", true},
	} {
		if got := releasedSnapshotProblem(c.snapshot, c.floor); (got != "") != c.bad {
			t.Errorf("releasedSnapshotProblem(%q, %q) = %q; want a problem: %v", c.snapshot, c.floor, got, c.bad)
		}
	}
}

// releasedSnapshotProblem reports why a snapshot of release snapshot cannot
// stand for floor, the release this source descends from: it must be a valid
// version no newer than floor.
func releasedSnapshotProblem(snapshot, floor string) string {
	if !semver.IsValid(snapshot) || (semver.IsValid(floor) && semver.Compare(snapshot, floor) > 0) {
		return fmt.Sprintf("testdata/released_commands.txt describes forge %q, but this source descends from release %q "+
			"(internal/buildinfo/VERSION): the file must describe that release or an older one. "+
			"Regenerate it: scripts/released-commands.sh", snapshot, floor)
	}
	return ""
}

// releasedInvocationProblem resolves words against this binary's tree, then
// requires the resolved command to be in released, the command paths of
// releasedVersion.
func releasedInvocationProblem(words []string, tree map[string]forgeCommand, released map[string]bool, releasedVersion string) string {
	cmdPath, problem := resolveForgeInvocation(words, tree)
	if problem == "" && !released[cmdPath] {
		problem = fmt.Sprintf("`%s` is not in forge %s, the release a forge built from this checkout pins "+
			"(buildinfo.PublishedFloor), so a project it scaffolds would run a forge without it. "+
			"Use the command in templates once a release ships it, then refresh "+
			"testdata/released_commands.txt with scripts/released-commands.sh", cmdPath, releasedVersion)
	}
	return problem
}

// forgeCommand is what the resolver needs to know of one command.
type forgeCommand struct {
	// runs: invoked alone, it does something (a group with no Run prints
	// its help and exits).
	runs bool
	// takesArgs: a word after it that is no subcommand is its argument.
	// A command with subcommands takes arguments only when its Use
	// declares them ("issue <id> [env]"); otherwise cobra takes the word
	// for a subcommand it does not have.
	takesArgs bool
}

// forgeCommandTree is every command path of root ("forge a b", aliases
// included, help and completion left out).
func forgeCommandTree(root *cobra.Command) map[string]forgeCommand {
	tree := map[string]forgeCommand{}
	var walk func(c *cobra.Command, prefix string)
	walk = func(c *cobra.Command, prefix string) {
		for _, child := range c.Commands() {
			if child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			takesArgs := child.Runnable() && (!child.HasSubCommands() || len(strings.Fields(child.Use)) > 1)
			for _, name := range append([]string{child.Name()}, child.Aliases...) {
				p := prefix + " " + name
				tree[p] = forgeCommand{runs: child.Runnable(), takesArgs: takesArgs}
				walk(child, p)
			}
		}
	}
	walk(root, "forge")
	return tree
}

// resolveForgeInvocation walks words (what follows `forge`) down tree as far
// as they name commands; the rest are the command's arguments. It reports a
// problem when the first word is no command, when the walk stops on a command
// whose next word can be neither a subcommand nor an argument, and when it
// ends on a group that does nothing when run alone.
func resolveForgeInvocation(words []string, tree map[string]forgeCommand) (cmdPath, problem string) {
	cmdPath = "forge"
	i := 0
	for ; i < len(words); i++ {
		if _, ok := tree[cmdPath+" "+words[i]]; !ok {
			break
		}
		cmdPath += " " + words[i]
	}
	cmd := tree[cmdPath]
	switch {
	case cmdPath == "forge":
		return cmdPath, fmt.Sprintf("`forge %s` is not a forge command", words[0])
	case i < len(words) && !cmd.takesArgs:
		return cmdPath, fmt.Sprintf("`%s %s`: %s has no subcommand %q", cmdPath, words[i], cmdPath, words[i])
	case i == len(words) && !cmd.runs:
		return cmdPath, fmt.Sprintf("`%s` is a command group, not a command", cmdPath)
	}
	return cmdPath, ""
}

// readReleasedCommands reads testdata/released_commands.txt: the release it
// describes (its first line, "# forge vX.Y.Z: …") and its command paths.
func readReleasedCommands(t *testing.T) (string, map[string]bool) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "released_commands.txt"))
	if err != nil {
		t.Fatalf("%v: write it with scripts/released-commands.sh", err)
	}
	defer f.Close()
	var version string
	paths := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#"):
			if version == "" {
				if rest, ok := strings.CutPrefix(line, "# forge "); ok {
					version, _, _ = strings.Cut(rest, ":")
				}
			}
		default:
			paths[line] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return version, paths
}

// forgeInvocation is one `forge …` a template runs.
type forgeInvocation struct {
	file  string
	line  int
	text  string
	words []string
}

// forgeInvocationRE finds `forge <words>` (or the Taskfile's `{{.FORGE}}
// <words>`) as a command, not as part of a path or a longer name
// (cmd/forge@…, forge.yaml, forge-deploy).
var forgeInvocationRE = regexp.MustCompile("(?:^|[\\s;|&(`\"'=])(?:forge|\\{\\{\\s*\\.FORGE\\s*\\}\\})((?:[ \\t]+[a-z][a-z0-9-]*)+)")

// templateRawAction is a Go-template action that prints a raw string — how a
// template whose output is itself templated (a Taskfile) writes a literal
// {{…}}: {{`{{.FORGE}}`}} renders as {{.FORGE}}.
var templateRawAction = regexp.MustCompile("\\{\\{-?\\s*`([^`]*)`\\s*-?\\}\\}")

// scaffoldTemplateForgeInvocations scans the templates whose content is RUN
// — Taskfiles, CI workflows and actions, compose and air configs, shell
// scripts, Dockerfiles — for forge invocations, skipping comment lines.
// Templates whose forge mentions are prose (Go, KCL, TypeScript, Markdown,
// skills) are not scanned.
func scaffoldTemplateForgeInvocations(t *testing.T) []forgeInvocation {
	t.Helper()
	categories := map[string]templates.TemplateCategory{
		"project": templates.ProjectTemplates(), "frontend": templates.FrontendTemplates(),
		"deploy": templates.DeployTemplates(), "ingress": templates.IngressTemplates(),
		"test": templates.TestTemplates(), "internal-package": templates.InternalPkgTemplates(),
		"service": templates.ServiceTemplates(), "webhook": templates.WebhookTemplates(),
		"worker": templates.WorkerTemplates(), "worker-cron": templates.WorkerCronTemplates(),
		"operator": templates.OperatorTemplates(), "ci/github": templates.CITemplates("github"),
	}
	unescape := strings.NewReplacer(`{{"{{"}}`, "{{", `{{"}}"}}`, "}}", `{{ "{{" }}`, "{{", `{{ "}}" }}`, "}}")
	var out []forgeInvocation
	names := make([]string, 0, len(categories))
	for n := range categories {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, category := range names {
		cat := categories[category]
		files, err := cat.List("")
		if err != nil {
			t.Fatalf("list %s templates: %v", category, err)
		}
		for _, name := range files {
			if !templateIsRun(name) {
				continue
			}
			body, err := cat.Get(name)
			if err != nil {
				t.Fatalf("read %s/%s: %v", category, name, err)
			}
			text := templateRawAction.ReplaceAllString(unescape.Replace(string(body)), "$1")
			for i, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				for _, m := range forgeInvocationRE.FindAllStringSubmatch(line, -1) {
					out = append(out, forgeInvocation{
						file: path.Join("internal/templates", category, name), line: i + 1, text: line,
						words: strings.Fields(m[1]),
					})
				}
			}
		}
	}
	return out
}

// templateIsRun reports whether a template's content is executed in the
// scaffolded project, by its file name with any .tmpl suffix removed.
func templateIsRun(name string) bool {
	base := path.Base(strings.TrimSuffix(name, ".tmpl"))
	switch ext := path.Ext(base); {
	case ext == ".yml" || ext == ".yaml" || ext == ".toml" || ext == ".sh":
		return true
	case strings.HasPrefix(base, "Dockerfile") || base == "Makefile":
		return true
	}
	return false
}
