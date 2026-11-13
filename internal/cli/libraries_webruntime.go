// File: internal/cli/libraries_webruntime.go
//
// The frontend half of `forge project libraries`.
//
// forge ships two runtime libraries, not one: forge/pkg for Go and
// @reliantlabs/forge-web-runtime for the web. The verb only ever indexed the Go
// one, so an agent asking "what does the frontend runtime give me, and
// where is it" got no answer from the toolchain and went looking on the
// disk — every refusal the recursive-scan guard has recorded was an agent
// hunting this package.
//
// Everything here is ENUMERATED, the same way the Go side is:
//
//   - the DIRECTORY is wherever npm materialized the dependency
//     (node_modules/@reliantlabs/forge-web-runtime), which is npm's own answer
//     for this project — a file: link in a dev tree, an unpacked tarball
//     in a release one, without this code knowing which.
//   - the VERSION is that package's own package.json.
//   - the ENTRY POINTS are its "exports" map.
//   - the BARREL is the declaration file that same manifest points at, and
//     the MODULES and SYMBOLS are its re-export statements. Each module's
//     purpose is the JSDoc the barrel writes above it — the TS analogue of
//     a Go package doc.
//
// The barrel path is asked of the manifest rather than remembered here.
// This code hardcoded "src/index.ts" until the package began publishing
// built declarations from dist/ and dropped src/ from the tarball; off a
// released install that path does not exist, so the verb reported the
// runtime with zero modules and no error. A layout forge transcribes is a
// layout forge can be wrong about; "exports" is the package's own answer.

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// webRuntimePackage is the npm name of forge's frontend runtime.
const webRuntimePackage = "@reliantlabs/forge-web-runtime"

// WebRuntimeModule is one module the runtime's barrel re-exports: the
// symbols it contributes and the barrel's own comment about it.
type WebRuntimeModule struct {
	Name     string   `json:"name"`               // e.g. "interceptors" (the ./interceptors source module)
	Symbols  []string `json:"symbols"`            // exported identifiers, in barrel order
	Synopsis string   `json:"synopsis,omitempty"` // first sentence of the barrel's comment for the group
}

// WebRuntimeSpec is the frontend-runtime inventory. Dir is empty when the
// package is declared but not installed; Reason then says why.
type WebRuntimeSpec struct {
	Package     string             `json:"package"`
	Version     string             `json:"version,omitempty"`
	Dir         string             `json:"dir,omitempty"`
	Description string             `json:"description,omitempty"`
	EntryPoints []string           `json:"entry_points,omitempty"` // import specifiers from the exports map
	Modules     []WebRuntimeModule `json:"modules,omitempty"`
	// Declared is the specifier the frontend's package.json asks for, kept
	// even when the package is not on disk so the reader can tell a dev
	// `file:` bridge from a published range.
	Declared string `json:"declared,omitempty"`
	// Reason explains an empty Dir rather than leaving the caller to guess
	// whether the runtime exists at all.
	Reason string `json:"reason,omitempty"`
}

// buildWebRuntimeSpec locates and reads the frontend runtime for the
// project rooted at projectDir. A project with no frontend gets (nil, nil):
// that is a legitimate shape, not a failure.
func buildWebRuntimeSpec(projectDir string) (*WebRuntimeSpec, error) {
	declared, declaredIn := declaredWebRuntimeSpecifier(projectDir)
	dir := findInstalledWebRuntime(projectDir)
	if dir == "" {
		if declared == "" {
			return nil, nil // no frontend depends on it
		}
		return &WebRuntimeSpec{
			Package:  webRuntimePackage,
			Declared: declared,
			Reason: fmt.Sprintf("declared in %s as %q but not installed — run `npm install` in that frontend",
				declaredIn, declared),
		}, nil
	}

	manifestPath := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read the installed runtime's manifest %s: %w", manifestPath, err)
	}
	var manifest webRuntimeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", manifestPath, err)
	}

	spec := &WebRuntimeSpec{
		Package:     webRuntimePackage,
		Dir:         dir,
		Declared:    declared,
		Version:     manifest.Version,
		Description: manifest.Description,
	}
	for subpath := range manifest.Exports {
		if subpath == "./package.json" {
			continue // resolver plumbing, not an import an author writes
		}
		spec.EntryPoints = append(spec.EntryPoints, strings.Replace(subpath, ".", webRuntimePackage, 1))
	}
	sort.Strings(spec.EntryPoints)

	barrel, err := webRuntimeBarrelPath(dir, manifest)
	if err != nil {
		return nil, err
	}
	spec.Modules, err = readWebRuntimeBarrel(barrel)
	if err != nil {
		return nil, fmt.Errorf(
			"%s is installed at %s but its inventory could not be read: %w\n"+
				"If that path is a local checkout, build it (`npm run build` there); otherwise reinstall the package",
			webRuntimePackage, dir, err)
	}
	return spec, nil
}

