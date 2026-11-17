package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// resolveImageTag computes the canonical docker-image tag that `forge
// build --push` would push and that `forge env deploy <env>` would reference
// when no explicit override and no per-env build-state file is present.
//
// The tag mirrors what dockerBuildProject actually tags the image with:
// `git describe --tags --always --dirty`. This is the cornerstone of
// the "build is the source of truth" contract — both phases compute the
// same string from the same git state, so a build followed by a deploy
// in a clean tree always agrees.
//
// When the working tree changes between phases (the original bug:
// untracked files appear/disappear, `-dirty` toggles), only the state
// file written by `forge env build --push` keeps the two phases in lock-
// step. resolveImageTag is the standalone-deploy fallback for when no
// state file exists.
//
// The env arg is reserved for future per-env tag conventions (e.g.
// staging vs prod) and is currently unused. Keeping it in the signature
// avoids a churn-only change later.
//
// A repository with no commits yet (a fresh `forge project new`) has no
// revision to describe: it resolves to untaggedDevTag instead of failing, so
// the first `forge env up` works before the first commit. Outside a git
// repository (or without git) it is an error and the caller decides whether
// to surface it or ask for --tag.
func resolveImageTag(ctx context.Context, _ string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		if gitHeadUnborn(ctx) {
			return untaggedDevTag, nil
		}
		// Not a git repo, or git not on PATH. Fall back to short SHA;
		// rev-parse covers the "git installed but no tag" case which
		// describe --always already handled, plus the "shallow clone
		// with no tags" path that some CI runners hit.
		out2, err2 := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
		if err2 != nil {
			return "", fmt.Errorf("git tag resolution: %w", err)
		}
		tag := strings.TrimSpace(string(out2))
		if tag == "" {
			return "", fmt.Errorf("git rev-parse --short HEAD returned empty output")
		}
		return tag, nil
	}
	tag := strings.TrimSpace(string(out))
	if tag == "" {
		return "", fmt.Errorf("git describe returned empty output")
	}
	return tag, nil
}

// untaggedDevTag is the image tag for a project with no commits. It names
// no revision because there is none; the first commit replaces it with the
// usual git-derived tag.
const untaggedDevTag = "uncommitted"

// gitHeadUnborn reports whether the working directory is inside a git
// repository whose HEAD points at no commit yet.
func gitHeadUnborn(ctx context.Context) bool {
	if err := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", "HEAD").Run() != nil
}

// buildTagFor is THE tag precedence for an image forge builds, for every
// lane — ShellBuild, DockerBuild, the project image and frontend images:
//
//  1. a release version (`--release`): a cut never writes a shared tag;
//  2. an explicit `--tag`;
//  3. the tag the workload's declared image pins (`image = "reliant:e2e"`);
//  4. buildWide — the env's image_tag, else the git-derived default
//     (resolveBuildImageTag).
//
// (2) over (3) never builds a ref nothing deploys: a --tag that differs from
// a selected workload's pin is refused before anything runs
// (checkExplicitTagAgainstPins), so reaching here with both set means they
// agree or the workload is not pinned.
//
// Before this was one function, each lane ordered the sources itself and the
// ShellBuild lane put the env's image_tag above --tag: `forge build prod
// --tag t1 --push` printed `Tag: t1` and then built and recorded `:latest`.
func buildTagFor(opts buildOptions, pin, buildWide string) string {
	if rt := releaseImageTag(opts); rt != "" {
		return rt
	}
	if opts.tag != "" {
		return opts.tag
	}
	if pin != "" {
		return pin
	}
	return buildWide
}

