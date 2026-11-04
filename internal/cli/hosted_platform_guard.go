package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// hostedPlatformResolver reads the platforms a pushed image advertises from
// the REGISTRY. A package var so tests can serve a fake registry.
var hostedPlatformResolver = imageRegistryPlatforms

// checkHostedArtifactPlatforms refuses a release whose pushed image cannot run
// on the hosted env's nodes. A hosted image is built on whatever machine ran
// `forge build`; if that machine's arch leaked into the image, the pod dies at
// exec ("exec format error") long after the push succeeded.
//
// The release's recorded platforms are used when present. When they are
// absent — capture is best-effort and older releases never recorded any — the
// registry is asked, by digest. FAIL CLOSED: if the manifest cannot be read
// the artifact is refused, because "unknown" is exactly the state the bad
// release was in.
//
// Only hosted WORKLOAD images are checked. A static-site release (static.v1)
// is not an executable image and has no platform; it is exempt by never being
// a hosted workload's artifact. It is a read, so it runs on --plan-only too.
func checkHostedArtifactPlatforms(ctx context.Context, out io.Writer, e *KCLEntities, artifacts map[string]release.Artifact, env, version string) error {
	if e == nil {
		return nil
	}
	var bad []string
	checked := map[string]bool{}
	for _, w := range e.WorkloadsOn(RuntimeHosted) {
		if w.Image == "" {
			continue
		}
		key := hostedArtifactKey(e, w)
		art, ok := artifacts[key]
		if !ok || art.Kind != release.KindOCI || checked[w.Name] {
			continue
		}
		checked[w.Name] = true
		want := "linux/" + w.Runtime.HostedPlatform()
		platforms, source := art.Platforms, "recorded in the release"
		if len(platforms) == 0 {
			digest := art.Digests[release.SharedVariant]
			ref := key + "@" + digest
			if digest == "" {
				bad = append(bad, fmt.Sprintf("  %s (%s): the release records no digest and no platform", w.Name, key))
				continue
			}
			p, err := hostedPlatformResolver(ctx, ref)
			if err != nil {
				bad = append(bad, fmt.Sprintf("  %s (%s): the release records no platform and the registry manifest could not be read (%v)", w.Name, ref, err))
				continue
			}
			platforms, source = p, "read from the registry"
		}
		if !containsString(platforms, want) {
			bad = append(bad, fmt.Sprintf("  %s (%s): expected %s, found %s", w.Name, key, want, strings.Join(platforms, ", ")))
			continue
		}
		fmt.Fprintf(out, "  platform  %s: %s (%s)\n", w.Name, strings.Join(platforms, ", "), source)
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("hosted env %q release %s: image platform does not match the control plane's nodes (or cannot be verified) — such pods exit with \"exec format error\".\n%s\n"+
		"  fix: rebuild with `forge env build %s --push --release <new-version>` (forge builds hosted images for forge.OnHosted.platform), then `forge env deploy %s <new-version>`",
		env, version, strings.Join(bad, "\n"), env, env)
}

// checkReleaseHostedPlatforms loads the release and the env's render and runs
// checkHostedArtifactPlatforms over them. An env with no hosted workload, or
// one that does not render, is not this check's to judge.
func checkReleaseHostedPlatforms(ctx context.Context, out io.Writer, projectDir, env, version string, releases interface {
	Get(ctx context.Context, version string) (*release.Release, error)
}) error {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil || entities == nil || !entities.HasHosted() {
		return nil
	}
	rel, err := releases.Get(ctx, version)
	if err != nil || rel == nil {
		return nil
	}
	return checkHostedArtifactPlatforms(ctx, out, entities, rel.Artifacts, env, version)
}
