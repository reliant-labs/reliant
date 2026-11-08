package codegen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	forgeconfig "github.com/reliant-labs/forge/pkg/config"
)

// Binding rules for (forge.v1.binary_config).
//
// A binary-bound config message is the whole config surface of one process:
// forge generates its loader, its KCL schema and its env projection, and the
// process's Deployment carries exactly those env vars. That only means
// something when the binding names a process that exists. A binding to a
// binary nothing builds still generates all of it, cleanly, and nothing ever
// reads any of it — so a typo, a renamed cmd/ directory, or a subcommand line
// written where a binary name belongs (`binary: "x server api"`) used to
// generate green and leave a config that no process loads.
//
// forge therefore refuses the binding at GENERATE time, before any artifact
// is written: the same rule (forge.v1.frontend_config) enforces for a
// frontend forge.yaml does not declare. A lint warning would arrive after the
// projection had already been wired into an environment.
//
// The `command` half is validated for SHAPE only. Whether a cobra command
// path exists is not knowable from the source without guessing (see
// BinaryConfigOptions.command in forge.proto); the generated loader checks it
// exactly, at runtime, against the real tree.

// bindingProblemKind classifies a refused binding, so the runbook's footer
// says only what applies to the problems actually found.
type bindingProblemKind int

const (
	bindingUnknownBinary bindingProblemKind = iota
	bindingBadCommand
	bindingDuplicate
	bindingProcessCollision
)

// BinaryConfigBindingProblem is one refused binding.
type BinaryConfigBindingProblem struct {
	// Message is the config message carrying the annotation.
	Message string
	// Where is "proto/<file>:<line>", or "" when the descriptor carries no
	// source position.
	Where string
	// Binding renders the annotation as written, e.g.
	// `{binary: "reliant", command: ["server", "api"]}`.
	Binding string
	// Detail explains this one offender: what is wrong and, where forge can
	// tell, what was probably meant.
	Detail string

	kind  bindingProblemKind
	order int // the message's declaration index
}

