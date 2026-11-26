package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/hostedimage"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedBackendDigestResolver resolves a tag-pinned image to its registry
// digest. A var so tests state the registry's answer instead of needing one.
var hostedBackendDigestResolver = func(ctx context.Context, ref string) (string, []string, error) {
	return externalImageDigestResolver(ctx, ref)
}

// hostedImageMirror copies a third-party image into the platform registry
// (deploytarget.MirrorImage). A var so tests state the registry's answer.
var hostedImageMirror = deploytarget.MirrorImage

// harvestHostedBackendArtifacts records the image every HOSTED workload that
// forge does not build declares — so a release cut covers the workloads
// a hosted deploy ships, and `forge env deploy` can pin them from the binding.
//
// WHY THIS IS SEPARATE FROM THE BUILD-STATE HARVEST. Such a workload names an
// image forge did not build (CI pushed it, or it is a third-party image), so
// there is no .forge/state record to project. The declaration is
// the source of truth for WHICH image; the registry is the source of truth for
// WHICH BYTES:
//
//   - a digest-pinned image (`repo@sha256:…`) is recorded as declared;
//   - a tag-pinned image is resolved to its digest NOW, at cut time. That is
//     the one moment the tag is allowed to mean something: once recorded, the
//     release pins the bytes and a later re-push of the tag changes nothing;
//   - a BARE image is read under the push base, where CI pushed it;
//   - a third-party image OUTSIDE the push base (docker.io/traefik/whoami) is
//     MIRRORED into it (hostedimage.MirrorRepository), registry to registry and
//     by digest, because the platform pulls only from its own registry. The
//     artifact key is the mirror's address, which is what the deploy pins.
//
// An artifact the build-state harvest already recorded (forge built and
// pushed it) wins: it is the digest of the bytes this very pipeline produced.
// Artifacts are keyed by hostedArtifactKey, the same rule the hosted provider
// pins by, so the cut and the deploy cannot disagree.
func harvestHostedBackendArtifacts(ctx context.Context, envName string, entities *KCLEntities, out map[string]release.Artifact) error {
	if entities == nil {
		return nil
	}
	// Every mirror is judged BEFORE any is copied: a cut refused half-way
	// would have pushed bytes no release records.
	if err := checkHostedMirrors(entities); err != nil {
		return err
	}
	var errs []string
	for _, svc := range entities.WorkloadsOn(RuntimeHosted) {
		name := hostedArtifactKey(entities, svc)
		if _, have := out[name]; have || svc.Image != "" {
			// forge built and pushed it (the build state's digest wins), or
			// forge builds it and this cut simply did not — the release
			// coverage check names that, with the build command.
			continue
		}
		image := svc.Spec.Image
		var (
			digest    = deploytarget.HostedImageDigest(image)
			platforms []string
			repo      = deploytarget.HostedImageRepository(image)
			uri       = strings.TrimSuffix(repo, "/"+deploytarget.HostedArtifactName(image))
		)
		switch {
		case hostedimage.HasRegistryHost(name) && !hostedimage.HasRegistryHost(repo):
			// A BARE image someone pushed under the push base: it is read
			// THERE, not on Docker Hub, which is where `whoami` alone resolves.
			image = resolveHostedImageBase(platformPushBase(entities), image)
			uri = strings.TrimSuffix(name, "/"+deploytarget.HostedArtifactName(name))
		case hostedimage.HasRegistryHost(name) && repo != name:
			// A third-party image outside the push base: copy it in, pinned
			// by digest (hostedImageMirror), so the platform can pull it.
			m, err := hostedImageMirror(ctx, image, name)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: mirror %s into %s: %v", svc.Name, image, name, err))
				continue
			}
			fmt.Printf("[build] %s: mirrored %s → %s@%s\n", svc.Name, image, name, m.Digest)
			digest, platforms = m.Digest, m.Platforms
			uri = strings.TrimSuffix(name, "/"+deploytarget.HostedArtifactName(name))
		}
		if digest == "" {
			d, p, err := hostedBackendDigestResolver(ctx, image)
			if err != nil {
				hint := ""
				if !hostedimage.HasRegistryHost(repo) {
					hint = fmt.Sprintf(" (a BARE image on a workload forge does not build is read from your push base: push it there, "+
						"or, for a third-party image, name it in full with its registry, e.g. image = \"docker.io/<owner>/%s:<tag>\", and forge mirrors it into the push base)",
						deploytarget.HostedArtifactName(repo))
				}
				errs = append(errs, fmt.Sprintf("%s: resolve %s to a digest: %v%s", svc.Name, image, err, hint))
				continue
			}
			digest, platforms = d, p
		}
		out[name] = release.Artifact{
			Kind:      release.KindOCI,
			Mode:      release.ModeShared,
			Digests:   map[string]string{release.SharedVariant: digest},
			URI:       uri,
			Platforms: platforms,
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("hosted backend images could not be recorded:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// checkHostedMirrors refuses, before anything is copied, every third-party
// image whose mirror destination the platform registry cannot hold:
//
//   - a destination outside the registry's repository-name grammar, or past
//     the reference length limit (hostedimage.ValidateMirror) — a source path
//     with uppercase letters, or a push base long enough to overflow;
//   - two DIFFERENT source references that land on one destination
//     (`ghcr.io/acme/api` and `docker.io/acme/api`, or `redis:6` and
//     `redis:7`). The release keys an artifact by its repository, so the
//     second would silently be pinned to the first one's digest. Two
//     spellings of one Docker Hub reference (`docker.io/nginx:1`,
//     `index.docker.io/library/nginx:1`) are one source and are fine.
//
// Each refusal names the workload and the way out: point every workload at one
// reference, or push the image under the push base yourself and name it bare.
func checkHostedMirrors(entities *KCLEntities) error {
	base := platformPushBase(entities)
	if base == "" {
		return nil
	}
	type use struct{ workload, source, canonical string }
	byDest := map[string]use{}
	var errs []string
	for _, svc := range entities.WorkloadsOn(RuntimeHosted) {
		if svc.Image != "" {
			continue
		}
		dest := hostedimage.MirrorRepository(base, svc.Spec.Image)
		if dest == "" {
			continue
		}
		if err := hostedimage.ValidateMirror(dest); err != nil {
			errs = append(errs, fmt.Sprintf("%s: image %s cannot be mirrored into the platform registry: %v.\n"+
				"    fix: push the image under %s yourself and name it bare on the workload (image = \"<name>:<tag>\"), "+
				"or use a shorter project name", svc.Name, svc.Spec.Image, err, base))
			continue
		}
		canonical := hostedimage.CanonicalSource(svc.Spec.Image) + mirrorSourceRef(svc.Spec.Image)
		if prev, ok := byDest[dest]; ok && prev.canonical != canonical {
			errs = append(errs, fmt.Sprintf("%s and %s: images %s and %s would both be mirrored to %s, and a release pins one "+
				"digest per repository, so one workload would run the other's image.\n"+
				"    fix: point both workloads at the same reference, or push one under %s yourself and name it bare",
				prev.workload, svc.Name, prev.source, svc.Spec.Image, dest, base))
			continue
		}
		byDest[dest] = use{workload: svc.Name, source: svc.Spec.Image, canonical: canonical}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("third-party hosted images could not be mirrored (nothing was copied):\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// mirrorSourceRef is the tag or digest a source reference names, with a bare
// repository read as `:latest`, so `nginx` and `nginx:latest` compare equal.
func mirrorSourceRef(image string) string {
	if at := strings.LastIndex(image, "@"); at >= 0 {
		return image[at:]
	}
	if repo := hostedimage.Repository(image); repo != image {
		return image[len(repo):]
	}
	return ":latest"
}
