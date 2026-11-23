//forge:exclude-contract: one-shot textual rewrite of a local deploy/kcl tree; its only methods are on result value types (ImageRegistryResult, Unaccounted, AmbiguousImage), it does no outbound I/O, and nothing substitutes it

// Package kclmigrate rewrites a project's deploy/kcl tree across a forge
// change that moved a declaration from one place to another.
//
// It works TEXTUALLY, on purpose. The obvious alternative — render the env and
// read the resolved values — is not available: the old tree no longer compiles
// against the new schema (the field being migrated is now a closed-schema
// error), so there is nothing to render. Working on text also means the rewrite
// preserves comments, ordering and formatting, which a render-and-reprint would
// destroy.
//
// THE GOVERNING RULE IS THAT THERE IS NO SILENT NO-OP. Every `registry = …` in
// the tree must be ACCOUNTED FOR — rewritten onto an image, dropped with a
// stated reason, or refused with its exact location. A registry the reader
// could not parse, resolve or attribute is a refusal, never a shrug. This is
// the rule the first version lacked: its two matchers missed the styles real
// projects write, so both inputs to its ambiguity check came back empty, it
// found nothing to do, and it reported success on a tree it had not understood.
// The tree then failed minutes later at render, pointing at a schema error with
// no trace of the migration that should have prevented it.
package kclmigrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ImageRegistryResult is what a migration run did or refused to do.
type ImageRegistryResult struct {
	// Rewrites is one entry per file changed, with a human description.
	Rewrites []string
	// Dropped names env registries that were removed WITHOUT being prefixed
	// onto any image, with the reason — a registry on an env where nothing
	// pulls an image says nothing about where an image should live.
	Dropped []string
	// Ambiguous is non-empty when the migration refused because a workload
	// pulls from two different registries. Each entry carries the exact
	// per-env KCL to write instead.
	Ambiguous []AmbiguousImage
	// Unaccounted is non-empty when the migration refused because it found a
	// `registry = …` it could not fully account for. Each entry names a
	// location and says what could not be determined.
	Unaccounted []Unaccounted
}

// Applied reports whether anything changed on disk.
func (r ImageRegistryResult) Applied() bool { return len(r.Rewrites) > 0 }

// Refused reports whether the migration declined to rewrite. Both refusal
// kinds count: a caller must not be able to treat "I could not read this" as
// less serious than "this is ambiguous", because both leave a tree that will
// not render.
func (r ImageRegistryResult) Refused() bool {
	return len(r.Ambiguous) > 0 || len(r.Unaccounted) > 0
}

// Unaccounted is one `registry = …` the migration could not fully handle,
// with the location and the reason.
//
// It exists so that "forge found something it does not understand" has a
// representation at all. Without one, the only way to express it was to return
// an empty success, which is what made the original defect invisible.
type Unaccounted struct {
	// File is the repo-relative path, e.g. "deploy/kcl/prod/main.k".
	File string
	// Line is the 1-based line of the `registry = …` declaration.
	Line int
	// Expr is the declaration's right-hand side, verbatim.
	Expr string
	// Reason says what could not be determined, in the author's terms.
	Reason string
}

func (u Unaccounted) String() string {
	return fmt.Sprintf("%s:%d: `registry = %s` — %s", u.File, u.Line, u.Expr, u.Reason)
}

// AmbiguousImage is one workload whose image cannot be completed
// unambiguously: it is bound to an image-pulling runtime in two or more envs
// that declared DIFFERENT registries, so there is no single reference that is
// correct everywhere.
type AmbiguousImage struct {
	// Workload is the declaration's name in deploy/kcl/workloads.k.
	Workload string
	// Image is the registry-less artifact it resolves today — its declared
	// image, or the name forge derives from its build when it declares none.
	Image string
	// Derived reports that Image was derived from the build rather than read
	// off an `image = …` line, so the runbook can say where it came from.
	Derived bool
	// ByEnv maps env name → the registry that env declared, for the envs
	// where this workload is bound to a runtime that pulls.
	ByEnv map[string]string
}