// ProjectBinaries lists the binaries a (forge.v1.binary_config).binary may
// name: the directory leaves under projectDir/cmd.
//
// A project with no cmd/ directory has NO binaries (an empty, non-nil
// slice), so a binding there is refused. nil means UNKNOWN — cmd/ exists but
// could not be read — and the existence check is then skipped rather than
// reporting every binding as dangling: "forge could not tell" must never
// render as "it does not exist".
func ProjectBinaries(projectDir string) []string {
	entries, err := os.ReadDir(filepath.Join(projectDir, "cmd"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}
		}
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// ProcessName renders the process a binding names, as it is typed at a
// shell: "reliant" for a root config, "reliant server api" for a command
// config. Empty for a message bound to no binary.
func (m ConfigMessage) ProcessName() string {
	if m.Binary == "" {
		return ""
	}
	return strings.TrimSpace(m.Binary + " " + strings.Join(m.Command, " "))
}

// ValidateBinaryConfigBindings refuses every (forge.v1.binary_config)
// binding that cannot mean what it says:
//
//   - binary names no cmd/<name> directory. binaries is the set
//     ProjectBinaries returned; nil skips this check (see ProjectBinaries).
//   - command is set without a binary, or an element is not ONE command
//     name (empty, contains whitespace, or is a flag).
//   - two messages bind the same (binary, command). A process has ONE
//     config at each level — one Load<Msg>, one KCL schema its env is
//     projected from — so a second message for it has no defined meaning,
//     and the projection silently used whichever came first.
//   - a binary's root config and one of its command configs declare the same
//     env var or flag. The process running that command loads both, so it
//     would read one value from two declarations that can drift, and its
//     env projection would carry the key twice.
//   - a binary's root config and one of its command configs declare a
//     top-level field of the same name. The process reads both from ONE
//     config file, flat across the two (forge/pkg/config's file layer), so
//     the file could not say which of them a key sets.
//
// It reports EVERY offender rather than the first, for the reason
// ValidateFrontendConfigs does: a renamed binary takes several messages with
// it, and fixing them one generate-run at a time is miserable.
func ValidateBinaryConfigBindings(messages []ConfigMessage, binaries []string) error {
	known := map[string]bool{}
	for _, b := range binaries {
		known[b] = true
	}

	var problems []BinaryConfigBindingProblem
	index := map[*ConfigMessage]int{}
	refuse := func(m *ConfigMessage, kind bindingProblemKind, detail string) {
		problems = append(problems, BinaryConfigBindingProblem{
			Message: m.Name,
			Where:   configMessageWhere(*m),
			Binding: bindingLiteral(*m),
			Detail:  detail,
			kind:    kind,
			order:   index[m],
		})
	}

	// Bindings that name a real process, grouped by it. A binding already
	// refused for its own shape names no process, so it joins no group:
	// reporting it again as a duplicate of something it only resembles
	// would be noise.
	byProcess := map[string][]*ConfigMessage{}
	var order []string
	var subtreeConfigs []*ConfigMessage
	for i := range messages {
		m := &messages[i]
		index[m] = i
		if m.Binary == "" {
			if len(m.Command) > 0 {
				refuse(m, bindingBadCommand,
					"`command` names a subcommand path BELOW a binary, and no `binary` is set. Add the binary whose subcommand this is.")
			} else if len(m.Subtrees) > 0 {
				refuse(m, bindingBadCommand,
					"`subtrees` names command paths BELOW a binary, and no `binary` is set. Add the binary whose commands these are.")
			}
			continue
		}
		valid := true
		if detail := commandShapeProblem(m.Command); detail != "" {
			refuse(m, bindingBadCommand, detail)
			valid = false
		}
		if detail := subtreesProblem(m); detail != "" {
			refuse(m, bindingBadCommand, detail)
			valid = false
		}
		if binaries != nil && !known[m.Binary] {
			refuse(m, bindingUnknownBinary, unknownBinaryDetail(m.Binary, binaries))
			valid = false
		}
		if !valid {
			continue
		}
		if len(m.Subtrees) > 0 {
			// A subtree config is one of SEVERAL a process may load, so it
			// has no level to be a duplicate at; what it may not do is share
			// a leaf with a config loaded beside it (bindingCollisions).
			subtreeConfigs = append(subtreeConfigs, m)
			continue
		}
		key := processKey(m.Binary, m.Command)
		if _, seen := byProcess[key]; !seen {
			order = append(order, key)
		}
		byProcess[key] = append(byProcess[key], m)
	}

	for _, key := range order {
		group := byProcess[key]
		for _, m := range group[1:] {
			what := fmt.Sprintf("binary %q's root config", m.Binary)
			if len(m.Command) > 0 {
				what = fmt.Sprintf("%#q", m.ProcessName())
			}
			refuse(m, bindingDuplicate, fmt.Sprintf(
				"%s is already bound by %s. A process has ONE config at each level (one Load<Msg>, one KCL schema, one env projection), so a second message for it has no defined meaning. Merge the two messages, or compose the fields they share as a block on one of them.",
				what, group[0].Name))
		}
	}

	// The configs that bind a process, each once: the first of every
	// root/command group (a later one is already refused as a duplicate),
	// and every valid subtree config. In declaration order.
	var bound []*ConfigMessage
	for _, key := range order {
		bound = append(bound, byProcess[key][0])
	}
	bound = append(bound, subtreeConfigs...)
	sort.SliceStable(bound, func(i, j int) bool { return index[bound[i]] < index[bound[j]] })
	for _, c := range bindingCollisions(messages, bound) {
		refuse(c.cmd, bindingProcessCollision, c.detail())
	}

	if len(problems) == 0 {
		return nil
	}
	// In declaration order, so the runbook reads top to bottom like the
	// proto it points into.
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].order < problems[j].order })
	return fmt.Errorf("%s", formatBinaryConfigBindingProblems(problems, binaries))
}

// processKey identifies the process a binding names. It is NOT the
// space-joined ProcessName: `{binary: "x server"}` and `{binary: "x",
// command: ["server"]}` print alike but are different bindings.
func processKey(binary string, command []string) string {
	return binary + "\x00" + strings.Join(command, "\x00")
}

// commandShapeProblem explains why a command path is not one, or "" when
// every element is a single command name. Element by element, because the
// common mistake — `command: ["server api"]` — is a whole line in one slot.
func commandShapeProblem(command []string) string {
	for _, c := range command {
		switch {
		case strings.TrimSpace(c) == "":
			return "`command` has an empty element. Each element is one cobra command name: [\"server\", \"api\"]."
		case strings.ContainsAny(c, " \t\n"):
			return fmt.Sprintf("`command` element %q contains whitespace. Each element is ONE cobra command name: %s.",
				c, quotedList(strings.Fields(c)))
		case strings.HasPrefix(c, "-"):
			return fmt.Sprintf("`command` element %q is a flag. `command` is the subcommand PATH a process runs; flags are config fields.", c)
		}
	}
	return ""
}