// webRuntimeManifest is the part of the package's own package.json this verb
// reads: what it is, what an author may import, and where its public
// declarations live.
type webRuntimeManifest struct {
	Version     string                     `json:"version"`
	Description string                     `json:"description"`
	Types       string                     `json:"types"`
	Exports     map[string]json.RawMessage `json:"exports"`
}

// typesDeclaration returns the package's declared public declaration entry:
// the "types" condition of the root export, or the top-level "types" field.
// Both are the package telling its consumers where its API is described;
// TypeScript reads the first and falls back to the second, so this asks the
// same question in the same order.
func (m webRuntimeManifest) typesDeclaration() string {
	if raw, ok := m.Exports["."]; ok {
		// The root export is either a conditions object or a bare specifier
		// with no types condition to read.
		var conditions map[string]string
		if json.Unmarshal(raw, &conditions) == nil && conditions["types"] != "" {
			return conditions["types"]
		}
	}
	return m.Types
}

// webRuntimeBarrelPath resolves the package-relative declaration entry to a
// path on this machine.
func webRuntimeBarrelPath(dir string, m webRuntimeManifest) (string, error) {
	rel := m.typesDeclaration()
	if rel == "" {
		return "", fmt.Errorf(
			"%s in %s declares no \"types\" entry, so its public API cannot be read — "+
				"the installed package looks incomplete or is not the forge web runtime",
			webRuntimePackage, dir)
	}
	return filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(rel, "./"))), nil
}

// findInstalledWebRuntime returns the directory npm resolved the runtime
// to, or "". Reading node_modules is not a reimplementation of node's
// resolution — it is that resolution's own output, the npm counterpart of
// asking `go list -m` for a module directory.
func findInstalledWebRuntime(projectDir string) string {
	rel := filepath.Join("node_modules", filepath.FromSlash(webRuntimePackage))
	roots := []string{projectDir}
	if entries, err := os.ReadDir(filepath.Join(projectDir, "frontends")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				roots = append(roots, filepath.Join(projectDir, "frontends", e.Name()))
			}
		}
	}
	for _, root := range roots {
		cand := filepath.Join(root, rel)
		// EvalSymlinks resolves the dev `file:` bridge npm installs as a
		// symlink, so the printed path is the checkout an author can edit.
		if info, err := os.Stat(cand); err == nil && info.IsDir() {
			if real, rerr := filepath.EvalSymlinks(cand); rerr == nil {
				return real
			}
			return cand
		}
	}
	return ""
}

// declaredWebRuntimeSpecifier returns the version specifier the first
// frontend that depends on the runtime asks for, and which manifest said
// so. It is the answer to "is this a dev bridge or a published range".
func declaredWebRuntimeSpecifier(projectDir string) (spec, foundIn string) {
	entries, err := os.ReadDir(filepath.Join(projectDir, "frontends"))
	if err != nil {
		return "", ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		manifestPath := filepath.Join(projectDir, "frontends", name, "package.json")
		raw, rerr := os.ReadFile(manifestPath)
		if rerr != nil {
			continue
		}
		var m struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for _, deps := range []map[string]string{m.Dependencies, m.DevDependencies} {
			if v, ok := deps[webRuntimePackage]; ok {
				return v, filepath.Join("frontends", name, "package.json")
			}
		}
	}
	return "", ""
}

// barrelReexportRE matches a barrel re-export statement and captures the
// braced symbol list and the source module.
var barrelReexportRE = regexp.MustCompile(`(?s)export\s*\{(.*?)\}\s*from\s*"\./([A-Za-z0-9_.-]+)"`)

