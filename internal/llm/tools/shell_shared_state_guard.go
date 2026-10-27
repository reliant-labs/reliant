// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/shellparse"
)

// On 2026-10-26 something wiped this machine's whole shared Go build cache
// twice (about 45–50 GB each, in under a minute), breaking every build running
// on it. The likely cause was an agent running `go clean -cache` once the disk
// guard refused heavy commands: `go clean` is a light command, so the guard let
// it through, and nothing on the machine recorded who ran it.
//
// The caches under /var/cache/workspace, $GOCACHE and $GOMODCACHE, and the
// docker engine's images, volumes and build cache, are shared by every chat on
// the machine. Deleting them is never one agent's decision; the machine's
// steward trims them beside running builds. So this refuses, before dispatch,
// the commands that delete them wholesale:
//
//   - go clean -cache | -modcache | -testcache | -fuzzcache
//   - rm / unlink / rmdir, find -delete / -exec rm, and rsync --delete aimed at
//     a shared cache
//   - docker system prune, image prune -a, volume prune, builder prune --all
//
// The command is PARSED (internal/shellparse), not searched for substrings:
// quoted text and heredoc bodies are data, `bash -c` and `eval` scripts are
// read, and paths are resolved through the variables the command can see. A
// `go clean -i ./mypkg` or an `rm -rf ./build` is untouched.
//
// There is no override. The existing guards in this package have none, and an
// override an agent can type is the thing that failed here.

// sharedStateGuardName is the audit-log name of this guard.
const sharedStateGuardName = "shared_state"

// sharedStateRefusal returns the refusal for command, or "" when it touches no
// shared state. env is the command's own environment from the tool call; it
// overrides the daemon's, so `GOCACHE=$(mktemp -d) go clean -cache` is private.
func sharedStateRefusal(command string, env map[string]string) string {
	return sharedStateRefusalWith(command, func() sharedState {
		return withEnvOverrides(defaultState(), env)
	})
}

// sharedStateRefusalWith is sharedStateRefusal with the machine's state
// supplied, loaded only if the command is one that could touch it.
func sharedStateRefusalWith(command string, load func() sharedState) string {
	a := &sharedStateAnalyzer{load: load, vars: map[string]string{}}
	if f := a.script(command, 0); f != nil {
		return f.message()
	}
	return ""
}

type sharedStateFinding struct {
	command string // the offending simple command
	action  string // what it would do
	label   string // what it would do it to
	kind    string // "go", "path", "docker"
}

type sharedStateAnalyzer struct {
	load func() sharedState
	st   *sharedState
	// vars are the variables the script has set so far; they shadow the
	// machine's.
	vars map[string]string
	// local holds the leading NAME=value assignments of the command being
	// examined; they apply to that command alone.
	local map[string]string
	// cwd is the directory the script has cd'd to, or "" while it is still in
	// the working directory the tool picks (a project checkout).
	cwd string
}

// child is an analyzer for a script run by this one (`bash -c`, `eval`): it
// starts with the variables and directory the parent has at that point.
func (a *sharedStateAnalyzer) child() *sharedStateAnalyzer {
	c := &sharedStateAnalyzer{load: a.load, st: a.st, vars: map[string]string{}, cwd: a.cwd}
	for k, v := range a.vars {
		c.vars[k] = v
	}
	for k, v := range a.local {
		c.vars[k] = v
	}
	return c
}

const maxScriptDepth = 6

func (a *sharedStateAnalyzer) state() *sharedState {
	if a.st == nil {
		s := a.load()
		a.st = &s
	}
	return a.st
}

