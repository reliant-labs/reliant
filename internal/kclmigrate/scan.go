package kclmigrate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file is the TEXTUAL reader the migration runs on. It exists because the
// tree being migrated NO LONGER COMPILES — `registry` is a closed-schema error
// on both schemas now — so there is nothing to render and read values off. Every
// fact below is therefore recovered from source text.
//
// The reader is deliberately CONSERVATIVE. Anything it cannot resolve to a
// certainty is reported as unresolved rather than guessed, because the caller's
// contract is that an unresolved input becomes a REFUSAL. Over-reporting costs a
// human one decision; under-reporting silently drops a registry and the tree
// fails minutes later at render, which is the defect this file was rewritten to
// remove.

var (
	// `registry = <anything>` on its own line. The RHS is captured WHOLE and
	// resolved separately, because the value is very often an identifier
	// (`registry = _registry`) rather than a literal — the style the first
	// version of this migration matched, and the reason it silently did
	// nothing on every real project that declared its registry once at the
	// top of the file.
	registryAssignRe = regexp.MustCompile(`(?m)^[ \t]*registry[ \t]*=[ \t]*([^\n#]+?)[ \t]*(?:#[^\n]*)?$`)
	// `registry = <expr>` inline in a braced literal, with a comma either side.
	registryInlineRe = regexp.MustCompile(`,[ \t]*registry[ \t]*=[ \t]*[^,}\n]+|registry[ \t]*=[ \t]*[^,}\n]+,[ \t]*`)

	// `<name> = "<literal>"`, at ANY indentation. Indentation is not a
	// reliable signal of scope in KCL — a lambda body's assignments are
	// indented and are still the only assignment to that name — so the
	// single-assignment check below carries the certainty instead.
	literalAssignRe = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*"([^"]*)"[ \t]*(?:#[^\n]*)?$`)
	// Any assignment to a name, whatever the RHS. Counted so that a name
	// assigned twice is never resolved to one of its values.
	anyAssignRe = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*\S`)

	// `_name = forge.OnCluster {…}` — a runtime bound to a variable and
	// referred to by name later (`runtime = _on_cp`). Indentation allowed:
	// the assembly lambda in a project's lib/stack.k declares its runtimes
	// inside the lambda body.
	runtimeVarRe = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*forge\.(OnCluster|OnHosted|OnHost|OnCompose|BuildOnly)\b`)
	// `_name = lambda …` — a binder. Its body is read to learn which runtime
	// it constructs; it is never rewritten.
	lambdaAssignRe = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*lambda\b`)
	runtimeRe      = regexp.MustCompile(`forge\.(OnCluster|OnHosted|OnHost|OnCompose|BuildOnly)\b`)

	// `runtime = <expr>` inside a workload refinement.
	runtimeFieldRe = regexp.MustCompile(`runtime[ \t]*=[ \t]*([A-Za-z_][A-Za-z0-9_.]*)`)
	// A call-style binder application immediately before a workload
	// reference: `_on_cluster(wl.api)`.
	callBinderTailRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\([ \t\n]*$`)

	// `<ident> = fw.Workload {` / `forge.Workload {` — a declaration in
	// workloads.k.
	workloadStartRe = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*(?:fw|forge)\.Workload[ \t]*\{`)
	// A frontend literal ANYWHERE, including inline in the Bundle's list
	// (`frontends = [forge.Frontend {…}]`), which is what forge scaffolds and
	// what hounders wrote. A reader that required a top-level assignment saw no
	// frontend at all on the shape most projects have.
	frontendLiteralRe = regexp.MustCompile(`(?:fe|fw|forge)\.Frontend[ \t]*\{`)
	// An env-local refinement of a shared workload: `_membership = wl.membership
	// | {…}`. Captures the local name, the module alias, and the workload.
	localRefinementRe = regexp.MustCompile(`(?m)^(_[A-Za-z0-9_]*)[ \t]*=[ \t]*([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)[ \t]*\|`)
	// A binder applied to an env-local name: `_hosted(_membership)`.
	localAliasBindingRe = regexp.MustCompile(`(_[A-Za-z0-9_]*)\(\s*(_[A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	// `runtime = forge.OnHosted` inside a frontend literal — the one frontend
	// runtime that publishes its static build to a registry.
	hostedFrontendRuntimeRe = regexp.MustCompile(`runtime[ \t]*=[ \t]*(?:forge\.OnHosted\b|(_[A-Za-z0-9_]*))`)

	imageLineRe = regexp.MustCompile(`(?m)^([ \t]*)image[ \t]*=[ \t]*"([^"]*)"[ \t]*(#[^\n]*)?$`)
	nameLineRe  = regexp.MustCompile(`(?m)^([ \t]*)name[ \t]*=[ \t]*"([^"]*)"[ \t]*(?:#[^\n]*)?$`)
	// `build = <expr>` — the RHS captured whole, because it is very often a
	// reference to a shared build declared once at the top of the file
	// (`build = _cp_build`) rather than an inline literal. The name it points
	// at is resolved separately; an unresolvable one is a refusal, because
	// `output_name` is what forge DERIVES the image from and guessing it
	// writes a reference to a repository the build never pushes to.
	buildFieldRe     = regexp.MustCompile(`(?m)^[ \t]*build[ \t]*=[ \t]*([^\n#]+?)[ \t]*(?:#[^\n]*)?$`)
	outputNameRe     = regexp.MustCompile(`output_name[ \t]*=[ \t]*"([^"]*)"`)
	importRe         = regexp.MustCompile(`(?m)^import[ \t]+([A-Za-z0-9_.]+)(?:[ \t]+as[ \t]+([A-Za-z0-9_]+))?[ \t]*$`)
	stringLiteralRe  = regexp.MustCompile(`^"([^"]*)"$`)
	identifierRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	workloadsModules = map[string]bool{"..workloads": true, "workloads": true, ".workloads": true}
)