// Runbook is the exact KCL to write, per env, to resolve this workload by hand.
//
// It is per-env because that is what the situation actually is: the workload
// genuinely has a different reference in each env, so the declaration in
// workloads.k cannot state one, and each env refines it. forge prints this
// rather than guessing, because picking one registry silently would point some
// env at a repository its image was never pushed to — an ImagePullBackOff
// minutes after a deploy reported success.
func (a AmbiguousImage) Runbook() string {
	var b strings.Builder
	declares := fmt.Sprintf("declares the bare image %q", a.Image)
	if a.Derived {
		declares = fmt.Sprintf("declares no image (forge derives %q from its build)", a.Image)
	}
	fmt.Fprintf(&b, "workload %q %s and is bound to an image-pulling runtime in %d envs that declared different registries:\n",
		a.Workload, declares, len(a.ByEnv))
	for _, env := range sortedKeys(a.ByEnv) {
		fmt.Fprintf(&b, "    %-12s %s\n", env, a.ByEnv[env])
	}
	b.WriteString("\n  There is no single reference that is right in every env, so forge will not pick one.\n")
	b.WriteString("  Refine the image per env. In each deploy/kcl/<env>/main.k, alongside the binding:\n\n")
	for _, env := range sortedKeys(a.ByEnv) {
		fmt.Fprintf(&b, "    # deploy/kcl/%s/main.k\n", env)
		fmt.Fprintf(&b, "    %s | {image = %q}\n", "wl."+kclIdent(a.Workload), a.ByEnv[env]+"/"+a.Image)
	}
	b.WriteString("\n  Or, if one registry is the common case, declare THAT image in\n")
	b.WriteString("  deploy/kcl/workloads.k and state the whole image in the envs that differ:\n\n")
	b.WriteString("    # deploy/kcl/<the-odd-env>/main.k\n")
	b.WriteString("    " + "wl." + kclIdent(a.Workload) + " | {image = \"<that env's registry host>/" + a.Image + "\"}\n")
	return b.String()
}

