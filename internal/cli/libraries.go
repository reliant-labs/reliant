// Package cli — `forge project libraries`: where forge/pkg actually is on
// THIS machine, and what each subpackage is for.
//
// WHY THIS EXISTS. A dogfood run caught three independent agents running a
// full-disk `find / -path "*forge/pkg/svcerr*"` in the same session, hunting
// for library source. Every one of them already knew the package name; what
// they could not answer was "where is it, and what else is in there". The
// existing answer — a `forge-libraries` skill — was loaded by nobody, and
// its hand-written table had drifted anyway: it listed a `pkg/dialects` that
// does not exist and omitted nine packages that do. A stale index nobody
// opens is not a routing mechanism.
//
// Everything here is ENUMERATED, never transcribed:
//
//   - the DIRECTORY comes from `go list -m`, so it is the go toolchain's own
//     answer for the pkg version THIS project resolves — a go.work replace
//     in a dev tree, the module cache in a release build, without this code
//     knowing which.
//   - the PACKAGE SET is the subdirectories of that directory.
//   - each PURPOSE is the package's own doc comment, read from that source.
//
// So the output cannot drift from the packages, and it describes the
// version the project actually compiles against rather than whatever the
// forge binary was built beside.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/goexec"
)

// forgePkgModule is the IMPORT PATH PREFIX of forge's public runtime
// libraries. It is no longer a module path: pkg/ is a directory inside the
// forge module (forgeModulePath, declared in skill.go), so the toolchain is
// asked about forge and the pkg/ subdirectory is joined on afterwards. The
// import paths themselves are unchanged, which is why this constant is.
const forgePkgModule = "github.com/reliant-labs/forge/pkg"

// LibrarySpec is one forge/pkg subpackage: what to import, where its source
// is, and the opening line of its own package doc.
type LibrarySpec struct {
	Name       string `json:"name"`               // e.g. "svcerr"
	ImportPath string `json:"import_path"`        // e.g. "github.com/reliant-labs/forge/pkg/svcerr"
	Dir        string `json:"dir"`                // absolute path to the package source
	Synopsis   string `json:"synopsis,omitempty"` // first sentence of the package doc
	// Symbols is the package's exported surface, present only for the
	// packages --signatures selected. See libraries_signatures.go.
	Symbols []LibrarySymbol `json:"symbols,omitempty"`
}

// LibrariesSpec is the whole dump: the resolved Go module and its packages,
// then the frontend runtime. forge ships two runtime libraries — forge/pkg
// for Go and @reliantlabs/forge-web-runtime for the web — and a verb that
// indexed only the first sent every agent looking for the second onto the
// filesystem.
type LibrariesSpec struct {
	Module   string        `json:"module"`
	Version  string        `json:"version,omitempty"`
	Dir      string        `json:"dir"`
	Packages []LibrarySpec `json:"packages,omitempty"`
	// WebRuntime is nil when no frontend in this project depends on it.
	WebRuntime *WebRuntimeSpec `json:"web_runtime,omitempty"`
	// SignatureSelectors echoes what --signatures asked for, so a pasted
	// brief records which slice of the API it carries — and which it does
	// not. Empty when signatures were not requested.
	SignatureSelectors string `json:"signature_selectors,omitempty"`
	// Divergence is non-nil when the module resolution above (the one
	// the build actually uses) does not match what an OFFLINE `go doc`
	// consults — a go.work `use`/replace pointing forge/pkg at a local
	// checkout. nil means the two agree, which is the overwhelmingly
	// common case and prints nothing extra.
	Divergence *LibraryDivergence `json:"divergence,omitempty"`
}