// subtreesProblem explains why m's `subtrees` cannot mean what they say, or
// "" when they are well formed (or absent).
func subtreesProblem(m *ConfigMessage) string {
	if len(m.Subtrees) == 0 {
		return ""
	}
	if len(m.Command) > 0 {
		return "`command` and `subtrees` are both set. A config is ONE process's (`command`) or every process at or below some commands (`subtrees`), not both: move the leaves only that one command reads to a config of its own."
	}
	for i, p := range m.Subtrees {
		if len(p) == 0 {
			return "`subtrees` has an empty path. The empty path is every process of the binary — its root config: drop `subtrees` to declare that."
		}
		if detail := commandShapeProblem(p); detail != "" {
			return "in `subtrees`: " + detail
		}
		for j, q := range m.Subtrees {
			if i != j && isPathPrefix(q, p) && (len(q) < len(p) || j < i) {
				return fmt.Sprintf("subtree %s lies at or below subtree %s of the same config, which already covers it. Drop %s.",
					quotedList(p), quotedList(q), quotedList(p))
			}
		}
	}
	return ""
}

// isPathPrefix reports whether prefix is a leading part of path (or equal).
func isPathPrefix(prefix, path []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if prefix[i] != path[i] {
			return false
		}
	}
	return true
}

// sharedProcessPath returns a command path whose process loads both a and b
// — the deepest point their scopes meet — and whether there is one. Root
// configs meet everything; a command config meets a subtree config at or
// above it; two subtree configs meet when one path prefixes the other. Two
// command configs never meet (one of them would be a duplicate).
func sharedProcessPath(a, b *ConfigMessage) ([]string, bool) {
	isRoot := func(m *ConfigMessage) bool { return len(m.Command) == 0 && len(m.Subtrees) == 0 }
	switch {
	case a.Binary != b.Binary:
		return nil, false
	case isRoot(a) && isRoot(b):
		return nil, false // the same process twice: a duplicate, refused already
	case isRoot(a):
		return representativePath(b), true
	case isRoot(b):
		return representativePath(a), true
	case len(a.Command) > 0 && len(b.Command) > 0:
		return nil, false
	case len(a.Command) > 0 || len(b.Command) > 0:
		cmd, sub := a, b
		if len(b.Command) > 0 {
			cmd, sub = b, a
		}
		for _, p := range sub.Subtrees {
			if isPathPrefix(p, cmd.Command) {
				return cmd.Command, true
			}
		}
		return nil, false
	}
	for _, p := range a.Subtrees {
		for _, q := range b.Subtrees {
			switch {
			case isPathPrefix(p, q):
				return q, true
			case isPathPrefix(q, p):
				return p, true
			}
		}
	}
	return nil, false
}

// representativePath is one process a non-root config is loaded by: its
// command, or its first subtree.
func representativePath(m *ConfigMessage) []string {
	if len(m.Command) > 0 {
		return m.Command
	}
	if len(m.Subtrees) > 0 {
		return m.Subtrees[0]
	}
	return nil
}

// scopePhrase names the processes a config is loaded by, for a refusal.
func scopePhrase(m *ConfigMessage) string {
	switch {
	case len(m.Command) > 0:
		return fmt.Sprintf("the %#q command config", m.ProcessName())
	case len(m.Subtrees) > 0:
		paths := make([]string, len(m.Subtrees))
		for i, p := range m.Subtrees {
			paths[i] = fmt.Sprintf("%#q", strings.TrimSpace(m.Binary+" "+strings.Join(p, " ")))
		}
		return "the subtree config for " + strings.Join(paths, ", ")
	}
	return "the binary's root config"
}

// processCollision is one leaf two configs a single process loads both
// declare: a binary's root config and any other of its configs, or a subtree
// config and a command or subtree config its scope meets. root is the wider
// (or earlier-declared) of the two, cmd the one refused.
type processCollision struct {
	root, cmd *ConfigMessage
	process   []string // a command path whose process loads both
	what      string   // "env var LOG_LEVEL" / "flag --log-level" / "config file key port"
	rootPath  string   // the leaf on root, e.g. "log.level"
	cmdPath   string
	fileKey   bool // a top-level field name, not a leaf's env var or flag
}