// ImageRegistry migrates a project off env-level registries: it removes
// `registry = …` from every ClusterTarget / ControlPlane in deploy/kcl, and
// puts the removed value onto the workloads that pull an image — completing a
// bare `image`, or ADDING an `image` line to a workload that declared none —
// when, and only when, that is unambiguous.
//
// UNAMBIGUOUS means: across every env where this workload is bound to a runtime
// that PULLS an image, exactly one registry was declared. An env that binds the
// workload OnHost, OnCompose or BuildOnly, or does not bind it at all, is not
// consulted — its registry was never going to be this workload's.
//
// So a project whose dev env declares a localhost registry and binds everything
// to the host, while prod declares ghcr.io and deploys to a cluster, migrates
// cleanly to ghcr.io: dev's registry is DROPPED, because nothing in dev ever
// pulled an image.
//
// EVERYTHING ELSE IS REFUSED, and refusing writes nothing — a partial migration
// would leave the tree in a state neither the old nor the new forge
// understands. There are two refusal kinds and they are equally hard:
//
//   - Ambiguous: the same workload pulls from two different registries.
//     Choosing one silently would point an env at a repository its image was
//     never pushed to. The exact per-env KCL is printed instead.
//   - Unaccounted: a `registry = …` this reader could not resolve to a literal,
//     or whose env binds workloads in a way it could not read. Naming the file
//     and line is the whole point: the alternative is dropping a registry the
//     migration never understood, which is the defect this rule exists for.
//
// USER BINDER EXPRESSIONS ARE NEVER EDITED. Binders are lambdas, and rewriting
// a lambda body is how a migration corrupts a file it does not understand. This
// reads them to learn which runtime each produces, and writes only to
// `registry = …` and `image = …` lines.
func ImageRegistry(projectDir string, apply bool) (ImageRegistryResult, error) {
	var res ImageRegistryResult
	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	if _, err := os.Stat(kclDir); err != nil {
		return res, nil // no deploy tree: nothing to migrate
	}
	tree, err := loadTree(kclDir)
	if err != nil {
		return res, err
	}

	// Every `registry = …` anywhere in the tree, with the value resolved.
	// Scanning the WHOLE tree, not just env main.k files, is what makes the
	// accounting total: a registry declared in a shared library is still a
	// registry the new schema rejects.
	sites, unresolved := scanRegistrySites(tree)
	res.Unaccounted = append(res.Unaccounted, unresolved...)
	if len(sites) == 0 && len(res.Unaccounted) == 0 {
		return res, nil // already migrated, or never had one
	}

	// Which env each registry site belongs to, and what that env binds.
	envs := tree.envs()
	envRegistry, envSites, siteBad := attributeSitesToEnvs(sites, envs)
	res.Unaccounted = append(res.Unaccounted, siteBad...)

	// What each env binds, and to which runtime. An env's bindings are read
	// from its main.k AND from the library modules it imports, because a real
	// project's per-workload bindings live in a shared assembly lambda.
	bindings := map[string]map[string]string{} // env -> workload ident -> runtime
	for _, env := range envs {
		bindings[env] = envBindings(tree, env)
	}

	// The project's declarations, and the artifact name each resolves to.
	// The build resolver lets a declaration that shares ONE build
	// (`build = _cp_build`) be read for its `output_name` — which is what
	// forge derives the image from when the declaration states none.
	workloadsSrc := tree.files["workloads.k"]
	resolveBuild := func(expr string) (string, bool) { return resolveBuildExpr(workloadsSrc, expr) }
	decls := declarationsIn(workloadsSrc, workloadStartRe, resolveBuild)
	byIdent := map[string]declaration{}
	for _, d := range decls {
		byIdent[d.Ident] = d
	}

	// An env that declares a registry but whose bindings could not be read at
	// all is unaccounted: forge cannot say the registry is unused (drop it)
	// without having understood what that env runs.
	for env, reg := range envRegistry {
		if len(bindings[env]) > 0 {
			continue
		}
		s := envSites[env][0]
		res.Unaccounted = append(res.Unaccounted, Unaccounted{
			File: s.File, Line: s.Line, Expr: s.Expr,
			Reason: fmt.Sprintf("env %q declares registry %q, but forge could not read which workloads this env binds or to which runtimes, so it cannot tell whether any image needs that registry. Put the full reference on each workload's image by hand", env, reg),
		})
	}

	// Which envs pull each workload, and with what registry.
	pullRegistries := map[string]map[string]string{} // ident -> env -> registry
	for env, reg := range envRegistry {
		if reg == "" {
			continue
		}
		for ident, runtime := range bindings[env] {
			if !runtimesThatPull[runtime] {
				continue
			}
			if _, known := byIdent[ident]; !known {
				// The env binds something workloads.k does not declare. The
				// registry may belong on it, and forge cannot see it.
				s := envSites[env][0]
				res.Unaccounted = append(res.Unaccounted, Unaccounted{
					File: s.File, Line: s.Line, Expr: s.Expr,
					Reason: fmt.Sprintf("env %q binds %q to a %s runtime, but deploy/kcl/workloads.k declares no such workload, so forge cannot place this registry on its image", env, ident, runtime),
				})
				continue
			}
			if pullRegistries[ident] == nil {
				pullRegistries[ident] = map[string]string{}
			}
			pullRegistries[ident][env] = reg
		}
	}

	// Judge every pulled workload BEFORE writing anything.
	ambiguous, judgeBad := judgePulledWorkloads(pullRegistries, byIdent, envSites)
	res.Ambiguous = append(res.Ambiguous, ambiguous...)
	res.Unaccounted = append(res.Unaccounted, judgeBad...)

	// A hosted FRONTEND publishes its static build to a registry, so it needs
	// its own reference by exactly the same rule — and pre-#322 it declared
	// none, taking the env's ControlPlane.registry instead. Frontends are
	// declared in the env rather than in workloads.k, so they are read per env.
	//
	// Without this the hounders shape still fails after migration: the
	// ControlPlane registry is removed, the `web` frontend keeps no reference,
	// and prod refuses at render with "image is REQUIRED on forge.OnHosted".
	// The workloads were completed, so the registry counted as placed and
	// nothing reported a problem.
	frontendPlan, frontendBad := planHostedFrontends(tree, envRegistry, envSites)
	res.Unaccounted = append(res.Unaccounted, frontendBad...)

	if res.Refused() {
		sort.Slice(res.Ambiguous, func(i, j int) bool { return res.Ambiguous[i].Workload < res.Ambiguous[j].Workload })
		sort.Slice(res.Unaccounted, func(i, j int) bool {
			if res.Unaccounted[i].File != res.Unaccounted[j].File {
				return res.Unaccounted[i].File < res.Unaccounted[j].File
			}
			return res.Unaccounted[i].Line < res.Unaccounted[j].Line
		})
		res.Unaccounted = dedupeUnaccounted(res.Unaccounted)
		return res, nil
	}

	// Settled. Complete each pulled workload's image — writing an image line
	// where the declaration had none, which is the case forge derives.
	newWorkloads, completed, workloadRewrites := completeWorkloadImages(workloadsSrc, pullRegistries, byIdent, resolveBuild)
	res.Rewrites = append(res.Rewrites, workloadRewrites...)

	// Complete each hosted frontend's image, in its env's own main.k. Done
	// BEFORE the strip pass below, which rewrites the same files: both edits
	// have to land, and the strip pass reads whatever this leaves behind.
	changed, frontendDone, frontendRewrites := applyHostedFrontends(tree, frontendPlan)
	res.Rewrites = append(res.Rewrites, frontendRewrites...)

	// Remove every registry declaration, everywhere it appears. The platform
	// subtree that replaced it is composed from the organization the
	// credential acts for, so there is nothing to seed.
	for _, s := range sites {
		src, ok := changed[s.File]
		if !ok {
			src = tree.files[relOf(s.File)]
		}
		changed[s.File] = stripRegistry(src)
	}
	strip := stripReport{
		changed: changed, envs: envs, envRegistry: envRegistry,
		frontendDone: frontendDone, pullRegistries: pullRegistries,
		anyCompleted: len(completed) > 0,
	}
	rm, dropped, err := strip.apply(kclDir, apply)
	res.Rewrites = append(res.Rewrites, rm...)
	res.Dropped = append(res.Dropped, dropped...)
	if err != nil {
		return res, err
	}
	if apply && newWorkloads != workloadsSrc {
		if err := atomicfile.WriteFile(filepath.Join(kclDir, "workloads.k"), []byte(newWorkloads), 0o644); err != nil {
			return res, fmt.Errorf("write workloads.k: %w", err)
		}
	}
	sort.Strings(res.Rewrites)
	sort.Strings(res.Dropped)
	return res, nil
}