// LibraryDivergence records a resolved-vs-module-cache mismatch for
// forge/pkg: the version pinned in go.mod (what a `go doc` run against
// the module cache alone would show) versus the directory this project
// actually builds against (a go.work `use` or a go.mod `replace`
// pointing at a local checkout).
//
// It exists because `go doc`'s answer depends on ambient state (GOWORK,
// which directory the invoker's shell happens to be in, whether a
// subprocess inherited it) in a way the build itself does not: the
// compiler always resolves through the active workspace/replace, so an
// API read that skips it is describing a package that is not the one
// linked into the binary.
type LibraryDivergence struct {
	// GoModVersion is the version github.com/reliant-labs/forge/pkg is
	// pinned to in this project's go.mod requirement.
	GoModVersion string `json:"go_mod_version"`
	// ResolvedDir echoes LibrariesSpec.Dir — the directory the build
	// actually uses. Repeated here so a consumer of just the
	// divergence field doesn't have to cross-reference the parent.
	ResolvedDir string `json:"resolved_dir"`
	// Source names what caused the override: "go.work" when GOWORK
	// pointed at an active workspace file, "replace" when a go.mod
	// replace directive did it with no workspace involved.
	Source string `json:"source"`
}

func newLibrariesCmd() *cobra.Command {
	var asJSON bool
	var signatures string
	cmd := &cobra.Command{
		Use:   "libraries [package...]",
		Short: "Print forge/pkg's API — name a package to get its full signatures",
		Long: `Print forge's public runtime libraries, and the full API of any you name.

With NO arguments this is an index: forge ships TWO runtime libraries and it
prints both — forge/pkg for Go and @reliantlabs/forge-web-runtime for the frontend
— one line per package, from each package's own doc comment.

NAME A PACKAGE and it stops being an index and answers the question:

  forge project libraries crud             every signature crud exports
  forge project libraries crud orm svcerr  three packages, one call
  forge project libraries orm.Context      one type, with its methods
  forge project libraries all              everything (large — prefer a list)

That prints every func with its parameters, every struct with its fields,
every interface and type with its methods, parsed out of the source this
project actually resolves. Doc prose is omitted, so the block is API and
nothing else.

Use this INSTEAD of 'go doc <pkg>', which cannot answer the same question:
'go doc' renders a struct or interface as 'struct{ ... }' and lists no
methods at all, so 'go doc .../crud' never mentions crud.Repo.UpdateMasked.
'go doc -all <pkg>' is complete but roughly ten times larger, most of it
prose. Neither needs the source directory, and neither does reading files.

A selector naming a package or symbol that does not exist is an error listing
what does, so a briefing script whose list has gone stale fails loudly rather
than quietly shipping an incomplete API.

Examples:
  forge project libraries
  forge project libraries --json
  forge project libraries svcerr crud tdd testkit orm.Context`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := buildLibrariesSpec(cmd.Context())
			if err != nil {
				return err
			}
			if err := attachSignatures(&spec, mergeSignatureSelectors(signatures, args)); err != nil {
				return err
			}
			return writeLibraries(cmd.OutOrStdout(), spec, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the inventory as JSON")
	cmd.Flags().StringVar(&signatures, "signatures", "",
		"same selectors as the positional form, comma-separated: `pkg`, `pkg.Symbol`, or `all`")
	return cmd
}

// mergeSignatureSelectors folds the positional selectors and the
// --signatures flag into the one comma-separated list attachSignatures
// parses.
//
// The POSITIONAL form is the point. `--signatures` shipped first and was
// measurably not enough: a run three weeks later spent 35.5 minutes across
// 89 turns grepping forge's own pkg/ source for signatures this flag would
// have printed, and not one unit passed it. A capability reachable only
// through a flag you must already know about is discovered by reading
// --help, which is a step nobody takes when they believe they already know
// the command. `forge project libraries crud` is what someone types when
// they want crud's API, so that is what it now does.
//
// The flag stays because scripts pass it, and the two compose rather than
// conflict: a caller may pass either, or both, and gets the union.
func mergeSignatureSelectors(flag string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	if trimmed := strings.TrimSpace(flag); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, a := range args {
		// A comma-separated positional is accepted too: it is the spelling
		// the flag takes and the one a caller who learned the flag first
		// will reach for, and rejecting it would be forge being pedantic
		// about a separator it accepts one word to the left.
		if trimmed := strings.TrimSpace(a); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, ",")
}

// buildLibrariesSpec resolves the forge/pkg module and reads its packages.
func buildLibrariesSpec(ctx context.Context) (LibrariesSpec, error) {
	dir, version, err := resolveForgePkgDir(ctx)
	if err != nil {
		return LibrariesSpec{}, err
	}
	pkgs, err := readForgePkgPackages(dir)
	if err != nil {
		return LibrariesSpec{}, err
	}
	// Fail loudly rather than print a confident empty list: an empty set
	// here means the resolution pointed somewhere wrong, and a caller that
	// believes "forge ships no libraries" writes them all by hand.
	if len(pkgs) == 0 {
		return LibrariesSpec{}, fmt.Errorf(
			"resolved %s to %s but found no subpackages there — the module directory looks wrong or incomplete",
			forgePkgModule, dir)
	}
	spec := LibrariesSpec{Module: forgePkgModule, Version: version, Dir: dir, Packages: pkgs}
	spec.Divergence = detectLibraryDivergence(ctx, dir)

	// The frontend half needs a project root: run from outside one — the
	// forge repo itself, say — the Go inventory still answers.
	//
	// Everything past that point fails loudly, for the reason above. A
	// project with no frontend and one whose node_modules is missing are
	// both non-errors that buildWebRuntimeSpec reports in the spec itself;
	// an error here means the package IS installed and could not be read,
	// and swallowing it would print "no frontend depends on it" over a
	// frontend that does.
	if projectDir, perr := projectRootDir(); perr == nil {
		rt, rerr := buildWebRuntimeSpec(projectDir)
		if rerr != nil {
			return LibrariesSpec{}, rerr
		}
		spec.WebRuntime = rt
	}
	return spec, nil
}

// projectRootDir finds the project root by walking up for forge.yaml, the
// same anchor every other project-scoped verb uses.
func projectRootDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "forge.yaml")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no forge.yaml found walking up from %s", currentDirForMessage())
		}
		dir = parent
	}
}

