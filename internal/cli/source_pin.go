package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// THE RELEASE SOURCE-COMMIT GUARD.
//
// A ShellBuild whose cwd is a SIBLING checkout (`cwd = "../reliant"`) builds
// whatever that checkout has on disk. The project meanwhile PINS the sibling —
// a go.mod require of its module, a forge.GitSource ref for its repository —
// and a release is read as "the project at this commit", pins included. When
// the checkout is not at its pin, the release records bytes the pin does not
// describe. Prod release 20261007.165315 shipped a days-old reliant image
// exactly so: a deploy built from whatever ../reliant had checked out.
//
// So a release refuses to cut when an external ShellBuild checkout is
//
//   - not at the commit the project pins it to, or
//   - dirty, which puts its bytes in no commit at all.
//
// The project's OWN checkout is not judged here. Its commit IS the release's,
// and release provenance already records it, dirty flag and tree hash
// included.
//
// One rule, applied twice: before the build, against the checkouts as they are
// (so a refused release builds and pushes nothing under its version), and at
// the cut, against the checkouts the build state RECORDS — the only evidence a
// `--no-build` cut has, and the catch for a checkout that moved mid-build.

// sourcePin is one declaration of the commit a sibling checkout must be at.
type sourcePin struct {
	// declared names the declaration, as the refusal prints it.
	declared string
	// commit is the pinned commit: full hex, or the 12-hex prefix a Go
	// pseudo-version carries. Empty when unresolved.
	commit string
	// unresolved says why commit is empty.
	unresolved string
}

// admits reports whether head is the pinned commit.
func (p sourcePin) admits(head string) bool {
	return p.commit != "" && strings.HasPrefix(head, p.commit)
}

// sourceUse is one checkout, as one or more ShellBuilds ran (or will run) in
// it.
type sourceUse struct {
	src      *buildtarget.Source
	services []string
}

// releaseSourceCheck is what a set of checkouts is judged against: the
// project's go.mod and the env's GitSource declarations.
type releaseSourceCheck struct {
	projectDir   string
	env          string
	version      string
	buildSources []sourceDeclaration
	// recorded is true at the cut, where the checkouts judged are the ones
	// the build state RECORDS rather than the ones on disk now. The refusal
	// then says "built from", and its remedy is a rebuild: resetting the
	// checkout does not change images already built.
	recorded bool
	// resolver is built only when a GitSource names its pin by something
	// other than a commit, so the common case touches no source cache.
	resolver func() (pinResolver, error)
}

func newReleaseSourceCheck(projectDir, env, version string, recorded bool, entities *KCLEntities) releaseSourceCheck {
	return releaseSourceCheck{
		projectDir:   projectDir,
		env:          env,
		version:      version,
		buildSources: buildSourceDeclarations(nil, entities),
		recorded:     recorded,
		resolver: func() (pinResolver, error) {
			r, err := frontendSourceResolver(projectDir)
			if err != nil {
				return nil, err
			}
			return r, nil
		},
	}
}

// preflightReleaseSources is the guard before a release-bound build: every
// external ShellBuild checkout, as it is now. A no-op unless the build's
// output is sealed under a release version (--release). A no-version deploy
// that reuses an existing release builds nothing, so it never reaches here.
func preflightReleaseSources(ctx context.Context, projectDir string, entities *KCLEntities, opts buildOptions) error {
	version := opts.release
	if version == "" || entities == nil {
		return nil
	}
	uses, err := liveShellSources(ctx, projectDir, entities)
	if err != nil {
		return fmt.Errorf("--release %s: %w", version, err)
	}
	_, err = newReleaseSourceCheck(projectDir, opts.env, version, false, entities).judge(ctx, uses)
	return err
}

// checkRecordedReleaseSources is the guard at the cut: the checkouts the
// env's build state records for each ShellBuild the env declares. A sibling
// nothing pins is not refused — there is no commit to compare with — but the
// cut says so, because the release then records a commit it could not check.
func checkRecordedReleaseSources(ctx context.Context, projectDir, env, version string, entities *KCLEntities) error {
	check := newReleaseSourceCheck(projectDir, env, version, true, entities)
	uses, unrecorded := recordedShellSources(projectDir, env, entities)
	notes, err := check.judge(ctx, uses)
	for _, svc := range unrecorded {
		notes = append(notes, fmt.Sprintf("the build state of %s records no source checkout (an older forge built it), so its commit was not checked against the project's pins — "+
			"rebuild it to check: forge env build %s --release %s", svc, envNameOr(env), version))
	}
	for _, n := range notes {
		fmt.Printf("[build]   note: %s\n", n)
	}
	return err
}

