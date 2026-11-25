package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/templates"
)

// The Taskfile step of `forge fix trimpath-default`
// (upgrade_trimpath_default.go).
//
// A scaffolded Taskfile's top-level `env:` sets GOFLAGS to the machine's own
// GOFLAGS plus -trimpath (templates.TaskfileTrimpathGOFLAGS), so every
// worktree of a module shares Go build-cache entries instead of compiling and
// testing everything again under its own absolute path. Projects scaffolded
// before forge wrote that block have none, and the Taskfile is the project's
// own (scaffold-once), so moving the forge pin never adds it. This codemod is
// what adds it.
//
// It inserts forge's entry, byte for byte as a new project gets it, and
// changes nothing else in the file:
//
//   - a top-level block `env:` map gets GOFLAGS as its first key, at the
//     map's own indentation;
//   - a bare `env:` (null) gets it as its only key;
//   - no `env:` at all gets a new block before the first of `vars:` and
//     `tasks:` (above that key's comment), else after `version:`.
//
// It leaves the file alone when GOFLAGS is already set in that env, whatever
// its value: the project chose one. When that value never mentions -trimpath
// (and neither does the file) the report says how to merge it by hand. A
// shape it cannot edit by lines — a flow-style `env: {...}`, an alias, a file
// that does not parse — is left alone with the manual step. Every edit is
// proven before it is written: the result must parse, carry the entry as a
// top-level env key, and decode to exactly the old document plus that key.

// taskfileNames are the files Task reads, in the order it looks for them.
var taskfileNames = []string{
	"Taskfile.yml", "taskfile.yml", "Taskfile.yaml", "taskfile.yaml",
	"Taskfile.dist.yml", "taskfile.dist.yml", "Taskfile.dist.yaml", "taskfile.dist.yaml",
}

// trimpathTaskfileStep is the Taskfile step: the edit (written when write
// is set) and what is left by hand.
func trimpathTaskfileStep(dir string, write bool) (codemod.Report, error) {
	var rep codemod.Report
	rel, src, err := readTaskfile(dir)
	if err != nil || rel == "" {
		return rep, err
	}
	out, rewrite, next := planTaskfileTrimpath(dir, rel, src)
	rep.Next = next
	if out == nil {
		return rep, nil
	}
	if write {
		if err := writeKeepingMode(filepath.Join(dir, rel), out); err != nil {
			return rep, err
		}
	}
	rep.Rewrites = append(rep.Rewrites, rewrite)
	rep.Verify = []string{"task --list-all", "task test:short"}
	return rep, nil
}

// readTaskfile returns the Taskfile Task would read in dir, "" when there is
// none.
func readTaskfile(dir string) (string, []byte, error) {
	for _, name := range taskfileNames {
		src, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // the project's own Taskfile
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return name, src, nil
	}
	return "", nil, nil
}