// resolveForgePkgDir asks the go toolchain where forge's runtime libraries
// live for the module in the current directory: it resolves the forge MODULE
// and returns its pkg/ subdirectory.
//
// `go list -m` is the right question to ask because it is the SAME
// resolution the compiler performs: a `replace` or a go.work `use` pointing
// at a local checkout resolves to that checkout, and everything else
// resolves into the module cache. Reimplementing either lookup here would
// be a second answer that can disagree with the build.
func resolveForgePkgDir(ctx context.Context) (dir, version string, err error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}\t{{.Version}}", forgeModulePath)
	cmd.Env = goexec.Env()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	if runErr != nil {
		return "", "", fmt.Errorf(
			"could not resolve %s from %s: %w\n%s\n"+
				"Run this from inside a forge project (the module that requires forge)",
			forgeModulePath, currentDirForMessage(), runErr, strings.TrimSpace(stderr.String()))
	}

	moduleDir, version, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	moduleDir = strings.TrimSpace(moduleDir)
	if moduleDir == "" {
		// `go list -m` answers with an empty Dir when the module is in the
		// build list but not extracted locally. Naming the fix matters more
		// than the diagnosis.
		return "", "", fmt.Errorf(
			"%s is required but its source is not on disk yet — run `go mod download %s` first",
			forgeModulePath, forgeModulePath)
	}
	// The libraries live under pkg/ inside that module.
	dir = filepath.Join(moduleDir, "pkg")
	if st, statErr := os.Stat(dir); statErr != nil || !st.IsDir() {
		return "", "", fmt.Errorf(
			"%s resolved to %s, which has no pkg/ directory — that is not a forge module tree",
			forgeModulePath, moduleDir)
	}
	return dir, strings.TrimSpace(version), nil
}