func (c processCollision) processName() string {
	return strings.TrimSpace(c.cmd.Binary + " " + strings.Join(c.process, " "))
}

func (c processCollision) detail() string {
	if c.fileKey {
		return fmt.Sprintf(
			"%s is also a top-level field of %s, %s. The %#q process reads both configs from ONE config file (--config, or %s), whose keys are flat across the two, so the file could not say which %s it sets. Rename one of the two fields.",
			c.what, c.root.Name, scopePhrase(c.root), c.processName(), forgeconfig.ConfigPathEnvFor(c.cmd.Binary), c.rootPath)
	}
	fix := fmt.Sprintf("Declare it once: on %s if every %s process reads it, otherwise on the command configs alone.", c.root.Name, c.root.Binary)
	if len(c.root.Command) > 0 || len(c.root.Subtrees) > 0 {
		fix = "Declare it once, on the config whose scope covers every process that reads it."
	}
	return fmt.Sprintf(
		"%s (%s) is also declared by %s (%s), %s. The %#q process loads both, so it would read one value from two declarations that can drift, and its env projection would carry the key twice. %s",
		c.what, c.cmdPath, c.root.Name, c.rootPath, scopePhrase(c.root), c.processName(), fix)
}

// bindingCollisions finds the leaves two configs of one binary share when
// some process loads both (sharedProcessPath). Composed blocks count: they
// are leaves of the process exactly as declared ones are. bound holds each
// binding once, in declaration order; the root config, when it is one of a
// pair, is always the side cited and the other the side refused.
func bindingCollisions(messages []ConfigMessage, bound []*ConfigMessage) []processCollision {
	isRoot := func(m *ConfigMessage) bool { return len(m.Command) == 0 && len(m.Subtrees) == 0 }
	var out []processCollision
	for j, cmd := range bound {
		for i, root := range bound {
			if i == j {
				continue
			}
			// Each pair once: the root is always the cited side; otherwise
			// the earlier-declared one is.
			if isRoot(cmd) || (!isRoot(root) && i > j) {
				continue
			}
			process, meet := sharedProcessPath(root, cmd)
			if !meet {
				continue
			}
			out = append(out, pairCollisions(messages, root, cmd, process)...)
		}
	}
	return out
}

// pairCollisions lists the env vars, flags and top-level file keys root and
// cmd both declare.
func pairCollisions(messages []ConfigMessage, root, cmd *ConfigMessage, process []string) []processCollision {
	var out []processCollision
	rootEnv, rootFlag := map[string]string{}, map[string]string{}
	for _, f := range FlattenFieldsWithBlocks(root.Fields, messages) {
		if f.EnvVar != "" {
			rootEnv[f.EnvVar] = f.KCLPath()
		}
		if f.Flag != "" && !f.Sensitive {
			rootFlag[f.Flag] = f.KCLPath()
		}
	}
	for _, f := range FlattenFieldsWithBlocks(cmd.Fields, messages) {
		if p, dup := rootEnv[f.EnvVar]; dup && f.EnvVar != "" {
			out = append(out, processCollision{root: root, cmd: cmd, process: process,
				what: "env var " + f.EnvVar, rootPath: p, cmdPath: f.KCLPath()})
			continue
		}
		if p, dup := rootFlag[f.Flag]; dup && f.Flag != "" && !f.Sensitive {
			out = append(out, processCollision{root: root, cmd: cmd, process: process,
				what: "flag --" + f.Flag, rootPath: p, cmdPath: f.KCLPath()})
		}
	}

	// The config FILE is flat across the two messages at the TOP level
	// only (each message decodes its own keys below it), so a top-level
	// field name — a composed block's included — is the file's key.
	rootKeys := map[string]bool{}
	for _, f := range root.Fields {
		rootKeys[f.Name] = true
	}
	for _, f := range cmd.Fields {
		if rootKeys[f.Name] {
			out = append(out, processCollision{root: root, cmd: cmd, process: process, fileKey: true,
				what: "config file key " + strconv.Quote(f.Name), rootPath: f.Name, cmdPath: f.Name})
		}
	}
	return out
}