// liveShellSources captures the checkout each ShellBuild in entities runs in.
// A cwd that does not exist is skipped: the build refuses it on its own, with
// the missing path, and a second refusal here would only repeat it.
//
// Captured once per cwd: services routinely share one (three build from
// ../reliant), and a `git status` over a large checkout is not free.
func liveShellSources(ctx context.Context, projectDir string, entities *KCLEntities) ([]sourceUse, error) {
	var uses []sourceUse
	captured := map[string]*buildtarget.Source{}
	for _, svc := range externalBuildServices(entities) {
		cwd, err := buildtarget.ResolveCwd(buildtarget.Spec{Service: svc.Name, BuildCwd: svc.EffectiveBuildCwd(), ProjectDir: projectDir})
		if err != nil {
			continue
		}
		src, seen := captured[cwd]
		if !seen {
			if src, err = buildtarget.CaptureSource(ctx, cwd, projectDir); err != nil {
				return nil, err
			}
			captured[cwd] = src
		}
		uses = addSourceUse(uses, src, svc.Name)
	}
	return uses, nil
}

// recordedShellSources reads the checkout each declared ShellBuild's state
// recorded. Only services the env DECLARES are read, so the state a removed
// service left behind cannot refuse a release that no longer builds it.
//
// unrecorded names the services with a cwd outside the project root whose
// state carries no checkout at all — written by a forge that predates the
// record. There is no evidence to judge, so they are noted rather than
// refused.
func recordedShellSources(projectDir, env string, entities *KCLEntities) (uses []sourceUse, unrecorded []string) {
	for _, svc := range externalBuildServices(entities) {
		st, err := buildtarget.ReadState(projectDir, env, svc.Name)
		if err != nil || st == nil || !buildtarget.StateBelongsTo(st, env, svc.Name) {
			continue
		}
		if st.Source == nil {
			if cwdOutsideProject(projectDir, svc.EffectiveBuildCwd()) {
				unrecorded = append(unrecorded, svc.Name)
			}
			continue
		}
		uses = addSourceUse(uses, st.Source, svc.Name)
	}
	return uses, unrecorded
}

// cwdOutsideProject reports whether a ShellBuild cwd (relative to projectDir,
// or absolute) leaves the project tree — the shape of a sibling checkout.
func cwdOutsideProject(projectDir, cwd string) bool {
	if cwd == "" {
		return false
	}
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(projectDir, cwd)
	}
	rel, err := filepath.Rel(projectDir, cwd)
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// addSourceUse files service under its checkout. Two services in one
// checkout at one commit are one use: the refusal is about the checkout, and
// listing it once per service reads like several problems.
func addSourceUse(uses []sourceUse, src *buildtarget.Source, service string) []sourceUse {
	if src == nil {
		return uses
	}
	for i := range uses {
		u := uses[i].src
		if u.Dir == src.Dir && u.Commit == src.Commit && u.Dirty == src.Dirty {
			uses[i].services = append(uses[i].services, service)
			return uses
		}
	}
	return append(uses, sourceUse{src: src, services: []string{service}})
}

