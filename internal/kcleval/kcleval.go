// Package kcleval evaluates ONE KCL file of a project and selects fields out
// of the result, so nothing outside forge ever has to run `kcl` itself.
//
// # The gap this closes
//
// Since forge started supplying the `forge` KCL module from the binary
// (docs/adr/0003-kcl-module-from-the-binary.md), a plain `kcl run` on a file
// that says `import forge` CANNOT resolve it: there is no kcl.mod dependency
// to fetch and no project-local copy to read. That is deliberate — the binary
// rendering a project IS the module version it renders against — and it is
// also why a consumer with a legitimate need to read a value out of project
// KCL had nowhere to go.
//
// forge's answer was `forge env render <env>`, which evaluates an
// ENVIRONMENT and prints Kubernetes objects. A project also keeps plain
// declarative constants in library files — control-plane's
// `deploy/kcl/lib/kata_pool.k` declares GKE node-pool inputs, its
// `lib/daemon_placement.k` declares which cluster each env's daemon pods land
// in — and those are read by shell scripts and Go tests, not deployed. There
// was no forge command that evaluated an arbitrary FILE and handed back one
// field.
//
// So consumers reconstructed forge's own render setup from outside: a
// control-plane shell library made forge materialize its module into
// FORGE_KCL_MODULE_CACHE, dug the directory out, and ran
// `kcl run <file> -S <field> -E forge=<dir>`. That works right up until any
// part of forge's render setup changes — the cache layout, the module name,
// the plugin namespace — at which point it breaks in a consumer's CI with an
// error about KCL, naming nothing that would lead anyone back here. It also
// requires `kcl` on PATH, which forge itself has not needed since it embedded
// the runtime.
//
// # What this is NOT
//
// Not an env render. Nothing here resolves an image tag, reads build state,
// routes objects to clusters, or templates a helm chart — see
// internal/cli/env_render.go for that. The unit is a file and the output is
// the file's own values.
//
// Not a port claim. Evaluation arms the read-only halves of the render
// context (internal/cli.armReadOnlyKCLContext): `fp.allocate_port` resolves
// keys to blocks they already hold and `fp.resolve_port` reads its store
// without writing it, so evaluating a file can neither hand out a port block
// nor fail at the dev_stack ceiling. A read-only command never claims a port
// block.
//
// # Why selection happens HERE and not in kcl
//
// kcl's own `-S` renders a selected LIST as one YAML document per element,
// which is unusable without knowing how many elements to expect;
// control-plane's check-kata-pool-staleness.sh carries a hand-written
// workaround for exactly this, and a comment explaining that an empty result
// passes a tier name through as data. Selecting in Go against the parsed JSON
// gives back the value KCL actually produced — an array stays one array —
// and it is the same traversal for a scalar, a list and a schema instance.
package kcleval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// Request is one evaluation: a file, the selections to take out of it, and
// the `-D` option bindings to evaluate it under.
type Request struct {
	// ProjectDir is the project root (the directory holding forge.yaml). The
	// kcl.mod migration checks read it, and File must resolve inside it.
	ProjectDir string
	// File is the .k file to evaluate, relative to ProjectDir or absolute.
	File string
	// Selectors are dotted field paths to take out of the result, in the
	// order given. Empty selects the whole document.
	Selectors []string
	// Options are `key=value` top-level option bindings, as `-D` passes
	// them to kcl.
	Options []string
}

// Result is what an evaluation produced.
type Result struct {
	// Value is the selected value: the whole document when no selector was
	// given, the selected value for exactly one, and an object keyed by
	// selector for several.
	Value any
	// Scalar reports whether Value is a single non-composite value, which is
	// what --format raw is allowed to print.
	Scalar bool
}

// ErrNoSuchField is returned when a selector names a field the evaluated
// document does not have. It is a distinct error because a caller reading an
// optional field wants to tell "absent" from "the file does not evaluate",
// and because a typo'd selector must not read as an empty value — the failure
// mode that made a kata-pool script pass a tier name through as data.
var ErrNoSuchField = errors.New("no such field")

// Eval evaluates req.File and applies req.Selectors.
//
// The evaluation runs with the PROJECT ROOT as the working directory, exactly
// as `env render` does, so one project has one evaluation semantics regardless
// of which file is being evaluated or where the caller stands. Two things
// follow, and they are the two the shell scripts this replaces had to know
// about:
//
//   - A relative `lib.*` import resolves from the file's own kcl.mod package,
//     because that is how kpm resolves a package — not from the cwd. The
//     scripts' `cd deploy/kcl` was never what made their imports work.
//   - A `file.read("deploy/...")` resolves from the project root, because the
//     KCL runtime reads relative paths against the process cwd. THAT is what
//     the `cd` was load-bearing for, and getting it wrong fails with "No such
//     file or directory" naming a path that plainly exists.
//
// A caller in a subdirectory therefore gets the same answer as one at the
// project root — the property `forge -C <dir>` was missing (see
// kclrender.Run's chdir note).
func Eval(req Request) (Result, error) {
	abs, err := resolveFile(req.ProjectDir, req.File)
	if err != nil {
		return Result{}, err
	}
	raw, err := kclrender.RunInWorkDir(req.ProjectDir, abs, req.Options)
	if err != nil {
		return Result{}, err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Result{}, fmt.Errorf("decode the render of %s: %w", req.File, err)
	}
	return selectFrom(doc, req.Selectors)
}