// bindingLiteral renders a message's binding as the annotation spells it.
func bindingLiteral(m ConfigMessage) string {
	parts := []string{}
	if m.Binary != "" {
		parts = append(parts, "binary: "+strconv.Quote(m.Binary))
	}
	if len(m.Command) > 0 {
		parts = append(parts, "command: "+quotedList(m.Command))
	}
	if len(m.Subtrees) > 0 {
		paths := make([]string, len(m.Subtrees))
		for i, p := range m.Subtrees {
			paths[i] = "{path: " + quotedList(p) + "}"
		}
		parts = append(parts, "subtrees: ["+strings.Join(paths, ", ")+"]")
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// quotedList renders a string list as a proto/KCL list literal.
func quotedList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// configMessageWhere renders a message's source position as the
// project-relative "proto/<file>:<line>" an editor can jump to. The
// descriptor records files relative to the buf module root (proto/), and
// config protos are always read from proto/<project>/config/v1. It degrades rather than
// lying: no file renders "", no line renders the file alone.
func configMessageWhere(m ConfigMessage) string {
	if m.ProtoFile == "" {
		return ""
	}
	file := filepath.ToSlash(m.ProtoFile)
	if !strings.HasPrefix(file, "proto/") {
		file = "proto/" + file
	}
	if m.Line <= 0 {
		return file
	}
	return fmt.Sprintf("%s:%d", file, m.Line)
}

// unknownBinaryDetail explains why binary names no binary, and what was
// probably meant.
func unknownBinaryDetail(binary string, binaries []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "there is no cmd/%s/ directory.", binary)
	if words := strings.Fields(binary); len(words) > 1 || strings.Contains(binary, "/") {
		b.WriteString(" `binary` is the cmd/<name> directory leaf, not a command line or a path.")
		// `x server api`: the leading word is a real binary, and the rest
		// is the subcommand path `command` exists to carry.
		if len(words) > 1 && containsString(binaries, words[0]) {
			fmt.Fprintf(&b, " To scope this config to that subcommand, write {binary: %q, command: %s}.",
				words[0], quotedList(words[1:]))
		}
	} else if near := nearestBinary(binary, binaries); near != "" {
		fmt.Fprintf(&b, " Did you mean %q?", near)
	}
	return b.String()
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// nearestBinary returns the binary whose name matches binary once case and
// the -/_ spelling are ignored — the difference between a binary's name and
// its cmd/ package directory (`forge scaffold binary workspace-proxy` writes
// cmd/workspace_proxy). "" when nothing matches that way.
func nearestBinary(binary string, binaries []string) string {
	norm := func(s string) string {
		return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(s))
	}
	want := norm(binary)
	for _, b := range binaries {
		if norm(b) == want {
			return b
		}
	}
	return ""
}

// formatBinaryConfigBindingProblems renders the refusal as a runbook. Kept
// separate so the wording is testable without a project on disk.
func formatBinaryConfigBindingProblems(problems []BinaryConfigBindingProblem, binaries []string) string {
	var b strings.Builder
	if len(problems) == 1 {
		b.WriteString("a (forge.v1.binary_config) binding was refused\n\n")
	} else {
		fmt.Fprintf(&b, "%d (forge.v1.binary_config) bindings were refused\n\n", len(problems))
	}
	kinds := map[bindingProblemKind]bool{}
	for _, p := range problems {
		kinds[p.kind] = true
		b.WriteString("  ")
		if p.Where != "" {
			b.WriteString(p.Where + ": ")
		}
		fmt.Fprintf(&b, "message %s: (forge.v1.binary_config) = %s\n", p.Message, p.Binding)
		fmt.Fprintf(&b, "    %s\n", p.Detail)
	}

	if kinds[bindingUnknownBinary] {
		b.WriteString("\n")
		switch {
		case binaries == nil:
		case len(binaries) == 0:
			b.WriteString("This project has no cmd/ binaries.\n")
		default:
			fmt.Fprintf(&b, "Binaries in this project (cmd/<name>): %s\n", strings.Join(binaries, ", "))
		}
		b.WriteString("\nA config bound to a binary nothing builds still generates a loader, a KCL\n")
		b.WriteString("schema and an env projection, and no process ever reads any of them.\n")
		b.WriteString("Fix: point `binary` at a directory under cmd/, or create the binary first\n")
		b.WriteString("(`forge scaffold binary <name>`). A subcommand of a binary is not a\n")
		b.WriteString("binary: scope a config to one with `command: [\"server\", \"api\"]`.")
	}
	return strings.TrimRight(b.String(), "\n")
}