// frontendCompletion is one hosted frontend that will gain a reference.
type frontendCompletion struct {
	decl declaration
	name string
	full string
}

// judgePulledWorkloads decides, for every workload some env pulls an image for,
// whether its registry can be placed — WITHOUT writing anything, so a refusal
// stops the migration before any file is touched.
//
// Three outcomes: placeable (returned in neither slice, completed later),
// AMBIGUOUS (two envs pulling it declared different registries, so no single
// reference is right), and UNACCOUNTED (nothing to place the registry on, or a
// build whose output_name could not be read — where a derived name would be a
// guess, and a wrong guess writes a reference to a repository the build never
// pushes to).
func judgePulledWorkloads(pullRegistries map[string]map[string]string, byIdent map[string]declaration, envSites map[string][]siteRef) ([]AmbiguousImage, []Unaccounted) {
	var ambiguous []AmbiguousImage
	var bad []Unaccounted
	for _, ident := range sortedKeys(flatten(pullRegistries)) {
		byEnv := pullRegistries[ident]
		d := byIdent[ident]
		site := envSites[sortedKeys(byEnv)[0]][0]
		if d.BuildUnresolved {
			bad = append(bad, Unaccounted{
				File: site.File, Line: site.Line, Expr: site.Expr,
				Reason: fmt.Sprintf("workload %q declares no image and names a build forge could not read in deploy/kcl/workloads.k, so the image name it would derive is a guess. Declare its full image reference by hand", declName(d)),
			})
			continue
		}
		artifact, ok := d.artifact()
		if !ok {
			bad = append(bad, Unaccounted{
				File: site.File, Line: site.Line, Expr: site.Expr,
				Reason: fmt.Sprintf("workload %q is bound to a pulling runtime but declares neither an image nor a build forge can derive one from, so there is nothing to put this registry on. Declare its full image reference in deploy/kcl/workloads.k", d.Ident),
			})
			continue
		}
		if hasRegistryHost(artifact) {
			continue // already a complete reference: this registry is not its
		}
		if len(distinctValues(byEnv)) > 1 {
			ambiguous = append(ambiguous, AmbiguousImage{
				Workload: declName(d), Image: artifact, Derived: !d.HasImageLine, ByEnv: byEnv,
			})
		}
	}
	return ambiguous, bad
}