func currentDirForMessage() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "the current directory"
}

// detectLibraryDivergence reports whether resolvedDir — the directory
// this project's BUILD actually resolves forge/pkg to — sits outside
// the module cache, meaning a go.work `use`/replace or a go.mod
// `replace` is overriding the pinned go.mod version.
//
// The module cache is the one place `go list -m` and `go doc` are
// guaranteed to agree: nothing else lives there, and nothing gets
// there except a real, addressable module version. A resolved
// directory anywhere else is necessarily an override, so "outside
// GOMODCACHE" is a sufficient — not just a matching — condition.
// Errors resolving GOMODCACHE or go.mod's pinned version are
// swallowed to nil: this is an advisory extra on top of the inventory
// buildLibrariesSpec already has to have gotten right to reach here,
// and a detector that can fail the whole command over a second-order
// check would be a worse trade than silently skipping the notice.
func detectLibraryDivergence(ctx context.Context, resolvedDir string) *LibraryDivergence {
	modCache, err := goEnv(ctx, "GOMODCACHE")
	if err != nil || modCache == "" {
		return nil
	}
	absResolved, err := filepath.Abs(resolvedDir)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(modCache, absResolved)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Resolved dir is inside (or IS) the module cache — no override.
		return nil
	}

	goModVersion, gerr := forgePkgVersionInGoMod(ctx)
	if gerr != nil || goModVersion == "" {
		return nil
	}

	source := "replace"
	if gowork, werr := goEnv(ctx, "GOWORK"); werr == nil && strings.TrimSpace(gowork) != "" {
		source = "go.work"
	}

	return &LibraryDivergence{
		GoModVersion: goModVersion,
		ResolvedDir:  absResolved,
		Source:       source,
	}
}

