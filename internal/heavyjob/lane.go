// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/shellparse"
)

// laneEnv is the variable forge/pkg/hostslot reads from Options.Env to put a
// waiter in a lane, and laneExpress the lane scoped commands ask for. It is the
// whole protocol between the two: a forge without lanes ignores the variable,
// so a scoped command then simply queues as it always did.
const (
	laneEnv     = "FORGE_SLOT_LANE"
	laneExpress = "express"
)

// maxScopedTargets is the most targets (packages, paths, files, scopes) a
// command may name and still be scoped. Past it the command is no longer a
// quick check of what an agent touched but a sweep of a good part of the tree,
// and it queues with the other long jobs. Eight covers a change to a package
// and its handful of importers, which is what `go test -short` on a diff names.
const maxScopedTargets = 8

// express reports whether a shell line runs only scoped heavy commands: at
// least one simple command matches a heavy pattern, and every one that does is
// scoped (see scopedCommand). One bulk command anywhere in the line makes the
// whole line bulk: the slot is held for the line, not for each command.
func express(patterns []pattern, line string) bool {
	heavy := false
	for _, words := range simpleCommands(shellparse.StripHeredocs(line)) {
		if !matchWords(patterns, words) {
			continue
		}
		if !scopedCommand(words) {
			return false
		}
		heavy = true
	}
	return heavy
}

// scopedCommand reports whether one heavy simple command names its targets
// explicitly, none of them the whole module, and at most maxScopedTargets of
// them. Anything it does not recognize is bulk: the express lane is for the
// commands known to be short, never a guess.
func scopedCommand(words []string) bool {
	words = skipModifiers(words)
	if len(words) == 0 {
		return false
	}
	prog := filepath.Base(words[0])
	args := words[1:]
	if prog == "npx" {
		args = skipFlags(args)
		if len(args) == 0 {
			return false
		}
		prog, args = filepath.Base(args[0]), args[1:]
	}
	if prog == "reliant" && len(args) > 0 && args[0] == "forge" {
		prog, args = "forge", args[1:]
	}
	if len(args) == 0 {
		return false
	}
	sub, rest := args[0], args[1:]
	switch {
	case prog == "go" && (sub == "test" || sub == "vet" || sub == "build" || sub == "install"):
		// No package argument is the package in the current directory,
		// which the classifier is not given: a module root, or wherever a
		// `cd` earlier in the line left it. Scope that is not known is
		// bulk, so a target is required (and `.` is bulk, scopedTarget).
		targets, ok := goTargets(rest)
		return ok && len(targets) > 0 && len(targets) <= maxScopedTargets && allScoped(targets)
	case prog == "golangci-lint" && sub == "run":
		// No path argument lints ./..., so a path is required.
		targets, ok := lintTargets(rest)
		return ok && len(targets) > 0 && len(targets) <= maxScopedTargets && allScoped(targets)
	case prog == "vitest":
		return vitestScoped(args)
	case prog == "task" && strings.HasPrefix(sub, "test"):
		// `task test… -- <go test args>`: the args name the packages.
		i := indexOf(rest, "--")
		if i < 0 {
			return false
		}
		targets, ok := goTargets(rest[i+1:])
		return ok && len(targets) > 0 && len(targets) <= maxScopedTargets && allScoped(targets)
	case prog == "make":
		return makeScoped(args)
	case prog == "forge" && sub == "lint":
		return forgeLintScoped(rest)
	}
	return false
}

// goValueFlags are the go command's build and test flags that take their value
// as the next word; every other flag is taken for a boolean.
var goValueFlags = map[string]bool{
	"run": true, "skip": true, "count": true, "timeout": true, "p": true, "tags": true,
	"o": true, "C": true, "bench": true, "benchtime": true, "cpu": true, "parallel": true,
	"list": true, "exec": true, "ldflags": true, "gcflags": true, "asmflags": true,
	"gccgoflags": true, "mod": true, "modfile": true, "pkgdir": true, "vet": true,
	"covermode": true, "coverpkg": true, "coverprofile": true, "cpuprofile": true,
	"memprofile": true, "memprofilerate": true, "blockprofile": true,
	"blockprofilerate": true, "mutexprofile": true, "mutexprofilefraction": true,
	"trace": true, "outputdir": true, "shuffle": true, "fuzz": true, "fuzztime": true,
	"fuzzminimizetime": true, "toolexec": true, "overlay": true, "pgo": true,
	"compiler": true, "installsuffix": true, "buildmode": true,
}

// goTargets is the package arguments of a go build or test command line. ok is
// false for a line that instruments or names the whole module some other way
// (-coverpkg=./...).
func goTargets(args []string) (targets []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-args" || a == "--args" {
			break // the rest goes to the test binary
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			targets = append(targets, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !hasVal && goValueFlags[name] && i+1 < len(args) {
			i++
			val = args[i]
		}
		if name == "coverpkg" && strings.Contains(val, "...") {
			return nil, false
		}
	}
	return targets, true
}