// readWebRuntimeBarrel derives the module inventory from the package's
// public barrel: what each module contributes, and the comment the barrel
// writes above it.
//
// The barrel is the right source for the same reason a package doc is on
// the Go side — it is the file that decides what is public, it is written
// next to the code, and it cannot drift from what actually ships.
//
// An unreadable or empty barrel is an ERROR, exactly as an empty forge/pkg
// package set is on the Go side. The failure mode this guards is not a
// crash, it is a confident lie: the section header, the directory and the
// entry points all print, with no symbols under them, and the reader
// concludes the runtime exports nothing and writes it all by hand.
func readWebRuntimeBarrel(path string) ([]WebRuntimeModule, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the public barrel: %w", err)
	}
	content := string(raw)

	var out []WebRuntimeModule
	for _, loc := range barrelReexportRE.FindAllStringSubmatchIndex(content, -1) {
		symbols := parseBarrelSymbols(content[loc[2]:loc[3]])
		if len(symbols) == 0 {
			continue
		}
		out = append(out, WebRuntimeModule{
			Name:     barrelModuleName(content[loc[4]:loc[5]]),
			Symbols:  symbols,
			Synopsis: firstSentence(commentAbove(content[:loc[0]])),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(
			"%s re-exports nothing readable, so the runtime's inventory would print empty — "+
				"either the barrel is not the one this verb expects or the package is incomplete",
			path)
	}
	return out, nil
}

// barrelModuleName strips a specifier's extension. A package with
// "type": "module" writes "./errors.js" even in TypeScript source, but the
// module an author asks about is "errors".
func barrelModuleName(specifier string) string {
	// .d.ts before .ts so a declaration specifier does not leave a ".d".
	for _, ext := range []string{".js", ".mjs", ".cjs", ".jsx", ".d.ts", ".ts", ".tsx"} {
		if name, ok := strings.CutSuffix(specifier, ext); ok {
			return name
		}
	}
	return specifier
}

// parseBarrelSymbols splits a braced re-export list into identifiers,
// dropping the `type ` qualifier (an author imports `Session`, not
// `type Session`) and any `as` alias's left side.
func parseBarrelSymbols(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		s = strings.TrimPrefix(s, "type ")
		if _, alias, ok := strings.Cut(s, " as "); ok {
			s = alias
		}
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// commentAbove returns the contiguous comment block that ends the given
// prefix — the lines immediately above the statement that follows — in
// either TypeScript comment form.
//
// JSDoc is not a stylistic alternative here: tsc's declaration emit
// PRESERVES `/** */` and DROPS `//`, so a group documented with line
// comments has no synopsis at all in the published artifact this reads.
func commentAbove(prefix string) string {
	lines := strings.Split(prefix, "\n")
	// The last element is the partial line the statement starts on.
	if len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	if strings.HasSuffix(strings.TrimSpace(lines[len(lines)-1]), "*/") {
		return jsdocAbove(lines)
	}
	var block []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "//") {
			break
		}
		block = append([]string{strings.TrimSpace(strings.TrimPrefix(line, "//"))}, block...)
	}
	return strings.TrimSpace(strings.Join(block, " "))
}

// jsdocAbove reads the `/** … */` block that closes on the last of lines,
// stripping the frame so the text reads as prose. Every line of such a block
// opens with `/*` or continues with `*` (`*/` included); anything else means
// the `*/` did not belong to a block comment sitting on these lines.
func jsdocAbove(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "/*") {
			if !strings.HasPrefix(line, "*") {
				return ""
			}
			continue
		}
		var block []string
		for _, l := range lines[i:] {
			l = strings.TrimSpace(l)
			l = strings.TrimPrefix(l, "/**")
			l = strings.TrimPrefix(l, "/*")
			l = strings.TrimSuffix(strings.TrimSpace(l), "*/")
			l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "*"))
			if l != "" {
				block = append(block, l)
			}
		}
		return strings.TrimSpace(strings.Join(block, " "))
	}
	return ""
}

// writeWebRuntime renders the frontend-runtime section of the text output.
func writeWebRuntime(w io.Writer, spec *WebRuntimeSpec) error {
	if spec == nil {
		_, err := fmt.Fprintf(w, "\nFORGE WEB RUNTIME — %s\n"+
			"  No frontend in this project depends on it. `forge scaffold frontend <name>`\n"+
			"  declares it.\n", webRuntimePackage)
		return err
	}
	p := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}

	label := spec.Package
	if spec.Version != "" {
		label += "@" + spec.Version
	}
	if err := p("\nFORGE WEB RUNTIME — %s\n", label); err != nil {
		return err
	}
	if spec.Dir == "" {
		return p("  %s\n", spec.Reason)
	}
	if err := p("Source on this machine: %s\n", spec.Dir); err != nil {
		return err
	}
	if spec.Declared != "" {
		if err := p("Declared as: %s\n", spec.Declared); err != nil {
			return err
		}
	}
	if len(spec.EntryPoints) > 0 {
		if err := p("Import from: %s\n", strings.Join(spec.EntryPoints, ", ")); err != nil {
			return err
		}
	}
	if err := p("\n"); err != nil {
		return err
	}
	for _, m := range spec.Modules {
		if m.Synopsis != "" {
			if err := p("  %-14s %s\n", m.Name, m.Synopsis); err != nil {
				return err
			}
		} else {
			if err := p("  %s\n", m.Name); err != nil {
				return err
			}
		}
		if err := p("  %-14s   %s\n", "", strings.Join(m.Symbols, ", ")); err != nil {
			return err
		}
	}
	return p("\nThe runtime is a PACKAGE: there is nothing to edit and nothing to\n" +
		"regenerate. `forge skill load frontend-runtime` has the composition rules.\n")
}
