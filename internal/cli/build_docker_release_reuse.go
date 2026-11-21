// build_docker_release_reuse.go lets `forge env build <env> --release` reuse an
// earlier release's image for a DockerBuild whose declared `inputs` did not
// change — the same rule build_external_release_reuse.go applies to a
// ShellBuild (forge#2326), over the same ledger fields (inputs_digest,
// carried_from), the same comparison base (releaseReuseBase) and the same
// registry step (findReleaseCarry).
//
// A DockerBuild used to rebuild on every cut. Few Dockerfiles are byte-for-byte
// reproducible (an apt layer, ldconfig caches, mtimes), so an unchanged tree
// pushed a new digest and rolled every workload pinned to it.
//
// What differs from a ShellBuild is only the fingerprint
// (dockerReleaseInputsFingerprint). forge composes the `docker build` argv
// itself and adds the `-t` tags, so the release tag is outside the
// declaration by construction: no template render is needed, and a build arg
// that carries the tag (a version stamp) is hashed as written, so that build
// is never reused — the reused image would carry the earlier stamp.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
)

// dockerReleaseInputsScheme names the layout of the recipe
// dockerReleaseInputsFingerprint hashes. Bump it whenever that layout changes.
// It differs from the ShellBuild scheme, so a workload that switches build
// type never matches the other's digest.
const dockerReleaseInputsScheme = "forge-dockerbuild-release-inputs/v1"

// serviceDockerBuilder runs one DockerBuild. A package var so tests can
// substitute a fake docker; production never reassigns it.
var serviceDockerBuilder = buildServiceDocker

// dockerInputsVerdict is what a DockerBuild's declared inputs say about a
// --release build.
type dockerInputsVerdict struct {
	// releaseFP is the release fingerprint; set whenever the build declares
	// inputs and they could be fingerprinted, so a build that runs records
	// it for the next release to compare.
	releaseFP *buildtarget.Fingerprint
	// carry is the earlier release's image this build reuses; nil means run
	// `docker build`.
	carry *releaseCarry
	// reason says why `docker build` runs despite declared inputs.
	reason string
	// hashed is the time spent fingerprinting; took adds the ledger read and
	// the registry checks.
	hashed, took time.Duration
}

// dockerBuildInputs is the DockerBuild's declared inputs, nil for a build
// that declares none (or is not a DockerBuild).
func dockerBuildInputs(w WorkloadEntity) []string {
	if d := w.Build.Docker; d != nil && len(d.Inputs) > 0 {
		return d.Inputs
	}
	return nil
}

// dockerInputsBase is the directory a DockerBuild's inputs are relative to:
// the resolved checkout of a GitSource context, else the project root — the
// same base its `dockerfile` resolves against.
func dockerInputsBase(d *DockerBuild, projectDir string) string {
	if d != nil && d.ContextDir != "" {
		return d.ContextDir
	}
	return projectDir
}

// releaseDockerTarget names the exact destination of the workload's image.
// The carry decision and the actual build must use the same target.
type releaseDockerTarget struct {
	repository string
	tag        string
}

// buildServiceDockerReusing builds a DockerBuild workload, or — on a --release
// build whose declared inputs equal an earlier release's — reuses that
// release's image instead of running `docker build`.
func buildServiceDockerReusing(ctx context.Context, cfg *config.ProjectConfig, w WorkloadEntity, target releaseDockerTarget,
	opts buildOptions, cfgArchForDocker, projectDir string, base *releaseReuseBase) buildResult {
	v := checkDockerReleaseInputs(ctx, cfg, w, target, opts, cfgArchForDocker, projectDir, base)
	if v.carry != nil {
		return carryDockerReleaseImage(w, target.tag, v)
	}
	if v.reason != "" {
		fmt.Printf("[build] %s: building — %s\n", w.Name, v.reason)
	}
	res := serviceDockerBuilder(ctx, cfg, w.Name, target.repository, w.Build.Docker, opts, cfgArchForDocker, target.tag)
	if res.err == nil && v.releaseFP != nil {
		// What the next release compares against (inputs_digest).
		res.releaseInputsDigest = v.releaseFP.Digest
	}
	return res
}