// attributeSitesToEnvs assigns each `registry = …` site to the env whose
// directory it sits in, and refuses the two cases it cannot attribute: an env
// declaring two different registries, and a site outside any env directory.
//
// Both are refusals rather than guesses for the same reason: the migration's
// only safe move is to put a registry onto the images of the env that declared
// it, and neither case identifies one env and one value.
func attributeSitesToEnvs(sites []siteRef, envs []string) (map[string]string, map[string][]siteRef, []Unaccounted) {
	envRegistry := map[string]string{} // env -> registry
	envSites := map[string][]siteRef{} // env -> its sites
	var bad []Unaccounted
	for _, s := range sites {
		env := envOfFile(s.File, envs)
		if env == "" {
			bad = append(bad, Unaccounted{
				File: s.File, Line: s.Line, Expr: s.Expr,
				Reason: "this registry is declared outside any deploy/kcl/<env>/ directory, so forge cannot tell which environment's workloads it applies to. Move the declaration into the env that uses it, or put the full reference on the workload's image",
			})
			continue
		}
		envSites[env] = append(envSites[env], s)
		if prev, ok := envRegistry[env]; ok && prev != s.Value {
			bad = append(bad, Unaccounted{
				File: s.File, Line: s.Line, Expr: s.Expr,
				Reason: fmt.Sprintf("env %q declares more than one registry (%q and %q); forge cannot tell which one each workload's image should carry", env, prev, s.Value),
			})
			continue
		}
		envRegistry[env] = s.Value
	}
	return envRegistry, envSites, bad
}

// stripReport is the registry-removal pass: which files changed, and what each
// env needed, so a removal can be reported either as MOVED or as DROPPED.
//
// The distinction is the safety property. "Dropped" means nothing in that env
// needed the registry; a removal reported that way when something DID need it
// is how a project ends up unrenderable with the value it needed gone.
type stripReport struct {
	changed        map[string]string
	envs           []string
	envRegistry    map[string]string
	frontendDone   map[string]bool
	pullRegistries map[string]map[string]string
	anyCompleted   bool
}

// apply writes each stripped file and returns one removal line per file, plus a
// Dropped line for every env whose registry went nowhere.
func (s stripReport) apply(kclDir string, write bool) (rewrites, dropped []string, err error) {
	for _, file := range sortedKeys(s.changed) {
		env := envOfFile(file, s.envs)
		rewrites = append(rewrites, fmt.Sprintf("%s: removed `registry = %q`", file, s.envRegistry[env]))
		if !s.frontendDone[env] && (!s.anyCompleted || !envPulled(s.pullRegistries, env)) {
			dropped = append(dropped, fmt.Sprintf(
				"%s: `registry = %q` dropped — no workload this env binds to a cluster or hosted runtime needed it",
				file, s.envRegistry[env]))
		}
		if !write {
			continue
		}
		if err := atomicfile.WriteFile(filepath.Join(kclDir, relOf(file)), []byte(s.changed[file]), 0o644); err != nil {
			return rewrites, dropped, fmt.Errorf("write %s: %w", file, err)
		}
	}
	return rewrites, dropped, nil
}

