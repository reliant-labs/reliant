package codegen

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// DeclaredWorkload is what one `<ident> = fw.Workload {...}` literal in
// deploy/kcl/workloads.k RUNS: its name, the cmd of its GoBuild (empty when
// it builds nothing forge compiles), its args, and the components it states
// it serves.
//
// It is read from the source rather than evaluated, because its readers —
// `forge lint` and `forge scaffold` — must work on a checkout that has never
// rendered, and must not need kcl on PATH to do it.
type DeclaredWorkload struct {
	Name     string
	BuildCmd string
	Args     []string
	// Kind is the literal `kind = "<k>"` (WorkloadKind*), or "" when the
	// kind is not a string literal forge can read.
	Kind string
	// Serves lists the components this workload's process runs besides one
	// named for itself: the `serves` field, for a subcommand that mounts
	// several components (control-plane's `admin-api`).
	Serves []string
}

var (
	// workloadLiteral matches the opening of a top-level workload binding.
	workloadLiteral = regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*\s*=\s*fw\.Workload\s*\{`)
	// workloadNameField matches a `name = "<x>"` field on a line of its own.
	workloadNameField = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"\s*$`)
	// workloadKindField matches a `kind = "<k>"` field on a line of its own.
	workloadKindField = regexp.MustCompile(`(?m)^\s*kind\s*=\s*"([^"]+)"\s*$`)
	// goBuildCmd captures the cmd of a GoBuild literal.
	goBuildCmd = regexp.MustCompile(`GoBuild\s*\{[^}]*\bcmd\s*=\s*"([^"]+)"`)
	// buildRef captures a build bound to a variable: `build = _cp_build`.
	buildRef = regexp.MustCompile(`(?m)^\s*build\s*=\s*([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	// kclString matches one double-quoted KCL string.
	kclString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// DeclaredWorkloads parses each workload literal in a workloads.k, keyed by
// its declared name.
//
// Docstrings and comments are stripped first (StripKCLProse), because the
// scaffolded workloads.k DOCUMENTS the shape it expects, including worked
// examples, and an example is not a declaration. A literal this cannot read
// is simply absent.
//
// A build bound to a top-level variable (`build = _cp_build`, where
// `_cp_build = forge.GoBuild {...}`) is resolved, because stating the one
// project build once and naming it from every workload is how a project
// with several workloads on one binary writes it.
func DeclaredWorkloads(src string) map[string]DeclaredWorkload {
	src = StripKCLProse(src)
	out := map[string]DeclaredWorkload{}
	for _, loc := range workloadLiteral.FindAllStringIndex(src, -1) {
		body, ok := kclBlockBody(src, loc[1])
		if !ok {
			continue
		}
		m := workloadNameField.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		w := DeclaredWorkload{
			Name:   m[1],
			Args:   kclStringListField(body, "args"),
			Serves: kclStringListField(body, "serves"),
		}
		if k := workloadKindField.FindStringSubmatch(body); k != nil {
			w.Kind = k[1]
		}
		if b := goBuildCmd.FindStringSubmatch(body); b != nil {
			w.BuildCmd = b[1]
		} else if ref := buildRef.FindStringSubmatch(body); ref != nil {
			bound := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(ref[1]) + `\s*=\s*forge\.GoBuild\s*\{[^}]*\bcmd\s*=\s*"([^"]+)"`)
			if b := bound.FindStringSubmatch(src); b != nil {
				w.BuildCmd = b[1]
			}
		}
		out[w.Name] = w
	}
	return out
}

// kclStringListField returns the strings of a `<field> = [...]` list in a
// literal body, or nil when the field is absent. The list is delimited by
// scanKCLList, not a pattern, so a bracket inside a string cannot end it.
func kclStringListField(body, field string) []string {
	head := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(field) + `\s*=\s*\[`).FindStringIndex(body)
	if head == nil {
		return nil
	}
	list, ok := scanKCLList(body, head[1]-1)
	if !ok {
		return nil
	}
	// Non-nil even when empty: `serves = []` is still a statement.
	out := []string{}
	for _, s := range kclString.FindAllStringSubmatch(list.code, -1) {
		out = append(out, s[1])
	}
	return out
}

// kclBlockBody returns the text between the `{` that ends at open and its
// matching `}`, skipping braces inside string literals.
func kclBlockBody(src string, open int) (string, bool) {
	depth := 1
	inString := false
	for i := open; i < len(src); i++ {
		c := src[i]
		switch {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return src[open:i], true
			}
		}
	}
	return "", false
}

// RunByGraphRoot reports whether component c runs only inside a root of the
// project binary's composition graph, so it has no subcommand of its own and
// no workload of its own to declare.
//
// A project that composes with the graph (GraphComposition) generates ONE
// command per root (cmd/<bin>/cmd/roots_gen.go) and none per component: the
// default `server` root mounts every service and supervises every worker. A
// workload declared `args = ["<service>"]` there names a command the binary
// does not have, and its pod crash-loops on "unknown command". Splitting a
// component into a process of its own is a new `//forge:root` in
// internal/app, whose workload binds it with `config_gen.root_of("<root>")`.
//
// Operators, jobs, tools and secondary binaries are not covered: they deploy
// as workloads of their own.
func RunByGraphRoot(projectDir string, c config.ComponentConfig) bool {
	if c.EffectiveKind() == config.ComponentKindBinary || !RunsInServer(WorkloadKindFor(c.EffectiveKind())) {
		return false
	}
	return GraphComposition(projectDir)
}