// judge applies the rule to every external checkout in uses. It returns a
// note for each checkout nothing pins, and an error carrying one runbook
// block per refused checkout.
func (c releaseSourceCheck) judge(ctx context.Context, uses []sourceUse) ([]string, error) {
	sort.SliceStable(uses, func(i, j int) bool { return uses[i].src.Dir < uses[j].src.Dir })
	var notes, refusals []string
	for _, u := range uses {
		if !u.src.External() {
			continue
		}
		pins, err := c.pinsFor(ctx, u.src)
		if err != nil {
			return notes, fmt.Errorf("--release %s: %w", c.version, err)
		}
		if block := sourceRefusal(u, pins, c.recorded); block != "" {
			refusals = append(refusals, block)
			continue
		}
		if len(pins) == 0 {
			notes = append(notes, unpinnedSourceNote(u))
		}
	}
	if len(refusals) == 0 {
		return notes, nil
	}
	// Before the build, the remedy is to fix the checkout and run the same
	// command again. At the cut the images are already built, from the wrong
	// checkout, so the remedy is a rebuild — re-running a --no-build cut
	// would judge the same recorded build and refuse again.
	what, then := "a ShellBuild runs in", "Nothing was built or pushed."
	closing := "Then re-run this command."
	if c.recorded {
		what, then = "a ShellBuild was built in", "The release was not recorded."
		closing = fmt.Sprintf("Then rebuild from the pinned checkout and cut: forge env build %s --release %s", envNameOr(c.env), c.version)
	}
	return notes, fmt.Errorf("--release %s: refusing to cut — %s a checkout that is not the commit this project pins.\n"+
		"  A release names exact source, so a sibling checkout must be AT its pin (the go.mod require of its module,\n"+
		"  or the forge.GitSource ref for its repository) with no uncommitted changes. %s\n\n%s\n\n  %s",
		c.version, what, then, strings.Join(refusals, "\n\n"), closing)
}

// pinsFor collects every declaration that pins src: the project's go.mod
// require of the module src declares, and each GitSource naming src's
// repository.
func (c releaseSourceCheck) pinsFor(ctx context.Context, src *buildtarget.Source) ([]sourcePin, error) {
	var pins []sourcePin
	if p, ok := goModPin(ctx, c.projectDir, src); ok {
		pins = append(pins, p)
	}
	gs, err := c.gitSourcePins(ctx, src)
	if err != nil {
		return nil, err
	}
	return append(pins, gs...), nil
}

// goModPin is the project go.mod's pin on the module src declares, after any
// replace. ok is false when go.mod does not require it, or replaces it with a
// local directory — Go then builds whatever that directory holds, so there is
// no commit to hold the checkout to.
func goModPin(ctx context.Context, projectDir string, src *buildtarget.Source) (sourcePin, bool) {
	if src.Module == "" {
		return sourcePin{}, false
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return sourcePin{}, false
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return sourcePin{}, false
	}
	version := ""
	for _, r := range f.Require {
		if r != nil && r.Mod.Path == src.Module {
			version = r.Mod.Version
		}
	}
	if version == "" {
		return sourcePin{}, false
	}
	declared := fmt.Sprintf("go.mod: require %s %s", src.Module, version)
	for _, rep := range f.Replace {
		if rep == nil || rep.Old.Path != src.Module || (rep.Old.Version != "" && rep.Old.Version != version) {
			continue
		}
		if modfile.IsDirectoryPath(rep.New.Path) {
			return sourcePin{}, false
		}
		version = rep.New.Version
		declared = fmt.Sprintf("go.mod: replace %s => %s %s", src.Module, rep.New.Path, rep.New.Version)
	}
	return goVersionPin(ctx, src, version, declared), true
}

// goVersionPin reduces a module version to the commit it names. A
// pseudo-version carries the commit itself (its 12-hex suffix); a release
// version names a TAG, resolved in the checkout — and a tag the checkout has
// not fetched is reported as such, never compared as though it were a commit.
func goVersionPin(ctx context.Context, src *buildtarget.Source, version, declared string) sourcePin {
	if module.IsPseudoVersion(version) {
		if rev, err := module.PseudoVersionRev(version); err == nil {
			return sourcePin{declared: declared, commit: rev}
		}
	}
	tag := moduleTagPrefix(src) + strings.TrimSuffix(version, "+incompatible")
	commit, err := src.ResolveCommit(ctx, "refs/tags/"+tag)
	if err != nil {
		return sourcePin{declared: declared, unresolved: fmt.Sprintf("tag %s is not in this checkout", tag)}
	}
	return sourcePin{declared: declared, commit: commit}
}