// completeWorkloadImages writes the settled reference onto each workload that
// pulls one, returning the new workloads.k text, the idents completed, and one
// rewrite line each.
//
// A declaration with NO image line GAINS one: that is the shape forge derives
// the artifact for, and the case the migration originally did nothing about.
func completeWorkloadImages(src string, pullRegistries map[string]map[string]string, byIdent map[string]declaration, resolveBuild func(string) (string, bool)) (out string, completed []string, rewrites []string) {
	out = src
	for _, ident := range sortedKeys(flatten(pullRegistries)) {
		regs := distinctValues(pullRegistries[ident])
		if len(regs) != 1 {
			continue
		}
		d := byIdent[ident]
		artifact, ok := d.artifact()
		if !ok || hasRegistryHost(artifact) {
			continue
		}
		full := regs[0] + "/" + artifact
		// Re-find the declaration in the CURRENT text: an earlier insertion
		// shifted every later block's offsets.
		cur, found := findDeclaration(out, workloadStartRe, ident, resolveBuild)
		if !found {
			continue
		}
		out = setImage(out, cur, full)
		how := fmt.Sprintf("image %q → %q", artifact, full)
		if !d.HasImageLine {
			how = fmt.Sprintf("added image = %q (derived from its build)", full)
		}
		completed = append(completed, ident)
		rewrites = append(rewrites, fmt.Sprintf("deploy/kcl/workloads.k: workload %q %s", declName(d), how))
	}
	return out, completed, rewrites
}

// applyHostedFrontends writes each planned frontend reference into its env's
// own main.k, returning the modified sources keyed by repo-relative path, which
// envs gained a reference, and one rewrite line each.
//
// The returned map SEEDS the registry-strip pass, which rewrites the same
// files: both edits have to land, so the strip pass must read what this left
// behind rather than the original text.
func applyHostedFrontends(tree *kclTree, plan map[string][]frontendCompletion) (changed map[string]string, done map[string]bool, rewrites []string) {
	changed, done = map[string]string{}, map[string]bool{}
	envs := make([]string, 0, len(plan))
	for env := range plan {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		rel := env + "/main.k"
		src := tree.files[rel]
		for _, fc := range plan[env] {
			// Re-find by NAME in the current text: an earlier insertion shifted
			// every later block's offsets.
			cur, found := findHostedFrontend(src, fc.name)
			if !found {
				continue
			}
			src = setImage(src, cur, fc.full)
			done[env] = true
			rewrites = append(rewrites, fmt.Sprintf(
				"deploy/kcl/%s/main.k: hosted frontend %q added image = %q", env, fc.name, fc.full))
		}
		changed["deploy/kcl/"+rel] = src
	}
	return changed, done, rewrites
}

// planHostedFrontends works out which hosted frontends need a reference, and
// what it should be, WITHOUT writing anything — so a refusal found here stops
// the migration before any file is touched.
//
// A frontend bound to forge.OnHosted publishes its static build to a registry,
// so it needs its own reference by exactly the same rule as a workload, with
// the same field name. Pre-#322 it declared none and took the env's
// ControlPlane.registry instead.
//
// Without this the hounders shape still fails after migration: the ControlPlane
// registry is removed, the `web` frontend keeps no reference, and prod refuses
// at render with "image is REQUIRED on forge.OnHosted". The workloads were
// completed, so the registry counted as placed and nothing reported a problem.
func planHostedFrontends(tree *kclTree, envRegistry map[string]string, envSites map[string][]siteRef) (map[string][]frontendCompletion, []Unaccounted) {
	plan := map[string][]frontendCompletion{}
	var bad []Unaccounted
	for _, env := range sortedKeys(envRegistry) {
		reg := envRegistry[env]
		if reg == "" {
			continue
		}
		src, ok := tree.files[env+"/main.k"]
		if !ok {
			continue
		}
		for _, f := range hostedFrontendsIn(src) {
			if f.Image != "" && hasRegistryHost(f.Image) {
				continue // already a complete reference
			}
			name := declName(f)
			if name == "" {
				s := envSites[env][0]
				bad = append(bad, Unaccounted{
					File: s.File, Line: s.Line, Expr: s.Expr,
					Reason: fmt.Sprintf("env %q binds a frontend to forge.OnHosted, but forge could not read its name, so it cannot derive the reference to publish it under. Declare the frontend's full image reference by hand", env),
				})
				continue
			}
			plan[env] = append(plan[env], frontendCompletion{decl: f, full: reg + "/" + name, name: name})
		}
	}
	return plan, bad
}

// declName is the workload's declared name, falling back to its identifier.
func declName(d declaration) string {
	if d.Name != "" {
		return d.Name
	}
	return d.Ident
}