// BinaryCommandWords is every command name the project binary's cmd tree
// (cmd/<projectName>/...) declares with a literal cobra `Use:` — the first
// word of each, at any depth: the generated root commands, the built-ins,
// and the project's own (commands.go). Empty when the tree is not on disk.
//
// It is read from source, like DeclaredWorkloads, so `forge lint` judges a
// checkout that has never been built. A command whose Use is not a string
// literal is invisible to it.
func BinaryCommandWords(projectDir, projectName string) map[string]bool {
	words := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(projectDir, "cmd", projectName), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range cobraUseLiteral.FindAllStringSubmatch(string(src), -1) {
			if f := strings.Fields(m[1]); len(f) > 0 {
				words[f[0]] = true
			}
		}
		return nil
	})
	return words
}

// cobraUseLiteral matches a cobra command's `Use: "<word> ..."` field on a
// line of its own (so a commented-out example does not count).
var cobraUseLiteral = regexp.MustCompile(`(?m)^\s*Use:\s*"([^"]*)"`)

// ServingWorkload names the declared workload whose process ALREADY runs
// component c, when that workload is not c's own declaration. Two cases, and
// both are facts forge can read rather than guess:
//
//   - A workload that lists c in `serves`. A subcommand that mounts several
//     components (control-plane's `admin-api` mounts six admin services) is
//     ONE workload; `serves` is how it says which components it runs. forge
//     cannot see that from Go — the mount set lives in the project's own
//     code — so the declaration states it.
//   - A workload running the project binary's `server` command, which runs
//     every service, worker and operator in one process (cmd-tree-server's
//     MountAll + AllWorkers + AllOperators). A secondary binary is its own
//     program and is never served by it.
//   - A workload running c's OWN subcommand of the project binary under
//     another name (control-plane's `workspace-controller` runs `workspace`,
//     the workspace operator's subcommand): that is c's declaration, named
//     for the process rather than the component.
//
// Such a component needs no workload of its own: declaring one would deploy
// it a second time, as its own Deployment, beside the process that already
// serves it.
func ServingWorkload(declared map[string]DeclaredWorkload, projectName string, c config.ComponentConfig) (string, bool) {
	names := sortedWorkloadNames(declared)
	for _, name := range names {
		if declared[name].ServesComponent(c.Name) {
			return name, true
		}
	}
	if c.EffectiveKind() == config.ComponentKindBinary {
		return "", false
	}
	for _, name := range names {
		w := declared[name]
		if ProjectBinaryRuns(projectName, w.BuildCmd) && len(w.Args) > 0 && w.Args[0] == ServerSubcommand {
			return w.Name, true
		}
	}
	for _, name := range names {
		if RunsOwnSubcommand(declared[name], projectName, c) {
			return name, true
		}
	}
	return "", false
}

// RunsOwnSubcommand reports whether w runs c's own subcommand of the
// project binary — the one `forge scaffold` writes in c's stanza.
func RunsOwnSubcommand(w DeclaredWorkload, projectName string, c config.ComponentConfig) bool {
	return c.EffectiveKind() != config.ComponentKindBinary &&
		ProjectBinaryRuns(projectName, w.BuildCmd) && len(w.Args) > 0 && w.Args[0] == componentSubcommand(c)
}