// resolveFile turns req.File into an absolute path inside the project, or
// says why it is not one.
//
// A path OUTSIDE the project is refused rather than evaluated. The forge
// module and the plugin namespace are supplied on the strength of a project's
// identity, and the kcl.mod migration checks are run against THAT project's
// root; handing those to a file somewhere else on the filesystem would
// evaluate it under a project's context while none of the project's own
// checks applied to it.
func resolveFile(projectDir, file string) (string, error) {
	if file == "" {
		return "", errors.New("no file given")
	}
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectDir, file)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(projectDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project at %s; "+
			"a file is evaluated with the project's forge KCL module and kcl.mod checks, "+
			"so it must live in the project", file, projectDir)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; name a .k file "+
			"(to evaluate an environment package, use `env render <env>`)", file)
	}
	if filepath.Ext(abs) != ".k" {
		return "", fmt.Errorf("%s is not a .k file", file)
	}
	return abs, nil
}

// selectFrom applies selectors to a decoded document.
func selectFrom(doc any, selectors []string) (Result, error) {
	switch len(selectors) {
	case 0:
		return Result{Value: doc, Scalar: isScalar(doc)}, nil
	case 1:
		v, err := Select(doc, selectors[0])
		if err != nil {
			return Result{}, err
		}
		return Result{Value: v, Scalar: isScalar(v)}, nil
	}
	// Several selectors compose an object keyed by the selector, matching
	// what repeated `-S` means to kcl. It is never scalar, whatever the
	// members are, so --format raw refuses it rather than inventing a
	// separator between two values.
	out := make(map[string]any, len(selectors))
	for _, s := range selectors {
		v, err := Select(doc, s)
		if err != nil {
			return Result{}, err
		}
		out[s] = v
	}
	return Result{Value: out}, nil
}

// Select walks a dotted path through a decoded KCL document.
//
// A numeric segment indexes a list, so `pools.0.name` reaches into one, and a
// map key that happens to be numeric still wins over the list reading —
// checked in that order because a map is the common case and a KCL map may
// legitimately be keyed "0".
func Select(doc any, path string) (any, error) {
	cur := doc
	walked := make([]string, 0, 4)
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			return nil, fmt.Errorf("selector %q has an empty segment", path)
		}
		next, err := step(cur, seg, path, walked)
		if err != nil {
			return nil, err
		}
		cur = next
		walked = append(walked, seg)
	}
	return cur, nil
}

// step takes one segment of a selector. walked is the path so far, for a
// message that says WHERE the walk stopped rather than only what was asked
// for — "pool_defaults has no field taint_ky" localizes a typo that
// "no such field pool_defaults.taint_ky" does not.
func step(cur any, seg, path string, walked []string) (any, error) {
	at := strings.Join(walked, ".")
	if at == "" {
		at = "the document"
	}
	switch c := cur.(type) {
	case map[string]any:
		if v, ok := c[seg]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: %s has no field %q; it has %s",
			ErrNoSuchField, at, seg, fieldList(c))
	case []any:
		i, err := strconv.Atoi(seg)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is a list of %d, so %q must be an index",
				ErrNoSuchField, at, len(c), seg)
		}
		if i < 0 || i >= len(c) {
			return nil, fmt.Errorf("%w: %s is a list of %d, so index %d is out of range",
				ErrNoSuchField, at, len(c), i)
		}
		return c[i], nil
	default:
		return nil, fmt.Errorf("%w: %s is %s, which has no field %q (selector %q)",
			ErrNoSuchField, at, kindOf(cur), seg, path)
	}
}

// fieldList names a map's fields so a refusal shows what WAS available. It is
// capped: a document with 200 top-level keys would bury the message it is
// attached to.
func fieldList(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	const max = 12
	if len(keys) > max {
		return strings.Join(keys[:max], ", ") + fmt.Sprintf(", … (%d more)", len(keys)-max)
	}
	if len(keys) == 0 {
		return "no fields"
	}
	return strings.Join(keys, ", ")
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "None"
	case bool:
		return "a bool"
	case float64:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return "a value"
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, bool, float64, string:
		return true
	}
	return false
}