// runtimesThatPull are the runtimes an image reference is needed for. A
// workload bound OnHost or OnCompose runs from the local filesystem or a
// compose file: nothing pulls an image for it, so a registry declared on that
// env says nothing about where the workload's image should live. This is the
// whole basis of the ambiguity judgment.
var runtimesThatPull = map[string]bool{"OnCluster": true, "OnHosted": true}

// kclTree is every .k file under deploy/kcl, read once.
//
// The whole tree is loaded — not just the env main.k files — because a real
// project declares its per-workload bindings in a shared assembly library that
// each env calls, and a reader that looked only at main.k would conclude no env
// binds anything. That conclusion is indistinguishable from "this project has
// nothing to migrate", which is exactly how the first version reported success
// on a tree it had not understood.
type kclTree struct {
	dir   string
	files map[string]string // path relative to deploy/kcl, slash-separated
}

func loadTree(kclDir string) (*kclTree, error) {
	t := &kclTree{dir: kclDir, files: map[string]string{}}
	err := filepath.WalkDir(kclDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".k") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(kclDir, path)
		if rerr != nil {
			return rerr
		}
		t.files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// envs lists the environments: a subdirectory with a main.k.
func (t *kclTree) envs() []string {
	var out []string
	for rel := range t.files {
		dir, file := path2(rel)
		if file == "main.k" && dir != "" && !strings.Contains(dir, "/") {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out
}

func path2(rel string) (dir, file string) {
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return "", rel
	}
	return rel[:i], rel[i+1:]
}

// repoPath is the path as a user sees it in their repo.
func repoPath(rel string) string { return "deploy/kcl/" + rel }

// lineOf is the 1-based line number of a byte offset.
func lineOf(src string, off int) int { return 1 + strings.Count(src[:off], "\n") }

// ─── name resolution ────────────────────────────────────────────────────────

// resolveLiteral resolves `name` to a string literal, looking first in `rel`
// itself and then in the files `rel` imports locally.
//
// A name assigned more than ONCE anywhere in the search scope does not resolve.
// Picking one of two values would be a guess about which branch of the project's
// own logic wins, and the whole contract of this migration is that a guess is
// refused rather than written.
func (t *kclTree) resolveLiteral(rel, name string) (string, bool) {
	for _, f := range append([]string{rel}, t.localImports(rel)...) {
		src, ok := t.files[f]
		if !ok {
			continue
		}
		if countAssignments(src, name) != 1 {
			continue
		}
		for _, m := range literalAssignRe.FindAllStringSubmatch(src, -1) {
			if m[1] == name {
				return m[2], true
			}
		}
	}
	return "", false
}

func countAssignments(src, name string) int {
	n := 0
	for _, m := range anyAssignRe.FindAllStringSubmatch(src, -1) {
		if m[1] == name {
			n++
		}
	}
	return n
}

// localImports maps `rel`'s import statements to files that exist in this tree.
// An import that resolves to no file in the tree is a library import (forge,
// json, a kcl plugin) and is skipped — membership in the tree IS the test, so
// there is no allowlist to fall out of date.
func (t *kclTree) localImports(rel string) []string {
	dir, _ := path2(rel)
	src := t.files[rel]
	var out []string
	for _, m := range importRe.FindAllStringSubmatch(src, -1) {
		for _, cand := range importCandidates(m[1], dir) {
			if _, ok := t.files[cand]; ok {
				out = append(out, cand)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// importCandidates maps a KCL module path to the file paths it could name,
// relative to deploy/kcl.
func importCandidates(mod, envDir string) []string {
	switch {
	case strings.HasPrefix(mod, ".."):
		// `..workloads` — up out of the env directory, i.e. deploy/kcl root.
		return []string{strings.TrimPrefix(mod, "..") + ".k",
			strings.ReplaceAll(strings.TrimPrefix(mod, ".."), ".", "/") + ".k"}
	case strings.HasPrefix(mod, "."):
		// `.config` — a sibling inside the env directory.
		base := strings.TrimPrefix(mod, ".")
		return []string{envDir + "/" + base + ".k"}
	default:
		// `lib.stack` / `config_gen`.
		return []string{strings.ReplaceAll(mod, ".", "/") + ".k", mod + ".k"}
	}
}

// workloadAlias is the name this file imports the project's workload
// DECLARATIONS under (`import ..workloads as wl`). It is read rather than
// assumed: a project is free to alias it anything, and a reader that hardcoded
// `wl.` would silently find no bindings in a project that spelled it otherwise.
func workloadAlias(src string) string {
	for _, m := range importRe.FindAllStringSubmatch(src, -1) {
		if workloadsModules[m[1]] {
			if m[2] != "" {
				return m[2]
			}
			return strings.TrimLeft(m[1], ".")
		}
	}
	return "wl"
}

// ─── runtime bindings ───────────────────────────────────────────────────────

// runtimeNames maps every name in `src` that denotes a runtime to the runtime
// it constructs: a variable bound directly to one (`_on_cp = forge.OnCluster {…}`)
// and a binder lambda whose body constructs one.
//
// Lambda bodies are READ, never rewritten. A migration that edits a lambda is
// how it corrupts a file it does not understand.
func runtimeNames(src string) map[string]string {
	out := map[string]string{}
	for _, m := range runtimeVarRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = m[2]
	}
	locs := lambdaAssignRe.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range locs {
		name := src[loc[2]:loc[3]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if m := runtimeRe.FindStringSubmatch(src[loc[0]:end]); m != nil {
			out[name] = m[1]
		}
	}
	return out
}

// bindingsIn maps each workload `src` binds to the runtime it is bound to.
//
// It recognises BOTH styles a project writes, which is the second half of the
// original defect — the first version matched only call syntax, so a project
// that binds with pipes recorded no bindings at all:
//
//	_on_cluster(wl.api)                       call
//	wl.api | {runtime = _on_cp}               pipe, runtime by name
//	wl.api | _host_only | {runtime = _host(…)} pipe chain, runtime from a binder
//	wl.api | {runtime = forge.OnCluster {…}}  pipe, runtime inline
func bindingsIn(src string) map[string]string {
	alias := workloadAlias(src)
	refRe := regexp.MustCompile(regexp.QuoteMeta(alias) + `\.([A-Za-z_][A-Za-z0-9_]*)`)
	names := runtimeNames(src)

	// An env-local REFINEMENT is a third shape, and it is the one hounders
	// writes: the env names the refined workload, then binds THAT name.
	//
	//	_membership = wl.membership | {env = … CORS_ORIGINS …}
	//	_workloads  = [_hosted(_membership)]
	//
	// Neither the call nor the pipe reader sees it, because the binder's
	// argument is `_membership`, not `wl.membership`. Recording the refinement
	// here resolves the binder application below onto the real workload —
	// without it, membership looks bound nowhere that pulls, so no registry is
	// placed on it, while the env's registry is still removed. Verified against
	// houndersclub's real pre-#322 prod/main.k.
	localAliasOf := map[string]string{}
	for _, m := range localRefinementRe.FindAllStringSubmatch(src, -1) {
		if m[2] == alias {
			localAliasOf[m[1]] = m[3]
		}
	}

	out := map[string]string{}
	locs := refRe.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range locs {
		workload := src[loc[2]:loc[3]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		window := src[loc[0]:end]

		// Call style: the binder sits immediately before the reference.
		if m := callBinderTailRe.FindStringSubmatch(src[:loc[0]]); m != nil {
			if rt, ok := names[m[1]]; ok {
				out[workload] = rt
				continue
			}
		}
		// Pipe style: the runtime is named inside this reference's window.
		if m := runtimeFieldRe.FindStringSubmatch(window); m != nil {
			expr := m[1]
			if strings.HasPrefix(expr, "forge.") {
				if rm := runtimeRe.FindStringSubmatch(expr); rm != nil {
					out[workload] = rm[1]
					continue
				}
			}
			if rt, ok := names[expr]; ok {
				out[workload] = rt
				continue
			}
		}
	}
	// Bindings of an env-local refinement resolve onto the workload it refines.
	for _, m := range localAliasBindingRe.FindAllStringSubmatch(src, -1) {
		binder, local := m[1], m[2]
		workload, isRefinement := localAliasOf[local]
		if !isRefinement {
			continue
		}
		if rt, ok := names[binder]; ok {
			out[workload] = rt
		}
	}
	return out
}

// ─── workload and frontend declarations ─────────────────────────────────────

// declaration is one `fw.Workload {…}` / `forge.Frontend {…}` block.
type declaration struct {
	// Ident is the KCL identifier the block is assigned to — what an env
	// writes after the module alias (`wl.<Ident>`).
	Ident string
	// Name is its declared `name = "…"`, which is what forge derives an
	// artifact name from when no image is declared.
	Name string
	// Image is the declared `image = "…"`, empty when the declaration has none.
	Image string
	// HasImageLine distinguishes "declares no image" from `image = ""`.
	HasImageLine bool
	// HasBuild reports whether forge builds an artifact for it. A declaration
	// with no build resolves no image and needs none.
	HasBuild bool
	// OutputName is the build's `output_name`, if it states one.
	OutputName string
	// BuildUnresolved reports that the declaration names a build this reader
	// could not read — a reference to something it could not find, or found
	// assigned more than once. The build's `output_name` is what forge derives
	// the image from, so an unread build means the artifact name is a guess,
	// and the caller turns that into a refusal rather than writing it.
	BuildUnresolved bool
	// Start and End bound the block in its source file.
	Start, End int
}

// artifact is the registry-less image name forge would resolve for this
// declaration, mirroring kcl/render.k's `_artifact`: a declared image wins,
// else the build's output_name, else the declared name.
//
// This is the third silent no-op the first version had. It read only a literal
// `image = "…"` line, so a workload that declares a build and NO image — the
// common shape, because forge derives the name — was invisible. Its env's
// registry was removed and moved nowhere, and the tree failed at render.
func (d declaration) artifact() (string, bool) {
	if d.Image != "" {
		return d.Image, true
	}
	if !d.HasBuild {
		return "", false // forge builds nothing for it: no image to complete
	}
	if d.OutputName != "" {
		return d.OutputName, true
	}
	if d.Name != "" {
		return d.Name, true
	}
	return "", false
}

// declarationsIn reads every declaration in src. buildOf resolves a `build = …`
// right-hand side to the build expression's text; it may be nil when no
// resolution is possible (the standalone parse used by tests and by the
// re-location pass, where the build was already judged).
func declarationsIn(src string, startRe *regexp.Regexp, buildOf func(expr string) (string, bool)) []declaration {
	var out []declaration
	locs := startRe.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := src[loc[0]:end]
		d := declaration{Ident: src[loc[2]:loc[3]], Start: loc[0], End: end}
		if m := nameLineRe.FindStringSubmatch(block); m != nil {
			d.Name = m[2]
		}
		if m := imageLineRe.FindStringSubmatch(block); m != nil {
			d.Image, d.HasImageLine = m[2], true
		}
		if m := buildFieldRe.FindStringSubmatch(block); m != nil {
			d.HasBuild = true
			d.OutputName, d.BuildUnresolved = readOutputName(strings.TrimSpace(m[1]), buildOf)
		}
		out = append(out, d)
	}
	return out
}

// readOutputName reads a build's `output_name` from a `build = <expr>` RHS,
// reporting whether the build could not be resolved at all.
//
// An unresolved build is NOT the same as one with no output_name: forge derives
// the image from output_name, so a build this reader could not read means the
// derived name would be a guess — and a wrong guess writes a reference to a
// repository the build never pushes to. The caller turns that into a refusal.
func readOutputName(expr string, buildOf func(string) (string, bool)) (name string, unresolved bool) {
	// Inline: `build = forge.GoBuild {… output_name = "x"}`.
	if om := outputNameRe.FindStringSubmatch(expr); om != nil {
		return om[1], false
	}
	// A reference to a build declared elsewhere — the shape a project writes
	// when several workloads share ONE build.
	if identifierRe.MatchString(expr) {
		if buildOf == nil {
			return "", true
		}
		body, ok := buildOf(expr)
		if !ok {
			return "", true
		}
		if om := outputNameRe.FindStringSubmatch(body); om != nil {
			return om[1], false
		}
		return "", false
	}
	// Something else that is neither inline nor a plain name.
	if !strings.Contains(expr, "{") {
		return "", true
	}
	return "", false
}

// hostedFrontendsIn returns every forge.Frontend declaration in src bound to
// forge.OnHosted — the one frontend runtime that publishes its build to a
// registry, and therefore the one that needs its own image reference.
//
// The runtime may be named inline (`runtime = forge.OnHosted {}`) or through a
// local variable the env declares once (`_hosted = forge.OnHosted {}`, then
// `runtime = _hosted`), which is what hounders writes; runtimeNames resolves
// the latter. A frontend on any other runtime publishes no registry artifact
// and is left alone.
func hostedFrontendsIn(src string) []declaration {
	names := runtimeNames(src)
	var out []declaration
	locs := frontendLiteralRe.FindAllStringIndex(src, -1)
	for i, loc := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := src[loc[0]:end]
		m := hostedFrontendRuntimeRe.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		if m[1] != "" && names[m[1]] != "OnHosted" {
			continue
		}
		d := declaration{Start: loc[0], End: end}
		if nm := nameLineRe.FindStringSubmatch(block); nm != nil {
			d.Name = nm[2]
			// A frontend literal has no identifier of its own when it is
			// declared inline, so its NAME is the handle — that is what the
			// re-location below matches on.
			d.Ident = nm[2]
		}
		if im := imageLineRe.FindStringSubmatch(block); im != nil {
			d.Image, d.HasImageLine = im[2], true
		}
		out = append(out, d)
	}
	return out
}

// findHostedFrontend re-locates a hosted frontend by its declared NAME in text
// that may have shifted since it was read.
func findHostedFrontend(src, name string) (declaration, bool) {
	for _, d := range hostedFrontendsIn(src) {
		if d.Name == name {
			return d, true
		}
	}
	return declaration{}, false
}

// resolveBuildExpr returns the assignment text for `name` in src, when src
// assigns it exactly once.
func resolveBuildExpr(src, name string) (string, bool) {
	if countAssignments(src, name) != 1 {
		return "", false
	}
	re := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(name) + `[ \t]*=[ \t]*([^\n]*)$`)
	m := re.FindStringSubmatchIndex(src)
	if m == nil {
		return "", false
	}
	return src[m[2]:m[3]], true
}

// ─── rewriting ──────────────────────────────────────────────────────────────

// setImage writes `image = "<image>"` into one declaration's block, replacing
// an existing image line or INSERTING one where the declaration has none.
func setImage(src string, d declaration, image string) string {
	block := src[d.Start:d.End]
	if d.HasImageLine {
		replaced := imageLineRe.ReplaceAllStringFunc(block, func(line string) string {
			m := imageLineRe.FindStringSubmatch(line)
			out := m[1] + "image = " + quote(image)
			if m[3] != "" {
				out += " " + m[3]
			}
			return out
		})
		return src[:d.Start] + replaced + src[d.End:]
	}
	// No image line: add one under the `name = …` line, at its indentation,
	// which is where an author reading the declaration expects to find it.
	if m := nameLineRe.FindStringSubmatchIndex(block); m != nil {
		indent := block[m[2]:m[3]]
		lineEnd := m[1]
		inserted := block[:lineEnd] + "\n" + indent + "image = " + quote(image) + block[lineEnd:]
		return src[:d.Start] + inserted + src[d.End:]
	}
	// No name line either: put it on the line after the opening brace.
	if brace := strings.Index(block, "{"); brace >= 0 {
		inserted := block[:brace+1] + "\n    image = " + quote(image) + block[brace+1:]
		return src[:d.Start] + inserted + src[d.End:]
	}
	return src
}

func quote(s string) string { return `"` + s + `"` }

// registryWholeLineRe is registryAssignRe plus its trailing newline, so
// removing the declaration takes the LINE rather than leaving a blank one
// behind. A blank line is cosmetic, but a migration that litters every env
// with them reads as damage in the diff a human is being asked to approve.
var registryWholeLineRe = regexp.MustCompile(`(?m)^[ \t]*registry[ \t]*=[ \t]*[^\n#]+?[ \t]*(?:#[^\n]*)?\n`)

// stripRegistry removes every `registry = …` declaration, whatever the RHS is,
// whether it sits on its own line or inline in a braced literal.
func stripRegistry(src string) string {
	out := registryWholeLineRe.ReplaceAllString(src, "")
	return stripOrganization(registryInlineRe.ReplaceAllString(out, ""))
}

// organizationWholeLineRe, organizationInlineRe: a declared
// `organization = "…"` on forge.ControlPlane. The field is gone — the org is
// the credential's — and ControlPlane is a closed schema, so a tree that still
// carries one fails to load. An own-line declaration takes its line (and the
// scaffold's `# REPLACE THIS…` comment block above it, which would otherwise be
// left describing nothing).
var (
	organizationCommentRe   = regexp.MustCompile(`(?m)^([ \t]*#[^\n]*\n)+([ \t]*organization[ \t]*=[ \t]*"[^"\n]*"[ \t]*\n)`)
	organizationWholeLineRe = regexp.MustCompile(`(?m)^[ \t]*organization[ \t]*=[ \t]*"[^"\n]*"[ \t]*(?:#[^\n]*)?\n`)
	organizationInlineRe    = regexp.MustCompile(`,[ \t]*organization[ \t]*=[ \t]*"[^"\n]*"|organization[ \t]*=[ \t]*"[^"\n]*"[ \t]*,[ \t]*|\{[ \t]*organization[ \t]*=[ \t]*"[^"\n]*"[ \t]*\}`)
)

// stripOrganization removes every declared control-plane organization.
func stripOrganization(src string) string {
	out := organizationCommentRe.ReplaceAllString(src, "")
	out = organizationWholeLineRe.ReplaceAllString(out, "")
	return organizationInlineRe.ReplaceAllStringFunc(out, func(m string) string {
		if strings.HasPrefix(m, "{") {
			return "{}"
		}
		return ""
	})
}

// hasRegistryHost mirrors kcl/lib/images.k: the first path segment is a
// registry host iff it contains a `.` or a `:`, or is exactly `localhost`.
func hasRegistryHost(image string) bool {
	slash := strings.Index(image, "/")
	if slash <= 0 {
		return false
	}
	head := image[:slash]
	return strings.ContainsAny(head, ".:") || head == "localhost"
}

func distinctValues(m map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range m {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kclIdent folds a workload name into the KCL identifier workloads.k declares
// it under (hyphens become underscores) — matching internal/naming.
func kclIdent(name string) string {
	return strings.NewReplacer("-", "_", ".", "_").Replace(name)
}