// ComposedProcesses names the declared workloads through which the project
// runs its components of c's workload kind in processes it composes itself,
// when that is how it runs them: control-plane's one binary mounts every
// service into `public-api` or `admin-api` and runs every worker in
// `workers`, so no service or worker has a workload of its own.
//
// A composed process is a workload of that kind running the project binary
// through a subcommand that is neither one the cmd tree owns (`server`,
// `db …`) nor any component's own subcommand, and that does not state its
// components in `serves` (one that does is read by ServingWorkload, and
// what it leaves out is left out). Its mount set lives in the
// project's Go, so forge cannot tell which one, if any, a NEW component
// belongs to — only that the project's convention is not one workload per
// component. That convention is read from the other components of the kind:
// unless most of them run without a workload of their own (their name, or
// their own subcommand), the project declares per component, and this
// returns nil. So does a kind that never shares a process (a job, a tool).
//
// `forge scaffold` declares nothing for c when this returns workloads, and
// `forge lint` does not report c undeclared: the two read the file the same
// way. Listing c in a composed workload's `serves` states where it runs.
func ComposedProcesses(declared map[string]DeclaredWorkload, projectName string, components []config.ComponentConfig, c config.ComponentConfig) []string {
	kind := WorkloadKindFor(c.EffectiveKind())
	if c.EffectiveKind() == config.ComponentKindBinary ||
		(kind != WorkloadKindService && kind != WorkloadKindWorker && kind != WorkloadKindOperator) {
		return nil
	}
	own := map[string]bool{componentSubcommand(c): true}
	for _, o := range components {
		if o.EffectiveKind() != config.ComponentKindBinary {
			own[componentSubcommand(o)] = true
		}
	}
	names := sortedWorkloadNames(declared)
	var composed []string
	for _, name := range names {
		w := declared[name]
		// A workload with `serves` has stated what it runs: a component it
		// does not list is not in it.
		if w.Kind != kind || w.Serves != nil || !ProjectBinaryRuns(projectName, w.BuildCmd) || len(w.Args) == 0 ||
			RunsBuiltinSubcommand(w.Args) || own[w.Args[0]] {
			continue
		}
		composed = append(composed, name)
	}
	if len(composed) == 0 {
		return nil
	}
	// The convention is what most of the kind's other components do. One
	// worker with a process of its own beside thirty in `workers` does not
	// make the thirty-first its own workload; one custom process beside
	// services that each have their own does not stop the next one getting
	// its own. With no other component to go by, declare (the default).
	ownWorkload, shared := 0, 0
	for _, o := range components {
		if SameWorkload(o.Name, c.Name) || WorkloadKindFor(o.EffectiveKind()) != kind || o.EffectiveKind() == config.ComponentKindBinary {
			continue
		}
		if hasOwnWorkload(declared, names, projectName, o) {
			ownWorkload++
		} else {
			shared++
		}
	}
	if shared <= ownWorkload {
		return nil
	}
	return composed
}

// hasOwnWorkload reports whether c is declared by name or run through its
// own subcommand.
func hasOwnWorkload(declared map[string]DeclaredWorkload, names []string, projectName string, c config.ComponentConfig) bool {
	for _, name := range names {
		if SameWorkload(name, c.Name) || RunsOwnSubcommand(declared[name], projectName, c) {
			return true
		}
	}
	return false
}

// ComposedProcessHint is what `forge scaffold` prints, in place of writing,
// when the project runs components of c's kind in processes it composes
// (ComposedProcesses).
func ComposedProcessHint(modulePath, projectName string, c config.ComponentConfig, composed []string) string {
	var b strings.Builder
	kind := WorkloadKindFor(c.EffectiveKind())
	fmt.Fprintf(&b, "%s runs this project's %ss in processes it composes itself (%s),\n",
		WorkloadsKCLRelPath, kind, strings.Join(composed, ", "))
	fmt.Fprintf(&b, "   not one workload each, so no workload was declared for %s %q. Either:\n\n", kind, c.Name)
	fmt.Fprintf(&b, "     - mount it in the subcommand that should run it, and add %q to that\n", c.Name)
	b.WriteString("       workload's `serves` list so forge knows where it runs, or\n")
	b.WriteString("     - give it a workload of its own: add this, and its name to ALL:\n\n")
	b.WriteString(WorkloadStanza(modulePath, projectName, c))
	return b.String()
}

func sortedWorkloadNames(declared map[string]DeclaredWorkload) []string {
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DeclaresServes reports whether any workload in the file declares `serves`
// — the project's own statement that it runs several components through one
// subcommand. In such a project a new component may belong to one of those
// processes, which forge cannot see, so `forge scaffold` shows the choice
// instead of declaring a workload the project most likely does not want.
func DeclaresServes(declared map[string]DeclaredWorkload) bool {
	for _, w := range declared {
		if w.Serves != nil {
			return true
		}
	}
	return false
}

// ServesComponent reports whether w lists the component named name in its
// `serves`. Names compare by KCL identifier, so `overview-admin` and
// `overview_admin` are the same component, as they are everywhere else in
// this file.
func (w DeclaredWorkload) ServesComponent(name string) bool {
	ident := naming.KCLIdentifier(name)
	for _, s := range w.Serves {
		if naming.KCLIdentifier(s) == ident {
			return true
		}
	}
	return false
}

// ServesHint is the one sentence `forge lint` and `forge scaffold` both use to
// say how a component served by another workload's process is marked as such.
func ServesHint(componentName string) string {
	return fmt.Sprintf("If a workload that already runs serves it (a subcommand that mounts several components), "+
		"add %q to that workload's `serves` list in %s instead of declaring a new workload.",
		componentName, WorkloadsKCLRelPath)
}

// ServesChoiceHint is what `forge scaffold` prints, in place of writing,
// when the project declares `serves` and the new component is in none.
func ServesChoiceHint(modulePath, projectName string, c config.ComponentConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s groups components into shared processes (`serves`), so forge cannot tell\n", WorkloadsKCLRelPath)
	fmt.Fprintf(&b, "   which workload, if any, should run %s %q. Either:\n\n", c.EffectiveKind(), c.Name)
	fmt.Fprintf(&b, "     - add %q to the `serves` list of the workload whose subcommand mounts it, or\n", c.Name)
	b.WriteString("     - give it a workload of its own: add this, and its name to ALL:\n\n")
	b.WriteString(WorkloadStanza(modulePath, projectName, c))
	return b.String()
}