// siteRef is one `registry = …` declaration, located and resolved.
type siteRef struct {
	File  string // repo-relative
	Line  int
	Expr  string // the RHS, verbatim
	Value string // the resolved literal
}

// scanRegistrySites finds every `registry = …` in the tree and resolves each
// one's value, returning the resolved sites and an Unaccounted entry for every
// one it could not resolve.
//
// Resolution follows a single assignment through a variable, which is the case
// the original matcher missed: a project declares `_registry = "…"` once at the
// top of the file — because `forge build` and `forge registry login` read that
// one line — and writes `registry = _registry` on the target. Matching only a
// literal saw no registry at all in such a file.
func scanRegistrySites(t *kclTree) ([]siteRef, []Unaccounted) {
	var sites []siteRef
	var bad []Unaccounted
	for _, rel := range sortedKeys(t.files) {
		src := t.files[rel]
		for _, loc := range registryAssignRe.FindAllStringSubmatchIndex(src, -1) {
			expr := strings.TrimSpace(src[loc[2]:loc[3]])
			site := siteRef{File: repoPath(rel), Line: lineOf(src, loc[0]), Expr: expr}
			if m := stringLiteralRe.FindStringSubmatch(expr); m != nil {
				site.Value = m[1]
				sites = append(sites, site)
				continue
			}
			if identifierRe.MatchString(expr) {
				if v, ok := t.resolveLiteral(rel, expr); ok {
					site.Value = v
					sites = append(sites, site)
					continue
				}
				bad = append(bad, Unaccounted{
					File: site.File, Line: site.Line, Expr: expr,
					Reason: fmt.Sprintf("%s is not assigned exactly one string literal in this file or the files it imports, so forge cannot tell which registry it names", expr),
				})
				continue
			}
			bad = append(bad, Unaccounted{
				File: site.File, Line: site.Line, Expr: expr,
				Reason: "the value is computed, not a literal, so forge cannot resolve it to one registry. Put the full reference on each workload's image by hand",
			})
		}
	}
	return sites, bad
}

// envBindings reads what an env binds, from its own main.k and from every
// local module it imports (transitively).
//
// The transitive walk is what makes this correct on a project that factors its
// assembly into a shared library: reading only main.k finds no bindings, and
// "no bindings" is indistinguishable from "this env pulls nothing", which is
// how a registry gets silently dropped.
func envBindings(t *kclTree, env string) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{}
	var walk func(rel string)
	walk = func(rel string) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		src, ok := t.files[rel]
		if !ok {
			return
		}
		for workload, runtime := range bindingsIn(src) {
			// An env's own main.k wins over a library's default: the env is
			// the more specific statement about what IT runs.
			if _, have := out[workload]; !have || rel == env+"/main.k" {
				out[workload] = runtime
			}
		}
		for _, imp := range t.localImports(rel) {
			walk(imp)
		}
	}
	walk(env + "/main.k")
	return out
}

// envOfFile maps a repo-relative path back to the env directory it sits in.
func envOfFile(file string, envs []string) string {
	for _, env := range envs {
		if strings.HasPrefix(file, "deploy/kcl/"+env+"/") {
			return env
		}
	}
	return ""
}

func relOf(repoRelative string) string {
	return strings.TrimPrefix(repoRelative, "deploy/kcl/")
}

// findDeclaration re-locates a declaration in text that may have shifted.
func findDeclaration(src string, startRe *regexp.Regexp, ident string, buildOf func(string) (string, bool)) (declaration, bool) {
	for _, d := range declarationsIn(src, startRe, buildOf) {
		if d.Ident == ident {
			return d, true
		}
	}
	return declaration{}, false
}

func envPulled(pull map[string]map[string]string, env string) bool {
	for _, byEnv := range pull {
		if _, ok := byEnv[env]; ok {
			return true
		}
	}
	return false
}

func flatten(m map[string]map[string]string) map[string]string {
	out := map[string]string{}
	for k := range m {
		out[k] = ""
	}
	return out
}

func dedupeUnaccounted(in []Unaccounted) []Unaccounted {
	seen := map[string]bool{}
	var out []Unaccounted
	for _, u := range in {
		key := u.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, u)
	}
	return out
}