// goEnv runs `go env <name>` and returns its trimmed value.
func goEnv(ctx context.Context, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", name)
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// forgePkgVersionInGoMod reads the version github.com/reliant-labs/forge/pkg
// is pinned to in go.mod's own require directive — the version a bare
// `go doc` reads when nothing overrides it — by asking the toolchain
// with GOWORK forced off so a workspace can't shadow the answer. A
// go.mod `replace` alone (no workspace file) still resolves through
// this, since GOWORK=off only disables workspace mode; the underlying
// replace directive still applies. That is intentional: forge/pkg's
// own repo has exactly that shape (go.mod replaces ./pkg, no separate
// go.work override on top of it in a release build), and its go.mod
// pin is still the version a `go doc` run from a directory outside
// this checkout would show.
func forgePkgVersionInGoMod(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Version}}", forgeModulePath)
	cmd.Env = goexec.Env("GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// readForgePkgPackages lists the subdirectories of the resolved module that
// are Go packages, each with the synopsis of its own package doc.
func readForgePkgPackages(moduleDir string) ([]LibrarySpec, error) {
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", moduleDir, err)
	}

	var out []LibrarySpec
	for _, e := range entries {
		name := e.Name()
		// Dot- and underscore-prefixed directories are invisible to the go
		// tool; testdata is data, not a library.
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
			continue
		}
		pkgDir := filepath.Join(moduleDir, name)
		synopsis, isPkg := packageSynopsis(pkgDir)
		if !isPkg {
			continue
		}
		out = append(out, LibrarySpec{
			Name:       name,
			ImportPath: forgePkgModule + "/" + name,
			Dir:        pkgDir,
			Synopsis:   synopsis,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// packageSynopsis reports whether dir holds a non-test Go package and, if
// so, the first sentence of its package doc comment.
//
// The doc comment is the package's OWN description of itself, which is why
// it is the right source: it is written next to the code, reviewed with the
// code, and cannot be forgotten in a separate index.
func packageSynopsis(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		files = append(files, n)
	}
	if len(files) == 0 {
		return "", false
	}
	// doc.go first when present — the conventional home of a package
	// comment — then the rest in a stable order.
	sort.Slice(files, func(i, j int) bool {
		if (files[i] == "doc.go") != (files[j] == "doc.go") {
			return files[i] == "doc.go"
		}
		return files[i] < files[j]
	})

	fset := token.NewFileSet()
	for _, name := range files {
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly|parser.ParseComments)
		if perr != nil || f == nil {
			continue
		}
		if f.Doc != nil {
			if s := firstSentence(f.Doc.Text()); s != "" {
				return stripPackagePrefix(s), true
			}
		}
	}
	// A real package with no doc comment is still a package — report it
	// with an empty synopsis rather than hiding it from the inventory.
	return "", true
}

// stripPackagePrefix drops the conventional "Package foo " / "Package foo —
// " lead-in so the summary carries meaning instead of repeating the name
// already in the first column.
//
// The remainder keeps its original case on purpose. Godoc convention makes
// it a lowercase verb ("provides …", "is the paved path …"), which reads as
// a continuation of the name column; force-capitalizing it produced "Is the
// paved path".
func stripPackagePrefix(s string) string {
	rest, ok := strings.CutPrefix(s, "Package ")
	if !ok {
		return s
	}
	// Drop the package name itself, then any separator that followed it.
	_, after, found := strings.Cut(rest, " ")
	if !found {
		return s
	}
	after = strings.TrimLeft(after, " ")
	for _, sep := range []string{"— ", "-- ", "- ", "– "} {
		if trimmed, cut := strings.CutPrefix(after, sep); cut {
			after = trimmed
			break
		}
	}
	if after == "" {
		return s
	}
	return after
}

// writeLibraryDivergence prints the go.work/go.mod version-skew warning
// when div is non-nil, and nothing at all otherwise — the healthy
// steady state (no workspace, no replace override) must stay exactly
// as quiet as it was before this check existed.
//
// The wording is deliberately scoped to what is provable: an OFFLINE
// `go doc` (GOWORK unset/off, no replace in effect) reads the go.mod
// pin, and that is a real, reachable failure mode — a subprocess that
// doesn't inherit GOWORK, a CI job with no workspace file, an agent
// shelling out from a different directory. It stops short of claiming
// every `go doc` invocation is wrong, because a bare `go doc` run from
// inside an active workspace resolves through it same as the compiler
// does and shows the correct answer.
func writeLibraryDivergence(w io.Writer, module string, div *LibraryDivergence) error {
	if div == nil {
		return nil
	}
	_, err := fmt.Fprintf(w,
		"  resolved: %s   (%s override)\n"+
			"  go.mod:   %s   ← an offline `go doc` (no %s active) reads THIS\n"+
			"  ⚠ `go doc %s` run without the %s active shows %s, not what this project builds against — so use `forge project libraries <pkg>`, which always reads the resolved copy.\n\n",
		div.ResolvedDir, div.Source, div.GoModVersion, div.Source, module, div.Source, div.GoModVersion)
	return err
}

// writeLibrarySourceLine prints where forge/pkg resolved to, framed as a
// resolution fact rather than as a place to go look.
//
// The old line read `Source on this machine: /Users/…/forge/pkg` and sat
// directly above a package list, which is a signpost: it answers "where is
// it" for a reader whose actual question is "what does it export", and the
// only way to get from that path to a signature is to grep it. A measured
// run did exactly that for 35.5 minutes across 89 turns — `pkg/crud/repo.go`
// alone 14 times.
//
// The path itself is kept, because it is genuinely diagnostic: it is how a
// reader confirms WHICH forge/pkg this project builds against (a checkout
// via go.work, or the module cache), and the divergence warning printed
// just below it is unreadable without it. What changes is that the reader
// is handed the command that answers their question in the same breath, so
// the path stops being the only actionable thing on the line.
func writeLibrarySourceLine(w io.Writer, spec LibrariesSpec) error {
	_, err := fmt.Fprintf(w,
		"Resolves to: %s\n"+
			"  (that is which copy you compile against — for what it EXPORTS, don't read it:\n"+
			"   `forge project libraries <pkg>` prints the full signatures.)\n\n",
		spec.Dir)
	return err
}

func writeLibraries(w io.Writer, spec LibrariesSpec, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(spec)
	}
	p := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}

	label := spec.Module
	if spec.Version != "" {
		label += "@" + spec.Version
	}
	if err := p("FORGE LIBRARIES — %s\n", label); err != nil {
		return err
	}
	if err := writeLibrarySourceLine(w, spec); err != nil {
		return err
	}
	if err := writeLibraryDivergence(w, spec.Module, spec.Divergence); err != nil {
		return err
	}
	for _, l := range spec.Packages {
		synopsis := l.Synopsis
		if synopsis == "" {
			// Say so rather than print a blank column: an undocumented
			// package is a real (and fixable) state, not a rendering bug.
			synopsis = "(no package doc — read it with `go doc " + l.ImportPath + "`)"
		}
		if err := p("  %-14s %s\n", l.Name, synopsis); err != nil {
			return err
		}
	}
	if err := p("\nImport as %s/<name>.\n\n%s", spec.Module, deeperAPIGuidance(spec)); err != nil {
		return err
	}
	if err := p("Adopt before porting: if a package covers the surface, use it; if it covers\n" +
		"most of it, extend it project-side rather than forking it. `forge skill load\n" +
		"forge-libraries` has the when-to-adopt guidance.\n"); err != nil {
		return err
	}
	if err := writeSignatures(w, spec); err != nil {
		return err
	}
	return writeWebRuntime(w, spec.WebRuntime)
}