// moduleTagPrefix is the prefix Go puts on a module's version tags: its
// directory within the repository, minus a major-version subdirectory
// (example.com/r/sub/v2 in sub/v2 tags "sub/v2.0.0").
func moduleTagPrefix(src *buildtarget.Source) string {
	dir := src.ModuleDir
	if dir == "" || dir == "." {
		return ""
	}
	if _, major, ok := module.SplitPathVersion(src.Module); ok && major != "" {
		m := strings.TrimPrefix(major, "/")
		if dir == m {
			return ""
		}
		dir = strings.TrimSuffix(dir, "/"+m)
	}
	return dir + "/"
}

// gitSourcePins is every GitSource in the env that names src's repository,
// each reduced to the commit its ref resolves to. A ref that IS a commit
// resolves to itself; anything else goes through the same resolver the
// release uses to record the frontend, so the guard and the ledger cannot
// disagree about what the ref means. A local source override is skipped: it
// is not a pin, and the cut refuses one on its own.
func (c releaseSourceCheck) gitSourcePins(ctx context.Context, src *buildtarget.Source) ([]sourcePin, error) {
	if src.Repo == "" {
		return nil, nil
	}
	var pins []sourcePin
	var resolver pinResolver
	for _, d := range c.buildSources {
		if d.source == nil || d.source.Repo == "" || release.CanonicalRepo(d.source.Repo) != src.Repo {
			continue
		}
		declared := fmt.Sprintf("deploy/kcl: %s GitSource ref %s", d.name, d.source.Ref)
		if release.ValidObjectID(d.source.Ref) {
			pins = append(pins, sourcePin{declared: declared, commit: d.source.Ref})
			continue
		}
		if resolver == nil {
			r, err := c.resolver()
			if err != nil {
				return nil, err
			}
			resolver = r
		}
		res, err := resolver.Resolve(ctx, toGitSource(d.source))
		switch {
		case err != nil:
			pins = append(pins, sourcePin{declared: declared, unresolved: fmt.Sprintf("the ref does not resolve: %v", err)})
		case res.Overridden:
			continue
		case res.Commit == "":
			pins = append(pins, sourcePin{declared: declared, unresolved: "the ref resolved to no commit"})
		default:
			pins = append(pins, sourcePin{declared: declared, commit: res.Commit})
		}
	}
	return pins, nil
}