// lintValueFlags are golangci-lint run's flags that take their value as the
// next word.
var lintValueFlags = map[string]bool{
	"c": true, "config": true, "j": true, "concurrency": true, "timeout": true,
	"build-tags": true, "out-format": true, "new-from-rev": true, "new-from-patch": true,
	"new-from-merge-base": true, "E": true, "enable": true, "D": true, "disable": true,
	"enable-only": true, "p": true, "presets": true, "skip-dirs": true, "skip-files": true,
	"e": true, "exclude": true, "exclude-dirs": true, "exclude-files": true,
	"path-prefix": true, "modules-download-mode": true, "max-issues-per-linter": true,
	"max-same-issues": true, "sort-order": true, "go": true, "output.text.path": true,
	"output.json.path": true,
}

func lintTargets(args []string) (targets []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			targets = append(targets, a)
			continue
		}
		name, _, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !hasVal && lintValueFlags[name] {
			i++
		}
	}
	return targets, true
}

// vitestScoped: `vitest related <files>`, `vitest run <filters>` and
// `vitest <filters>` name what they run; vitest with no filter runs every
// test file (and, on a terminal, watches).
func vitestScoped(args []string) bool {
	if len(args) > 0 && (args[0] == "run" || args[0] == "related") {
		args = args[1:]
	}
	var targets []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			targets = append(targets, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			targets = append(targets, a)
			continue
		}
		name, _, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "shard" || name == "changed" {
			return false // a slice of the whole suite, or a git diff's worth
		}
		if !hasVal && vitestValueFlags[name] {
			i++
		}
	}
	return len(targets) > 0 && len(targets) <= maxScopedTargets
}

var vitestValueFlags = map[string]bool{
	"c": true, "config": true, "r": true, "root": true, "dir": true, "project": true,
	"reporter": true, "environment": true, "pool": true, "t": true,
	"testNamePattern": true, "outputFile": true, "maxWorkers": true, "minWorkers": true,
	"testTimeout": true, "mode": true,
}

// makeScoped: `make test… PKGS=<packages>` (or PKG=), every goal a test
// goal. Without the variable a make test target runs the whole suite.
func makeScoped(args []string) bool {
	var targets []string
	goals := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "-f" || a == "-j":
			i++
		case strings.HasPrefix(a, "-"):
		case isAssignment(a):
			name, val, _ := strings.Cut(a, "=")
			if name == "PKGS" || name == "PKG" {
				targets = append(targets, strings.Fields(val)...)
			}
		default:
			if !strings.HasPrefix(a, "test") {
				return false
			}
			goals++
		}
	}
	return goals > 0 && len(targets) > 0 && len(targets) <= maxScopedTargets && allScoped(targets)
}

// forgeLintScoped: `forge lint --scope <path>…` lints only those paths; forge
// lint without --scope lints the whole project.
func forgeLintScoped(args []string) bool {
	var scopes []string
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if name != "--scope" {
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return false
			}
			i++
			val = args[i]
		}
		if path.Clean(val) == "." {
			return false // the project root is the whole project
		}
		scopes = append(scopes, val)
	}
	return len(scopes) > 0 && len(scopes) <= maxScopedTargets && allScoped(scopes)
}

func allScoped(targets []string) bool {
	for _, t := range targets {
		if !scopedTarget(t) {
			return false
		}
	}
	return true
}

// scopedTarget reports whether one package or path argument is narrower than
// the module. A target without a wildcard names one package or path. A
// wildcard (…/...) is scoped only when it is relative and at least two
// directories deep: ./internal/foo/... is one subtree, while ./..., ./internal/...
// or ./pkg/... are most of a module, and an absolute, parent-relative or
// import-path wildcard cannot be told apart from a module root. The go
// command's own patterns (all, std, cmd) are bulk, and so is the current
// directory (".", "./"): the classifier is not given it, and it may be a
// module or project root.
func scopedTarget(t string) bool {
	switch t {
	case "all", "std", "cmd", "", ".", "./":
		return false
	}
	if !strings.Contains(t, "...") {
		return true
	}
	dir, ok := strings.CutSuffix(t, "/...")
	if !ok || strings.Contains(dir, "...") || !strings.HasPrefix(dir, "./") {
		return false
	}
	dir = path.Clean(dir)
	if dir == "." || strings.HasPrefix(dir, "..") {
		return false
	}
	return strings.Count(dir, "/") >= 1
}

func indexOf(words []string, w string) int {
	for i, x := range words {
		if x == w {
			return i
		}
	}
	return -1
}