// imagePinFor is the tag a workload built into image pins, if any — the pin
// the PROJECT image builds as, since that one image is shared by many
// workloads and so is not any one workload's build. A ShellBuild or
// DockerBuild workload reads its own pin (PinnedBuildTag) instead: a
// workload sharing an image with a pinned one still deploys the env's tag.
//
// The pin is read off the BUILD identity (WorkloadEntity.BuildImage), never
// off spec.image: the build is the same whichever runtime the workload
// binds, and a BuildOnly or host workload resolves no spec.image at all.
func imagePinFor(entities *KCLEntities, image string) string {
	if entities == nil || image == "" {
		return ""
	}
	for _, w := range entities.Workloads {
		// `image` is the artifact NAME the build knows (the project name, a
		// frontend's name); w.Image is the full repository the workload
		// declared. They join on the repository's last path segment, which is
		// the repository's own name: `ghcr.io/acme/shop` IS the artifact
		// `shop`. Matching the whole string would silently find nothing and
		// lose every pin.
		if w.Image != image && repositoryName(imageRepository(w.Image)) != image {
			continue
		}
		if tag, ok := w.PinnedBuildTag(); ok {
			return tag
		}
	}
	return ""
}

// checkExplicitTagAgainstPins refuses an explicit --tag that would build a
// pinned workload's image under a tag its deploy does not pull.
//
// A workload whose declared image pins a tag (`image = "reliant:e2e"`)
// deploys that exact ref. Building `reliant:t1` for it pushes a ref no pod
// ever pulls, while the deploy keeps pulling whatever `:e2e` last was — the
// build looks like it shipped and nothing changed. Silently preferring the
// pin instead would ignore a flag the user typed. So it is refused, naming
// every conflicting workload and both tags.
//
// Scoped to what THIS build produces: entities is already narrowed by
// --target, so a --target that selects only unpinned workloads is no
// conflict. A GoBuild workload counts only when the project image is built.
func checkExplicitTagAgainstPins(entities *KCLEntities, opts buildOptions, projectImage string, projectImageBuilt bool) error {
	if entities == nil || opts.tag == "" || releaseImageTag(opts) != "" {
		return nil
	}
	var conflicts []string
	for _, w := range entities.Workloads {
		pin, ok := w.PinnedBuildTag()
		if !ok || pin == opts.tag {
			continue
		}
		switch w.Build.Type {
		case "shell", "docker":
		case "go":
			if !projectImageBuilt || w.Image != projectImage {
				continue
			}
		default:
			continue
		}
		conflicts = append(conflicts, fmt.Sprintf("workload %q declares image %s, so its deploy pulls tag %q, not %q",
			w.Name, w.BuildImage, pin, opts.tag))
	}
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf("--tag %q conflicts with a pinned image:\n  %s\n"+
		"Building under --tag would push a ref nothing deploys. Drop --tag to build the pinned tag, "+
		"narrow --target to workloads whose image pins no tag, or change the pin in deploy/kcl/%s/",
		opts.tag, strings.Join(conflicts, "\n  "), envNameOr(opts.env))
}

// PinnedBuildTag is the tag the workload's own image pins for its build
// ("workspace-base:dev-per-daemon" answers dev-per-daemon). ok=false when it
// pins none (the env tag applies) or forge builds nothing for it.
func (w WorkloadEntity) PinnedBuildTag() (string, bool) {
	if w.Image == "" {
		return "", false
	}
	return pinnedTagOf(w.BuildImage, w.Image)
}

// pinnedTagOf is the tag of a resolved ref when that ref names the artifact
// repository — `localhost:5051/acme/app:v2` for `acme/app` answers v2. The
// repository must end the ref's path at a `/` boundary, so `app` does not
// claim `myapp:v1`'s tag (the org path is kept on both sides; the registry
// prefix is whatever the target resolved). ok=false for a digest, a tagless
// ref, or a different repository.
func pinnedTagOf(ref, repository string) (tag string, ok bool) {
	if strings.Contains(ref, "@") {
		return "", false
	}
	lastColon := strings.LastIndex(ref, ":")
	if lastColon < 0 || strings.Contains(ref[lastColon+1:], "/") {
		return "", false
	}
	path, tag := ref[:lastColon], ref[lastColon+1:]
	if tag == "" || (path != repository && !strings.HasSuffix(path, "/"+repository)) {
		return "", false
	}
	return tag, true
}