// sourceRefusal is the runbook block for one checkout, or "" when the checkout
// passes: clean, and admitted by every pin. recorded labels the checkout as
// the one the build state recorded rather than the one on disk.
func sourceRefusal(u sourceUse, pins []sourcePin, recorded bool) string {
	src := u.src
	var missed []sourcePin
	unresolved := false
	for _, p := range pins {
		if !p.admits(src.Commit) {
			missed = append(missed, p)
		}
		unresolved = unresolved || p.unresolved != ""
	}
	if !src.Dirty && len(missed) == 0 {
		return ""
	}
	dir := gitDirArg(src.Dir)
	head := shortSHA(src.Commit)
	label := "checkout:  "
	if recorded {
		label = "built from:"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  %s (%s) — the cwd of %s\n", dir, describeRepo(src), strings.Join(u.services, ", "))
	if src.Dirty {
		fmt.Fprintf(&b, "    %s %s, WITH UNCOMMITTED CHANGES (see: git -C %s status --short)\n", label, head, dir)
	} else {
		fmt.Fprintf(&b, "    %s %s\n", label, head)
	}
	for _, p := range pins {
		if p.unresolved != "" {
			fmt.Fprintf(&b, "    pinned by:  %s — %s\n", p.declared, p.unresolved)
			continue
		}
		fmt.Fprintf(&b, "    pinned by:  %s → %s\n", p.declared, shortSHA(p.commit))
	}
	target, agreed := agreedPin(pins)
	if len(missed) > 0 && agreed && target != "" {
		verb := "runs"
		if recorded {
			verb = "ran"
		}
		fmt.Fprintf(&b, "    The ShellBuild %s in this checkout, so the release would ship %s while the project pins %s.\n", verb, head, shortSHA(target))
	}
	if src.Dirty {
		b.WriteString("    A release must be rebuildable from a commit, and uncommitted changes are in none.\n")
	}

	var fixes []string
	if src.Dirty {
		fixes = append(fixes, fmt.Sprintf("git -C %s stash push --include-untracked    # `git -C %s stash pop` brings the changes back", dir, dir))
	}
	switch {
	case unresolved:
		fixes = append(fixes, fmt.Sprintf("git -C %s fetch --tags origin    # then re-run; the pin resolves once the checkout has it", dir))
	case !agreed:
		fixes = append(fixes, "the pins name different commits: move them to ONE commit (the go.mod require and every GitSource ref for this repository move together), then check it out")
	case len(missed) > 0 && target != "":
		fixes = append(fixes, fmt.Sprintf("git -C %s fetch origin && git -C %s checkout --detach %s", dir, dir, shortSHA(target)))
	}
	for i, f := range fixes {
		lead := "    fix:        "
		if i > 0 {
			lead = "                "
		}
		b.WriteString(lead + f + "\n")
	}
	if len(missed) > 0 && agreed && target != "" && !unresolved {
		fmt.Fprintf(&b, "    (if the PIN is the side that is behind, move it to %s instead — the go.mod require and any GitSource ref for this repository together)", head)
	}
	return strings.TrimRight(b.String(), "\n")
}

// agreedPin is the one commit every resolved pin names. ok is false when the
// resolved pins disagree; a pseudo-version's 12-hex prefix and the full commit
// it abbreviates agree. With no resolved pin, ok is true and target "".
func agreedPin(pins []sourcePin) (target string, ok bool) {
	for _, p := range pins {
		if p.commit == "" {
			continue
		}
		switch {
		case target == "", strings.HasPrefix(p.commit, target):
			if len(p.commit) > len(target) {
				target = p.commit
			}
		case strings.HasPrefix(target, p.commit):
		default:
			return "", false
		}
	}
	return target, true
}

// unpinnedSourceNote says that a sibling checkout was released at a commit
// nothing in the project pins.
func unpinnedSourceNote(u sourceUse) string {
	what := "its repository"
	if u.src.Module != "" {
		what = "its module " + u.src.Module
	}
	return fmt.Sprintf("%s (%s, the cwd of %s) is at %s, and nothing in this project pins %s — no go.mod require, no forge.GitSource. "+
		"The release records that commit but cannot check it",
		gitDirArg(u.src.Dir), describeRepo(u.src), strings.Join(u.services, ", "), shortSHA(u.src.Commit), what)
}

func describeRepo(src *buildtarget.Source) string {
	if src.Repo != "" {
		return src.Repo
	}
	return "no origin remote"
}

// gitDirArg is how a fix command names dir: relative to where forge was run
// when that is shorter, so `git -C ../reliant …` can be pasted as printed.
//
// Forward slashes on every OS. git accepts them on Windows, and in every
// shell there, whereas `..\reliant` needs quoting in a POSIX shell and the
// quoting that works in one Windows shell does not work in another.
func gitDirArg(dir string) string {
	out := dir
	if wd, err := os.Getwd(); err == nil {
		if real, rerr := filepath.EvalSymlinks(wd); rerr == nil {
			wd = real
		}
		if rel, rerr := filepath.Rel(wd, dir); rerr == nil && len(rel) < len(dir) {
			out = rel
		}
	}
	out = filepath.ToSlash(out)
	if strings.ContainsAny(out, " \t'\"$`\\") {
		return "'" + strings.ReplaceAll(out, "'", `'\''`) + "'"
	}
	return out
}

// captureShellSource is buildtarget.CaptureSource for one ShellBuild about to
// run. Best-effort: a build is not a release, so a checkout git cannot read
// warns and the build goes on. A RELEASE build has already captured every
// checkout in preflightReleaseSources, which refuses on the same error.
func captureShellSource(ctx context.Context, service, cwd, projectDir string) *buildtarget.Source {
	src, err := buildtarget.CaptureSource(ctx, cwd, projectDir)
	if err != nil {
		fmt.Printf("[build] %s: warning: could not record the source checkout: %v\n", service, err)
	}
	return src
}

// describeShellSource is the build log's note of the checkout a ShellBuild
// runs in: "" for the project's own, so the common case prints as before.
func describeShellSource(src *buildtarget.Source) string {
	if !src.External() {
		return ""
	}
	dirty := ""
	if src.Dirty {
		dirty = ", dirty"
	}
	return fmt.Sprintf(" from %s @ %s%s", gitDirArg(src.Dir), shortSHA(src.Commit), dirty)
}