func (a *sharedStateAnalyzer) script(src string, depth int) *sharedStateFinding {
	if depth > maxScriptDepth {
		return nil
	}
	cmds := shellparse.Commands(shellparse.StripHeredocs(src))
	pipeShared := false
	for i, c := range cmds {
		if !c.PipeIn {
			pipeShared = false
		}
		// A substitution runs whatever it holds, wherever the word sits.
		for _, w := range c.Words {
			for _, inner := range shellparse.Substitutions(w) {
				if f := a.script(inner, depth+1); f != nil {
					return f
				}
			}
		}
		rest, viaXargs := a.peel(c.Words)
		if f := a.command(rest, viaXargs && pipeShared, depth); f != nil {
			return f
		}
		// What a pipeline's producer names decides what a downstream `xargs rm`
		// deletes; look only when something downstream reads this output.
		if i+1 < len(cmds) && cmds[i+1].PipeIn && len(rest) > 1 && a.touchesShared(rest[1:]) {
			pipeShared = true
		}
	}
	return nil
}

var shellKeywordWords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "do": true, "done": true,
	"while": true, "until": true, "!": true, "{": true, "}": true,
}

// wrapperValueFlags are, per wrapper, the flags that consume the next word.
var wrapperValueFlags = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-h": true, "-p": true, "-C": true, "-D": true, "-R": true, "-T": true, "-U": true},
	"doas":    {"-u": true, "-C": true},
	"env":     {"-u": true, "-C": true, "-S": true},
	"timeout": {"-s": true, "-k": true},
	"nice":    {"-n": true},
	"ionice":  {"-c": true, "-n": true, "-p": true},
	"xargs":   {"-I": true, "-n": true, "-P": true, "-L": true, "-d": true, "-E": true, "-s": true, "-a": true, "-J": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"time":    {"-f": true, "-o": true},
}

// peel strips what only says how the real command runs: shell keywords,
// leading assignments, and wrappers (sudo, env, time, timeout, nice, xargs …)
// with their flags. It returns the real command's words. Assignments that
// precede a command apply to that command alone and are left in a.local.
func (a *sharedStateAnalyzer) peel(words []string) (rest []string, viaXargs bool) {
	a.local = nil
	for len(words) > 0 {
		w := words[0]
		base := path.Base(w)
		switch {
		case shellKeywordWords[w]:
			words = words[1:]
		case isAssignmentWord(w):
			a.setLocal(w)
			words = words[1:]
		case wrapperValueFlags[base] != nil || base == "nohup" || base == "command" || base == "exec" || base == "builtin" || base == "setsid":
			words = words[1:]
			flags := wrapperValueFlags[base]
			for len(words) > 0 {
				f := words[0]
				if base == "env" && isAssignmentWord(f) {
					a.setLocal(f)
					words = words[1:]
					continue
				}
				if !strings.HasPrefix(f, "-") || f == "-" || f == "--" {
					if f == "--" {
						words = words[1:]
					}
					break
				}
				if flags[f] && len(words) > 1 {
					words = words[2:]
				} else {
					words = words[1:]
				}
			}
			switch base {
			case "timeout":
				if len(words) > 0 {
					words = words[1:] // the duration
				}
			case "xargs":
				viaXargs = true
			}
		default:
			return words, viaXargs
		}
	}
	return nil, viaXargs
}