// deeperAPIGuidance is the paragraph that tells a reader how to get past a
// one-line synopsis.
//
// It has to change with --signatures, because the untouched version is a
// measurable liability: it names `go doc .../svcerr` as THE way to see an
// API, and in a ten-unit wave that instruction was followed 36 times, four
// of them for svcerr alone. Left unscoped it would keep saying "go look it
// up" directly above a block that already contains the answer.
//
// So: with no selection, keep it — it is still the only route for 24
// packages — but advertise the one-shot alternative. With a selection,
// point at the block and reserve `go doc` for what is NOT in it, using a
// package that really was left out as the example, so the output never
// tells the reader to fetch something it just printed.
func deeperAPIGuidance(spec LibrariesSpec) string {
	if spec.SignatureSelectors == "" {
		return "" +
			"For what a package EXPORTS, name it — one call, any number of packages:\n" +
			"  forge project libraries crud\n" +
			"  forge project libraries crud orm svcerr\n" +
			"  forge project libraries orm.Context\n" +
			"Prefer that over `go doc`, which prints a struct or interface as\n" +
			"`struct{ ... }` with no methods: `go doc " + shortModuleRef(spec.Module) + "/crud` never\n" +
			"mentions Repo.UpdateMasked. Reading the source files is slower still.\n"
	}
	example := ""
	for _, l := range spec.Packages {
		if len(l.Symbols) == 0 {
			example = l.Name
			break
		}
	}
	if example == "" {
		return fmt.Sprintf("The complete exported API of every package is printed below (%s).\n", spec.SignatureSelectors)
	}
	return fmt.Sprintf(
		"The complete exported API of %s is printed below.\n"+
			"For a package that is NOT down there, name it the same way:\n"+
			"  forge project libraries %s\n",
		spec.SignatureSelectors, example)
}

// shortModuleRef renders the module for a one-line mention of `go doc`.
// The full path is 34 characters of prefix that carry no information at
// the point the sentence is making a comparison.
func shortModuleRef(module string) string {
	if i := strings.LastIndex(module, "/"); i >= 0 {
		return "..." + module[i:]
	}
	return module
}
