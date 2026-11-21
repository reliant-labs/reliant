// build_external_release_reuse.go lets `forge env build <env> --release` reuse
// an earlier release's image for a ShellBuild whose declared inputs did not
// change (forge#2326).
//
// Every release used to run every ShellBuild. A build that is not
// byte-for-byte reproducible (apt mirrors, `go install`, a remote builder)
// then pushed a new digest for unchanged inputs, and every workload pinned to
// that digest restarted although nothing it runs had changed.
//
// The rule, for a ShellBuild that declares `inputs`:
//
//  1. Fingerprint the inputs the way a release compares them
//     (releaseInputsFingerprint): every selected file's content plus the
//     step's own declaration — command, cwd, env, built_images, platform,
//     destination repository. The release TAG is kept out STRUCTURALLY: the
//     command and env are fingerprinted as a second render composed them
//     with image_tag bound to a sentinel (renderReleaseTemplates), so a
//     literal that merely equals the tag still counts, and forge proves the
//     template plus the tag reproduces exactly what runs. The cwd is keyed
//     relative to the project; nothing else is masked.
//  2. Look for that fingerprint on the same image in the release the env runs
//     now, then in the newest release of the project (releaseReuseBase). An
//     older release, or a record without the fingerprint, means rebuild.
//  3. Confirm the registry still serves that digest, then point the new
//     release's tag at it (a manifest copy: no layers move), so the registry
//     looks exactly as if the command had pushed it.
//  4. Record the digest — and the fingerprint and the release it came from —
//     in the build state the cut harvests (inputs_digest, carried_from).
//
// Anything forge cannot prove runs the command and says why. `--rebuild`
// forces every command; a step with no `inputs` runs as it always has.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// releaseInputsScheme names the layout of the recipe releaseInputsFingerprint
// hashes. Bump it whenever that layout changes, so a digest an older forge
// recorded can never match one a newer forge computes under other rules.
const releaseInputsScheme = "forge-shellbuild-release-inputs/v2"

// releaseTagSentinel is the image_tag the template render binds. A valid
// image tag, so KCL that composes an image reference accepts it, and one no
// author writes: if a command did contain it, substituting the real tag back
// would not reproduce the command, and the build runs.
const releaseTagSentinel = "forge-release-tag-template-7f3c9a1e"

// shellBuildTemplate is a ShellBuild's command and declared env as the
// template render composed them, with releaseTagSentinel where the tag went.
type shellBuildTemplate struct {
	Cmd string
	Env map[string]string
}

// renderReleaseTemplates renders env a second time with image_tag bound to
// releaseTagSentinel (the build's other bindings unchanged) and attaches each
// ShellBuild's template to the workload in ents. Only a --release build that
// can carry an image, and only when a ShellBuild declares inputs, pays for it.
// A failed render attaches nothing: those builds run, and say why.
func renderReleaseTemplates(ctx context.Context, opts buildOptions, extra []string, ents *KCLEntities) {
	if ents == nil || !releaseBuildCanCarry(opts) || opts.rebuild || !anyShellInputs(ents) {
		return
	}
	bindings := make([]string, 0, len(extra)+1)
	for _, b := range extra {
		if !strings.HasPrefix(b, "image_tag=") {
			bindings = append(bindings, b)
		}
	}
	bindings = append(bindings, "image_tag="+strconv.Quote(releaseTagSentinel))
	tpl, err := RenderKCLWith(ctx, projectDirForKCL(), opts.env, bindings)
	if err != nil {
		fmt.Printf("[build] warning: could not render the ShellBuild templates a release compares (%v); every ShellBuild runs\n", err)
		return
	}
	attachReleaseTemplates(ents, tpl)
}

func anyShellInputs(ents *KCLEntities) bool {
	for _, w := range ents.Workloads {
		if externalBuildInputs(w) != nil {
			return true
		}
	}
	return false
}