func isAssignmentWord(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// setLocal records NAME=value for the command being peeled, expanded now (the
// shell expands the value before the command sees it). A value that cannot be
// resolved — `$(mktemp -d)` — is kept as a non-path marker: whatever it names,
// it is not the shared directory.
func (a *sharedStateAnalyzer) setLocal(assign string) {
	name, val, _ := strings.Cut(assign, "=")
	if a.local == nil {
		a.local = map[string]string{}
	}
	if v, ok := a.expand(val); ok {
		a.local[name] = v
	} else {
		a.local[name] = unresolvedValue
	}
}

const unresolvedValue = "<unresolved>"

// lookup resolves a variable the way the command would see it: its own
// leading assignments, then the script's, then the machine's.
func (a *sharedStateAnalyzer) lookup(name string) (string, bool) {
	if v, ok := a.local[name]; ok {
		return v, true
	}
	if v, ok := a.vars[name]; ok {
		return v, true
	}
	v, ok := a.state().vars[name]
	return v, ok && v != ""
}

// expand resolves $NAME, ${NAME}, ~ and `go env NAME` in a word. ok is false
// when something in it cannot be known without running a command.
func (a *sharedStateAnalyzer) expand(word string) (string, bool) {
	if !strings.ContainsAny(word, "$`~") {
		return shellparse.Restore(word), true
	}
	var b strings.Builder
	if word == "~" || strings.HasPrefix(word, "~/") {
		home := a.state().home
		if home == "" {
			return "", false
		}
		b.WriteString(home)
		word = word[1:]
	}
	for i := 0; i < len(word); i++ {
		c := word[i]
		switch {
		case c == '$' && i+1 < len(word) && word[i+1] == '(' || c == '`':
			inner, end, ok := shellparse.SubstitutionAt(word, i)
			if !ok {
				return "", false
			}
			v, ok := a.goEnvValue(inner)
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i = end - 1
		case c == '$' && i+1 < len(word) && word[i+1] == '{':
			j := strings.IndexByte(word[i:], '}')
			if j < 0 {
				return "", false
			}
			v, ok := a.lookup(word[i+2 : i+j])
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i += j
		case c == '$' && i+1 < len(word) && isNameStart(word[i+1]):
			j := i + 1
			for j < len(word) && isNameChar(word[j]) {
				j++
			}
			v, ok := a.lookup(word[i+1 : j])
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i = j - 1
		case c == '$':
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	return shellparse.Restore(b.String()), true
}

func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameChar(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

// goEnvValue answers `go env NAME` — the one command substitution whose result
// is knowable.
func (a *sharedStateAnalyzer) goEnvValue(script string) (string, bool) {
	cmds := shellparse.Commands(script)
	if len(cmds) != 1 {
		return "", false
	}
	w := cmds[0].Words
	if len(w) != 3 || path.Base(w[0]) != "go" || w[1] != "env" {
		return "", false
	}
	return a.lookup(w[2])
}

// resolve turns a path word into an absolute cleaned path (glob characters
// kept), or "" when it cannot be known or is not absolute.
func (a *sharedStateAnalyzer) resolve(word string) string {
	p, ok := a.expand(word)
	if !ok || p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		if !strings.HasPrefix(a.cwd, "/") {
			return ""
		}
		p = path.Join(a.cwd, p)
	}
	return path.Clean(p)
}

func (a *sharedStateAnalyzer) hit(word string, ancestors bool) (string, bool) {
	p := a.resolve(word)
	if p == "" {
		return "", false
	}
	return a.state().hit(p, ancestors)
}

// touchesShared reports whether any non-flag word names shared state.
func (a *sharedStateAnalyzer) touchesShared(words []string) bool {
	for _, w := range words {
		if strings.HasPrefix(w, "-") {
			continue
		}
		if _, hit := a.hit(w, true); hit {
			return true
		}
	}
	return false
}

var removers = map[string]bool{"rm": true, "unlink": true, "rmdir": true, "shred": true}

func (a *sharedStateAnalyzer) command(words []string, xargsFedFromShared bool, depth int) *sharedStateFinding {
	if len(words) == 0 {
		// Only assignments: they persist for the rest of the script.
		for k, v := range a.local {
			a.vars[k] = v
		}
		return nil
	}
	name := path.Base(words[0])
	args := words[1:]
	text := func() string { return shellparse.Restore(strings.Join(words, " ")) }

	switch name {
	case "cd":
		a.cd(args)
	case "export", "declare", "typeset", "local", "readonly":
		for _, w := range args {
			if isAssignmentWord(w) {
				k, v, _ := strings.Cut(w, "=")
				if ev, ok := a.expand(v); ok {
					a.vars[k] = ev
				} else {
					a.vars[k] = unresolvedValue
				}
			}
		}
	case "go":
		return a.goClean(args, text)
	case "rm", "unlink", "rmdir", "shred":
		if xargsFedFromShared {
			return &sharedStateFinding{command: text(), action: "delete files that `xargs` reads from a shared cache", label: "shared cache", kind: "path"}
		}
		// Without -r, rm cannot remove a directory, so naming a directory that
		// merely contains a shared cache (`rm -f /`) removes nothing of it.
		recursive := rmRecursive(words)
		for _, w := range operands(args) {
			if label, hit := a.hit(w, recursive); hit {
				return &sharedStateFinding{command: text(), action: "delete under the " + label, label: label, kind: "path"}
			}
		}
	case "find":
		return a.find(args, text)
	case "rsync":
		if rsyncDeletes(append([]string{name}, args...)) {
			if ops := operands(args); len(ops) >= 2 {
				if label, hit := a.hit(ops[len(ops)-1], true); hit {
					return &sharedStateFinding{command: text(), action: "sync with --delete into the " + label, label: label, kind: "path"}
				}
			}
		}
	case "docker", "podman":
		return dockerPrune(name, args, text)
	case "bash", "sh", "zsh", "dash", "ksh":
		if script, ok := shellDashC(args); ok {
			return a.child().script(shellparse.Restore(script), depth+1)
		}
	case "eval":
		return a.child().script(shellparse.Restore(strings.Join(args, " ")), depth+1)
	}
	return nil
}

func (a *sharedStateAnalyzer) cd(args []string) {
	target := ""
	for _, w := range args {
		if !strings.HasPrefix(w, "-") || w == "-" {
			target = w
			break
		}
	}
	if target == "" {
		a.cwd = a.state().home
		return
	}
	a.cwd = a.resolve(target)
}

// operands are a command's non-flag arguments; after `--` everything is one.
func operands(args []string) []string {
	var out []string
	flags := true
	for _, w := range args {
		switch {
		case flags && w == "--":
			flags = false
		case w == "":
		case flags && strings.HasPrefix(w, "-") && w != "-":
		default:
			out = append(out, w)
		}
	}
	return out
}

func (a *sharedStateAnalyzer) goClean(args []string, text func() string) *sharedStateFinding {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-C" {
			i++
		}
		i++
	}
	if i >= len(args) || args[i] != "clean" {
		return nil
	}
	var cache, mod, dry bool
	for _, w := range args[i+1:] {
		if w == "--" {
			break
		}
		if !strings.HasPrefix(w, "-") {
			continue
		}
		flag, val, hasVal := strings.Cut(strings.TrimLeft(w, "-"), "=")
		on := true
		if hasVal {
			if v, err := strconv.ParseBool(val); err == nil {
				on = v
			}
		}
		switch flag {
		case "n":
			dry = on
		case "cache", "testcache", "fuzzcache":
			cache = cache || on
		case "modcache":
			mod = mod || on
		}
	}
	if dry {
		return nil // prints what it would remove and removes nothing
	}
	check := func(env, label string) *sharedStateFinding {
		if dir, ok := a.lookup(env); ok {
			if !strings.HasPrefix(dir, "/") {
				return nil // a directory only known at run time (`$(mktemp -d)`): not the shared one
			}
			got, hit := a.state().hit(path.Clean(dir), false)
			if !hit {
				return nil
			}
			label = got
		}
		return &sharedStateFinding{command: text(), action: "wipe the " + label + " that every agent on this machine builds with", label: label, kind: "go"}
	}
	if cache {
		if f := check("GOCACHE", "Go build cache"); f != nil {
			return f
		}
	}
	if mod {
		if f := check("GOMODCACHE", "Go module cache"); f != nil {
			return f
		}
	}
	return nil
}

func (a *sharedStateAnalyzer) find(args []string, text func() string) *sharedStateFinding {
	var roots []string
	i := 0
	for ; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") || args[i] == "(" || args[i] == "!" {
			break
		}
		roots = append(roots, args[i])
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	deletes := false
	for j := i; j < len(args); j++ {
		switch args[j] {
		case "-delete":
			deletes = true
		case "-exec", "-execdir", "-ok", "-okdir":
			if j+1 < len(args) && removers[path.Base(args[j+1])] {
				deletes = true
			}
		}
	}
	if !deletes {
		return nil
	}
	for _, r := range roots {
		if label, hit := a.hit(r, true); hit {
			return &sharedStateFinding{command: text(), action: "delete files under the " + label + " with find", label: label, kind: "path"}
		}
	}
	return nil
}

// shellDashC returns the script a `bash -c` style invocation runs.
func shellDashC(args []string) (string, bool) {
	for i, w := range args {
		if !strings.HasPrefix(w, "-") || strings.HasPrefix(w, "--") {
			return "", false
		}
		if strings.Contains(w[1:], "c") {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

var dockerValueFlags = map[string]bool{
	"--context": true, "-c": true, "-H": true, "--host": true, "--config": true,
	"-l": true, "--log-level": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true,
}

// dockerPrune finds the docker/podman prunes that remove other agents' images,
// volumes or build cache. `container prune` and a plain `image prune` /
// `builder prune` only touch stopped containers and dangling layers, which
// nothing builds on, and are left alone.
func dockerPrune(engine string, args []string, text func() string) *sharedStateFinding {
	var sub []string
	i := 0
	for ; i < len(args); i++ {
		w := args[i]
		if strings.HasPrefix(w, "-") {
			if dockerValueFlags[w] {
				i++
			}
			continue
		}
		sub = append(sub, w)
		if len(sub) == 2 {
			i++
			break
		}
	}
	if len(sub) < 2 || sub[1] != "prune" {
		return nil
	}
	all := false
	for _, w := range args[i:] {
		if w == "--all" || strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "a") {
			all = true
		}
	}
	f := &sharedStateFinding{command: text(), kind: "docker"}
	switch {
	case sub[0] == "system":
		f.action, f.label = "prune the container engine, which removes every agent's stopped containers, networks, dangling images and build cache", engine+" engine"
	case sub[0] == "volume":
		f.action, f.label = "delete every volume no running container uses, including other agents' dev databases", engine+" volumes"
	case (sub[0] == "image") && all:
		f.action, f.label = "delete every image no running container uses", engine+" images"
	case (sub[0] == "builder" || sub[0] == "buildx") && all:
		f.action, f.label = "delete the whole image build cache", engine+" build cache"
	default:
		return nil
	}
	return f
}

func (f *sharedStateFinding) message() string {
	var alt string
	switch f.kind {
	case "go":
		alt = "  - Need a clean build or fresh test results for ONE run? Do not delete the shared cache. `go test -count=1 ./pkg/...` skips cached test results, `go build -a` rebuilds, and `GOCACHE=$(mktemp -d) go test ./pkg/...` uses a private cache that you remove afterwards (only that directory)."
	case "docker":
		alt = "  - Remove only what you created, by name: `docker rm -fv <name>`, `docker rmi <image>`, `docker volume rm <volume>`. Never prune."
	default:
		alt = "  - Need to remove something of yours? Delete it by its own path (a build directory in your checkout, a file you created). Do not glob or recurse into a shared cache directory."
	}
	return fmt.Sprintf(`refused: %q would %s.

That state is shared by every agent and build on this machine. On 2026-10-26 the entire shared Go build cache was wiped twice (about 45–50 GB each time) by a command like this; every running build broke, and nothing recorded who ran it. Deleting a shared cache or pruning the container engine is refused here whether or not the disk is full.

Safe ways to get what you need:
  - Disk full? Load the `+"`machine-disk`"+` skill: it shows what is using the disk and what is safe to free. The machine's steward and the daemon trim the build caches beside running builds, and keep what live builds use; you do not need to.
  - To free space on purpose, use forge's storage tool, which removes only what is eligible: `+"`reliant forge storage gc`"+` previews, `+"`reliant forge storage gc --apply`"+` runs it (`+"`reliant forge storage gocache --apply`"+` for the Go build cache alone).
%s
  - Archive the worktrees you are done with (the worktree tool's delete) and remove only files you created yourself.

There is no override for this refusal. Heavy commands are refused above the disk guard's threshold, but light ones such as go clean are not a way around it.`, f.command, f.action, alt)
}