// checkDockerReleaseInputs fingerprints a DockerBuild's declared inputs on a
// --release build and decides whether an earlier release's image (base)
// stands in for building. Any other build returns the zero verdict: outside a
// release, docker's own layer cache is what makes an unchanged build cheap.
func checkDockerReleaseInputs(ctx context.Context, cfg *config.ProjectConfig, w WorkloadEntity, target releaseDockerTarget,
	opts buildOptions, cfgArchForDocker, projectDir string, base *releaseReuseBase) (v dockerInputsVerdict) {
	if !releaseBuildCanCarry(opts) {
		return v
	}
	if dockerBuildInputs(w) == nil {
		if opts.pushPlan.push {
			v.reason = "it declares no inputs, so nothing proves an earlier release's image is what it would build"
		}
		return v
	}
	start := time.Now()
	defer func() { v.took = time.Since(start) }()
	rfp, err := dockerReleaseInputsFingerprint(cfg, w, target.repository, opts, cfgArchForDocker, projectDir)
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
	case !opts.pushPlan.push:
		v.reason = "this build does not push, so there is no registry tag to point at an earlier release's image"
		return v
	}
	candidates, err := base.get()
	if err != nil {
		v.reason = fmt.Sprintf("could not read the release ledger to compare its inputs (%v)", err)
		return v
	}
	carry, why := findReleaseCarry(ctx, candidates, target.repository, rfp.Digest, target.tag, externalImageDigestResolver, externalImageRetag)
	if carry == nil {
		v.reason = why
		return v
	}
	v.carry = carry
	return v
}

// dockerReleaseInputsFingerprint fingerprints a DockerBuild the way a release
// compares it: the content of every file its inputs select (relative to
// dockerInputsBase), plus everything else the image depends on —
//
//   - the declaration as written (DockerBuild.MarshalJSON: the context and
//     dockerfile paths or GitSource pins, never the directory a pin resolved
//     to on this machine; target, build_args, build_contexts, pgo,
//     output_name and the patterns), so any field docker reads is hashed;
//   - the build contexts actually passed (forge.yaml's when the build
//     declares none);
//   - the Dockerfile's CONTENT, which need not be listed as an input;
//   - the effective platform, the destination repository, unit and the
//     content of a declared inputs_file.
//
// The release tag is not in it: forge adds the `-t` tags itself. Nor is
// SOURCE_DATE_EPOCH, which only dates the layers of the bytes being reused.
func dockerReleaseInputsFingerprint(cfg *config.ProjectConfig, w WorkloadEntity, repository string, opts buildOptions,
	cfgArchForDocker, projectDir string) (buildtarget.Fingerprint, error) {
	d := w.Build.Docker
	base, err := canonicalDir(dockerInputsBase(d, projectDir))
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	declaration, err := json.Marshal(d)
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	contexts := d.BuildContexts
	if len(contexts) == 0 && cfg != nil {
		contexts = cfg.Docker.BuildContexts
	}
	dockerfileDigest, err := fileContentDigest(resolveProjectPath(projectDir, serviceDockerfile(d)))
	if err != nil {
		return buildtarget.Fingerprint{}, fmt.Errorf("dockerfile: %w", err)
	}
	inputsFileDigest, err := shellBuildInputsFileDigest(w, projectDir)
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	recipe, err := json.Marshal(struct {
		Scheme           string                         `json:"scheme"`
		Declaration      json.RawMessage                `json:"declaration"`
		BuildContexts    map[string]config.BuildContext `json:"build_contexts"`
		DockerfileDigest string                         `json:"dockerfile_digest"`
		Platform         string                         `json:"platform"`
		Repository       string                         `json:"repository"`
		Unit             string                         `json:"unit"`
		InputsFileDigest string                         `json:"inputs_file_digest"`
	}{dockerReleaseInputsScheme, declaration, contexts, dockerfileDigest, serviceDockerPlatform(d, opts, cfgArchForDocker),
		repository, w.Build.Unit, inputsFileDigest})
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	return buildtarget.FingerprintInputs(shellBuildInputScope(base, projectDir), d.Inputs, recipe)
}

// fileContentDigest is the sha256 content address of the file at path.
func fileContentDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// carryDockerReleaseImage stands an earlier release's image in for building:
// the result carries what a build would have — the new release's tag, the
// reused digest, the checkout a GitSource context resolved to — plus the
// fingerprint and the release it came from, which persistImageBuildStates
// records for the cut.
func carryDockerReleaseImage(w WorkloadEntity, tag string, v dockerInputsVerdict) buildResult {
	c := v.carry
	fmt.Printf("[build] %s: reusing %s@%s from release %s — declared inputs unchanged (%s; %s); tagged %s; not running docker build (--rebuild forces it)\n",
		w.Name, c.repository, c.digest, c.from, describeFingerprint(v.releaseFP, v.hashed), describeMatchedInputs(dockerBuildInputs(w), v.releaseFP), c.tagged)
	return buildResult{
		name: w.Name + " (docker)", kind: "docker", duration: v.took, upToDate: true,
		image: w.Name, repository: c.repository, tag: tag, digest: c.digest, platforms: c.platforms,
		source:              w.Build.Docker.sourceForRecord(),
		releaseInputsDigest: v.releaseFP.Digest,
		carriedFrom:         c.from,
	}
}