// planTaskfileTrimpath decides the edit for one Taskfile: the new content
// (nil for none), the report line for it, and the manual steps left.
func planTaskfileTrimpath(dir, rel string, src []byte) (out []byte, rewrite string, next []string) {
	manual := func(why string) ([]byte, string, []string) {
		return nil, "", []string{fmt.Sprintf("%s %s, so it was left alone. Add GOFLAGS to its top-level `env:` by hand, as forge scaffolds it "+
			"(the entry is in `%s skill load %s`): GOFLAGS: {sh: %s}", rel, why, Name(), trimpathPlaybookSkill, templates.TaskfileTrimpathGOFLAGSScript)}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return manual(fmt.Sprintf("does not parse as YAML (%v)", err))
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return manual("is not a YAML map")
	}
	root := doc.Content[0]
	if root.Style&yaml.FlowStyle != 0 {
		return manual("is written in flow style")
	}
	var before map[string]any
	if err := doc.Decode(&before); err != nil {
		return manual(fmt.Sprintf("does not decode (%v)", err))
	}
	if env, ok := before["env"].(map[string]any); ok {
		if v, set := env["GOFLAGS"]; set {
			switch taskfileGOFLAGSTrimpath(dir, v) {
			case goflagsSaysTrimpath:
				return nil, "", nil
			case goflagsFromCommand:
				sh, _ := v.(map[string]any)["sh"].(string)
				return nil, "", []string{fmt.Sprintf("%s sets GOFLAGS in its top-level `env:` from a command (`sh: %s`) whose output forge cannot read without running it, so forge left it alone. "+
					"Make sure the flags it yields include -trimpath (`%s skill load %s` has forge's entry, which keeps the machine's own GOFLAGS), "+
					"or -trimpath=false to keep paths on purpose; naming -trimpath in the `sh:` line itself is what marks this step done", rel, strings.TrimSpace(sh), Name(), trimpathPlaybookSkill)}
			}
			return nil, "", []string{fmt.Sprintf("%s already sets GOFLAGS in its top-level `env:`, without -trimpath, so forge left your value alone. "+
				"Merge -trimpath into it by hand (append ` -trimpath` to the flags it yields; `%s skill load %s` has forge's entry, which keeps the machine's own GOFLAGS), "+
				"or say -trimpath=false there to keep paths on purpose", rel, Name(), trimpathPlaybookSkill)}
		}
	}

	nl := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		nl = "\r\n"
	}
	lines := strings.SplitAfter(string(src), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	key := map[string]*yaml.Node{}
	val := map[string]*yaml.Node{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := root.Content[i]; k.Kind == yaml.ScalarNode {
			if _, dup := key[k.Value]; !dup {
				key[k.Value], val[k.Value] = k, root.Content[i+1]
			}
		}
	}

	var at int // the 0-based line the insertion goes before
	var insert, where string
	switch envKey, env := key["env"], val["env"]; {
	case envKey != nil && env.Kind == yaml.MappingNode:
		if env.Style&yaml.FlowStyle != 0 || len(env.Content) == 0 {
			return manual("writes `env:` in flow style")
		}
		first := env.Content[0]
		if first.Line <= envKey.Line || first.Column < 2 {
			return manual("starts its `env:` map on the `env:` line")
		}
		at, insert, where = envKey.Line, taskfileTrimpathEntry(first.Column-1, nl)+nl, "first key of the top-level `env:`"
	case envKey != nil && env.Kind == yaml.ScalarNode && env.Tag == "!!null" && env.Value == "":
		at, insert, where = envKey.Line, taskfileTrimpathEntry(2, nl)+nl, "the top-level `env:`, which was empty"
	case envKey != nil:
		return manual("has a top-level `env:` that is not a map forge can add a key to")
	default:
		block := "env:" + nl + taskfileTrimpathEntry(2, nl) + nl
		anchor := earliest(key["vars"], key["tasks"])
		switch {
		case anchor != nil:
			at = anchor.Line - 1
			for at > 0 && strings.HasPrefix(lines[at-1], "#") {
				at-- // keep the key's own comment above it
			}
			insert = block + nl
			if at > 0 && strings.TrimSpace(lines[at-1]) != "" {
				insert = nl + insert
			}
			where = "a new top-level `env:` before `" + anchor.Value + ":`"
		case key["version"] != nil && val["version"].Line == key["version"].Line:
			at = key["version"].Line
			insert = nl + block
			if at < len(lines) && strings.TrimSpace(lines[at]) != "" {
				insert += nl
			}
			where = "a new top-level `env:` after `version:`"
		default:
			return manual("has no `version:`, `vars:` or `tasks:` to place a new `env:` beside")
		}
	}
	if at > len(lines) {
		return manual("ends before the line forge would insert at")
	}
	if at > 0 && !strings.HasSuffix(lines[at-1], "\n") {
		lines[at-1] += nl
	}
	edited := strings.Join(lines[:at], "") + insert + strings.Join(lines[at:], "")
	if why := verifyTaskfileTrimpath(before, []byte(edited)); why != "" {
		return manual("could not be edited without changing its meaning (" + why + ")")
	}
	return []byte(edited), fmt.Sprintf("%s: GOFLAGS with -trimpath, as %s (worktrees of this module now share Go build-cache entries)", rel, where), nil
}

// goflagsTrimpath is what a Taskfile env's own GOFLAGS value says about
// -trimpath, as far as forge can tell without running anything.
type goflagsTrimpath int

const (
	goflagsLacksTrimpath goflagsTrimpath = iota // a value forge can read, without the flag
	goflagsSaysTrimpath                         // the value decides -trimpath, on or off
	goflagsFromCommand                          // a `sh:` whose output forge cannot read
)