// attachReleaseTemplates gives every ShellBuild in ents that declares inputs
// the command and env its namesake in tpl (the sentinel render) carries.
func attachReleaseTemplates(ents, tpl *KCLEntities) {
	byName := map[string]WorkloadEntity{}
	for _, w := range tpl.Workloads {
		byName[w.Name] = w
	}
	for i, w := range ents.Workloads {
		t, ok := byName[w.Name]
		if externalBuildInputs(w) == nil || !ok || t.shell() == nil {
			continue
		}
		ents.Workloads[i].releaseTemplate = &shellBuildTemplate{Cmd: t.EffectiveBuildCmd(), Env: t.EffectiveBuildEnv()}
	}
}

// fromTemplate is the template value standing for actual, given the real tag:
// the template's when substituting the tag back reproduces actual exactly.
func fromTemplate(template, actual, tag string) (string, bool) {
	return template, strings.ReplaceAll(template, releaseTagSentinel, tag) == actual
}

// externalImageRetag points dst (`<repository>:<tag>`) at src
// (`<repository>@<digest>`) in the registry. A package var so tests can
// substitute a fake registry; production never reassigns it.
var externalImageRetag = retagImage

// releaseReuseCandidatesFn reads the releases a --release build may reuse an
// image from, in the order they are tried. A package var so tests can
// substitute a ledger; production never reassigns it.
var releaseReuseCandidatesFn = loadReleaseReuseCandidates

// retagImage copies src's manifest to dst with `docker buildx imagetools
// create`. --prefer-index=false keeps a single-platform manifest a carbon
// copy (the default would wrap it in a new index with a new digest). A buildx
// too old for the flag fails here, and the build runs instead.
func retagImage(ctx context.Context, src, dst string) error {
	out, err := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "create",
		"--prefer-index=false", "--tag", dst, src).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker buildx imagetools create --tag %s %s: %w: %s", dst, src, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// releaseBuildCanCarry reports whether this build may reuse an earlier
// release's image: a --release build outside snapshot composition (which
// carries an unchanged workload by leaving it unselected instead).
func releaseBuildCanCarry(opts buildOptions) bool {
	return opts.release != "" && opts.composition == nil
}

// releaseCarry is the earlier release's image a ShellBuild reuses.
type releaseCarry struct {
	// from is the version of the release the image was recorded in.
	from string
	// repository and digest name the image; platforms is what its manifest
	// advertises.
	repository, digest string
	platforms          []string
	// tagged is the new release's tag, now pointing at digest.
	tagged string
}

// releaseReuseBase is the releases a --release build compares against, read
// from the env's ledger once per build and only if a ShellBuild needs it.
type releaseReuseBase struct {
	once     sync.Once
	load     func() ([]release.Release, error)
	releases []release.Release
	err      error
}

// newReleaseReuseBase returns the lazily-read comparison base for a build,
// or nil when the build cannot carry anything (not a release, or --rebuild).
func newReleaseReuseBase(ctx context.Context, projectDir string, opts buildOptions) *releaseReuseBase {
	if !releaseBuildCanCarry(opts) || opts.rebuild {
		return nil
	}
	return &releaseReuseBase{load: func() ([]release.Release, error) {
		return releaseReuseCandidatesFn(ctx, projectDir, opts.env)
	}}
}

func (b *releaseReuseBase) get() ([]release.Release, error) {
	if b == nil {
		return nil, nil
	}
	b.once.Do(func() { b.releases, b.err = b.load() })
	return b.releases, b.err
}

// loadReleaseReuseCandidates is the release env runs now, then the newest
// release recorded for the project, without repeats. Those are "the previous
// release": the one whose digests the env's workloads are pinned to, and the
// one a re-cut would otherwise supersede. Reusing from the bound one first is
// what keeps a consumer from rolling when two releases carry equal inputs.
func loadReleaseReuseCandidates(ctx context.Context, projectDir, env string) ([]release.Release, error) {
	ledger, err := ledgerFor(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	var out []release.Release
	seen := map[string]bool{}
	if p, ok, err := ledger.Bindings.Current(ctx, env); err != nil {
		return nil, err
	} else if ok && p.Release != "" {
		r, err := ledger.Releases.Get(ctx, p.Release)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
			seen[r.Version] = true
		}
	}
	all, err := ledger.Releases.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) > 0 && !seen[all[0].Version] {
		out = append(out, all[0])
	}
	return out, nil
}