// taskfileGOFLAGSTrimpath judges a Taskfile env's own GOFLAGS value, without
// running anything:
//
//   - a plain value decides -trimpath when it carries the flag;
//   - a `sh:` decides it when its code names the flag: the command itself
//     (forge's own entry does), or the code of the project script it runs
//     (control-plane's scripts/lib/goflags.sh), comments stripped, so a
//     script that mentions -trimpath only in a comment proves nothing.
//
// Any other `sh:` yields flags forge cannot know without running it: a
// manual step, never a pass. Text elsewhere in the Taskfile is no evidence
// either (a comment, a task's own command).
func taskfileGOFLAGSTrimpath(dir string, v any) goflagsTrimpath {
	switch v := v.(type) {
	case string:
		for _, f := range strings.Fields(v) {
			f = "-" + strings.TrimLeft(f, "-") // -trimpath and --trimpath alike
			if f == "-trimpath" || strings.HasPrefix(f, "-trimpath=") {
				return goflagsSaysTrimpath
			}
		}
	case map[string]any:
		sh, _ := v["sh"].(string)
		if strings.TrimSpace(sh) == "" {
			return goflagsLacksTrimpath
		}
		if strings.Contains(shellCode(sh), "-trimpath") {
			return goflagsSaysTrimpath
		}
		fields := strings.Fields(sh)
		script := filepath.Clean(filepath.FromSlash(fields[0]))
		if !filepath.IsAbs(script) && script != ".." && !strings.HasPrefix(script, ".."+string(filepath.Separator)) {
			b, err := os.ReadFile(filepath.Join(dir, script)) //nolint:gosec // a script of the project's, named by its Taskfile
			if err == nil && strings.Contains(shellCode(string(b)), "-trimpath") {
				return goflagsSaysTrimpath
			}
		}
		return goflagsFromCommand
	}
	return goflagsLacksTrimpath
}

// shellCode is src with its shell comments removed: whole-line comments, and
// a `#` that starts a word (after a blank) through the end of its line. A `#`
// inside a word, such as ${var#pattern}, is code and stays.
func shellCode(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			continue
		}
		for i := 1; i < len(line); i++ {
			if line[i] == '#' && (line[i-1] == ' ' || line[i-1] == '\t') {
				line = line[:i]
				break
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// taskfileTrimpathEntry is forge's GOFLAGS entry re-indented for a map
// indented by indent spaces: the entry at indent, its `sh:` at twice that,
// the nesting step the map itself uses.
func taskfileTrimpathEntry(indent int, nl string) string {
	lines := strings.Split(templates.TaskfileTrimpathGOFLAGS, "\n")
	for i, l := range lines {
		body := strings.TrimLeft(l, " ")
		lines[i] = strings.Repeat(" ", (len(l)-len(body))/2*indent) + body
	}
	return strings.Join(lines, nl)
}

// earliest is whichever present key comes first in the file.
func earliest(keys ...*yaml.Node) *yaml.Node {
	var first *yaml.Node
	for _, k := range keys {
		if k != nil && (first == nil || k.Line < first.Line) {
			first = k
		}
	}
	return first
}

// verifyTaskfileTrimpath proves an edit: edited must parse, set the
// top-level env's GOFLAGS to forge's entry, and otherwise decode to exactly
// before. It returns why not, "" when it does.
func verifyTaskfileTrimpath(before map[string]any, edited []byte) string {
	var after map[string]any
	if err := yaml.Unmarshal(edited, &after); err != nil {
		return fmt.Sprintf("the result does not parse: %v", err)
	}
	var want map[string]any
	if err := yaml.Unmarshal([]byte("sh: "+templates.TaskfileTrimpathGOFLAGSScript), &want); err != nil {
		return fmt.Sprintf("forge's entry does not parse: %v", err)
	}
	env, ok := after["env"].(map[string]any)
	if !ok || !reflect.DeepEqual(env["GOFLAGS"], want) {
		return "the result has no top-level env GOFLAGS entry"
	}
	delete(env, "GOFLAGS")
	if len(env) == 0 {
		if old, had := before["env"]; !had {
			delete(after, "env")
		} else if old == nil {
			after["env"] = nil
		}
	}
	if !reflect.DeepEqual(before, after) {
		return "the rest of the file decodes differently afterwards"
	}
	return ""
}