// releaseInputsFingerprint fingerprints a ShellBuild's declared inputs the way
// a release compares them: the files' content, plus every field of the
// artifact identity (externalArtifactIdentity — the command, cwd, platform,
// inputs_file, env, built_images, source and patterns) and what only an
// execution knows (the COMPLETE effective env, spec.BuildEnv with the
// built_images overlay, every key and value; the destination repository,
// unit, output name and inputs_file content).
//
// The release tag changes on every cut, so the command and env are taken from
// the template render (shellBuildTemplate), where the tag is a sentinel at
// exactly the places the KCL composed it in. Nothing is substring-masked: a
// literal that equals the tag, or a path, stays in the fingerprint as itself.
// The template is used only where substituting the tag back reproduces the
// actual value byte for byte; a command that does not reproduce is refused (the
// build runs), and an env value that does not is hashed as it is.
//
// The cwd is keyed relative to the project (forge resolved it, so it knows
// what is location). A command that composes the checkout's absolute path
// (`${file.workdir()}`) hashes that path, so it reuses only across cuts from
// the same location — a rebuild, never a stale reuse.
//
// A command that bakes the tag into the image itself (a version stamp) would
// be carried with the earlier release's stamp: such a build should declare no
// inputs, or take its version from a file it declares as one.
func releaseInputsFingerprint(svc WorkloadEntity, spec buildtarget.Spec, cwd, projectDir string, opts buildOptions) (buildtarget.Fingerprint, error) {
	tpl := svc.releaseTemplate
	if tpl == nil {
		return buildtarget.Fingerprint{}, fmt.Errorf("no template render of its command, so forge cannot tell the release tag from the rest of it")
	}
	cmd, ok := fromTemplate(tpl.Cmd, svc.EffectiveBuildCmd(), spec.Tag)
	if !ok {
		return buildtarget.Fingerprint{}, fmt.Errorf("its command depends on the release tag in a way the template render does not reproduce")
	}
	canonical, err := canonicalDir(cwd)
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	project, err := canonicalDir(projectDir)
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	identity := externalBuildIdentity(svc, canonical, opts)
	identity.Cmd = cmd
	identity.Env = templatedEnv(tpl.Env, identity.Env, spec.Tag)
	identity.Cwd = releaseInputsCwd(project, canonical, identity.Source != nil)
	inputsFileDigest, err := shellBuildInputsFileDigest(svc, projectDir)
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	recipe, err := json.Marshal(struct {
		Scheme           string                   `json:"scheme"`
		Sentinel         string                   `json:"sentinel"`
		Identity         externalArtifactIdentity `json:"identity"`
		Repository       string                   `json:"repository"`
		Env              map[string]string        `json:"env"`
		Unit             string                   `json:"unit"`
		OutputName       string                   `json:"output_name"`
		InputsFileDigest string                   `json:"inputs_file_digest"`
	}{releaseInputsScheme, releaseTagSentinel, identity, externalPushedRepository(svc, opts.pushPlan.pushBase),
		templatedEnv(tpl.Env, spec.BuildEnv, spec.Tag), svc.Build.Unit, shellBuildOutputName(svc), inputsFileDigest})
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	return buildtarget.FingerprintInputs(shellBuildInputScope(canonical, projectDir), externalBuildInputs(svc), recipe)
}

// templatedEnv is actual — every key, every value; encoding/json sorts the
// keys — with each value the template reproduces replaced by its template
// form. A value it does not reproduce (a built_images overlay, an env the
// template render composed differently) is hashed exactly as it is.
func templatedEnv(template, actual map[string]string, tag string) map[string]string {
	out := make(map[string]string, len(actual))
	for k, v := range actual {
		out[k] = v
		if t, ok := template[k]; ok {
			if tv, same := fromTemplate(t, v, tag); same {
				out[k] = tv
			}
		}
	}
	return out
}

func canonicalDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// releaseInputsCwd keys the build's cwd without the checkout's location:
// relative to the project when it lies in the project's workspace (the
// project itself, or a sibling checkout), "git-source" for a declared
// GitSource's cache checkout (whose repo and ref the identity already holds),
// and absolute otherwise — which ties only an absolute cwd outside the
// workspace to its path, costing a rebuild, never a stale reuse.
func releaseInputsCwd(project, cwd string, gitSource bool) string {
	if within(filepath.Dir(project), cwd) {
		if rel, err := filepath.Rel(project, cwd); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	if gitSource {
		return "git-source"
	}
	return filepath.ToSlash(cwd)
}

// checkReleaseCarry finishes a --release build's verdict: it records the
// release fingerprint (so a build that runs still records it), and decides
// whether an earlier release's image stands in for running the command.
func checkReleaseCarry(ctx context.Context, svc WorkloadEntity, spec buildtarget.Spec, cwd string, opts buildOptions, projectDir string,
	base *releaseReuseBase, v shellInputsVerdict) (out shellInputsVerdict) {
	// v.hashed is the time already spent on the build's own fingerprint.
	start := time.Now().Add(-v.hashed)
	defer func() { out.took = time.Since(start) }()
	rfp, err := releaseInputsFingerprint(svc, spec, cwd, projectDir, opts)
	if err != nil {
		v.reason = fmt.Sprintf("could not fingerprint its inputs for a release (%v)", err)
		return v
	}
	v.releaseFP = &rfp
	v.hashed = time.Since(start)
	switch {
	case opts.rebuild:
		v.reason = "forced by --rebuild"
		return v
	case svc.Image == "":
		v.reason = "the workload declares no image, so there is no earlier release's image to reuse"
		return v
	}
	candidates, err := base.get()
	if err != nil {
		v.reason = fmt.Sprintf("could not read the release ledger to compare its inputs (%v)", err)
		return v
	}
	repo := externalPushedRepository(svc, opts.pushPlan.pushBase)
	carry, why := findReleaseCarry(ctx, candidates, repo, rfp.Digest, spec.Tag, externalImageDigestResolver, externalImageRetag)
	if carry == nil {
		v.reason = why
		return v
	}
	v.carry = carry
	return v
}

// findReleaseCarry picks the image a release build reuses: the first
// candidate release whose artifact for repo recorded inputsDigest and whose
// digest the registry still serves, after pointing repo:tag at it. nil and
// the reason when there is none.
func findReleaseCarry(ctx context.Context, candidates []release.Release, repo, inputsDigest, tag string,
	resolve func(context.Context, string) (string, []string, error),
	retag func(context.Context, string, string) error) (*releaseCarry, string) {
	if len(candidates) == 0 {
		return nil, "no earlier release is recorded to compare its inputs with"
	}
	var why []string
	note := func(format string, args ...any) { why = append(why, fmt.Sprintf(format, args...)) }
	for _, cand := range candidates {
		art, ok := cand.Artifacts[repo]
		digest, shared := art.SharedDigest()
		switch {
		case !ok || !shared:
			note("release %s has no image %s", cand.Version, repo)
			continue
		case art.InputsDigest == "":
			note("release %s recorded no inputs digest for %s (cut before forge recorded one, or the step declared no inputs then)", cand.Version, repo)
			continue
		case art.InputsDigest != inputsDigest:
			note("declared inputs changed since release %s (%s → %s)", cand.Version, shortFingerprint(art.InputsDigest), shortFingerprint(inputsDigest))
			continue
		}
		pinned := repo + "@" + digest
		got, platforms, err := resolve(ctx, pinned)
		if err != nil || got != digest {
			if err == nil {
				err = fmt.Errorf("the registry answered %s", got)
			}
			note("release %s's image %s is no longer in the registry (%v)", cand.Version, pinned, err)
			continue
		}
		tagged := repo + ":" + tag
		if why, ok := pointTagAt(ctx, tagged, pinned, digest, resolve, retag); !ok {
			note("%s", why)
			continue
		}
		return &releaseCarry{from: cand.Version, repository: repo, digest: digest, platforms: platforms, tagged: tagged}, ""
	}
	return nil, strings.Join(why, "; ")
}

// pointTagAt makes tagged resolve to digest: a no-op when it already does, a
// retag when the tag does not exist yet, and a refusal when it names another
// image (a release's tag is immutable). Verified by reading it back.
func pointTagAt(ctx context.Context, tagged, pinned, digest string,
	resolve func(context.Context, string) (string, []string, error),
	retag func(context.Context, string, string) error) (string, bool) {
	if got, _, err := resolve(ctx, tagged); err == nil {
		if got == digest {
			return "", true
		}
		return fmt.Sprintf("%s already names %s, not %s", tagged, got, digest), false
	}
	if err := retag(ctx, pinned, tagged); err != nil {
		return fmt.Sprintf("could not tag %s as %s (%v)", pinned, tagged, err), false
	}
	if got, _, err := resolve(ctx, tagged); err != nil || got != digest {
		if err == nil {
			err = fmt.Errorf("it resolves to %s", got)
		}
		return fmt.Sprintf("tagged %s as %s, but could not read it back (%v)", pinned, tagged, err), false
	}
	return "", true
}

// carryReleaseImage stands an earlier release's image in for running the
// command: it records the build state a run would have — the new release's
// tag, the reused digest, the checkout the inputs were read from — plus the
// fingerprint and the release it came from, for every consumer of the
// artifact.
func carryReleaseImage(ctx context.Context, svc WorkloadEntity, opts buildOptions, projectDir, cwd, tag string, aliases map[string][]WorkloadEntity, v shellInputsVerdict) buildResult {
	c := v.carry
	fmt.Printf("[build] %s: reusing %s@%s from release %s — declared inputs unchanged (%s; %s); tagged %s; not running the command (--rebuild forces it)\n",
		svc.Name, c.repository, c.digest, c.from, describeFingerprint(v.releaseFP, v.hashed), describeMatchedInputs(externalBuildInputs(svc), v.releaseFP), c.tagged)
	for _, alias := range aliases[svc.Name] {
		fmt.Printf("[build] %s: sharing ShellBuild artifact with %s\n", alias.Name, svc.Name)
	}
	state := buildtarget.State{
		Service:   svc.Name,
		Image:     c.repository,
		Tag:       tag,
		PushedAt:  nowRFC3339(),
		Digest:    c.digest,
		Platforms: c.platforms,
		// The checkout whose declared inputs matched: a release's sources
		// are held to the project's pins (checkRecordedReleaseSources), and
		// these are the inputs the carried image corresponds to.
		Source:              externalSourceCapture(ctx, svc.Name, cwd, projectDir),
		ReleaseInputsDigest: v.releaseFP.Digest,
		CarriedFrom:         c.from,
	}
	if v.fp != nil {
		state.InputsFingerprint = v.fp.Digest
	}
	recordExternalBuildState(svc, opts, projectDir, aliases, state)
	return buildResult{
		name:       svc.Name + " (external)",
		kind:       "external",
		duration:   v.took,
		upToDate:   true,
		digest:     c.digest,
		platforms:  c.platforms,
		repository: c.repository,
		image:      svc.Image,
		workload:   svc.Name,
	}
}

// describeMatchedInputs names what matched: the declared patterns and the
// first few files they selected.
func describeMatchedInputs(patterns []string, fp *buildtarget.Fingerprint) string {
	s := "inputs " + strings.Join(patterns, ", ")
	if fp == nil {
		return s
	}
	paths := fp.Paths()
	more := ""
	if len(paths) > 5 {
		more = fmt.Sprintf(" and %d more", len(paths)-5)
		paths = paths[:5]
	}
	return s + " → " + strings.Join(paths, ", ") + more
}
