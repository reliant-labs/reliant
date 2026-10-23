package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// hostedStaticPusher builds, packs and pushes one hosted frontend's site and
// returns the release digest. A var so the CLI tests state the registry's
// answer (an in-memory push) instead of needing a registry.
var hostedStaticPusher = func(ctx context.Context, projectDir, repository string, fe deploytarget.StaticSiteFrontend) (string, error) {
	tree, res, err := deploytarget.BuildStaticSiteTree(ctx, projectDir, fe)
	if err != nil {
		return "", err
	}
	layer, err := deploytarget.PackStaticSiteTree(tree)
	if err != nil {
		return "", err
	}
	if res.SourcemapsDir != "" {
		fmt.Printf("[build] %s: %d source map(s) extracted (build id %s) to %s, not part of the site\n",
			fe.Name, res.Sourcemaps, res.BuildID, res.SourcemapsDir)
	}
	// The maps are filed BEFORE the site is pushed: a site that reaches the
	// registry without its maps filed could be promoted unsymbolicatable.
	if err := deploytarget.FileStageSourcemaps(ctx, fe.SourcemapPublisher, "build", fe.Project, fe.Name, res); err != nil {
		return "", err
	}
	return deploytarget.PushStaticSiteArtifact(ctx, repository, layer)
}

// buildHostedStaticSites is the BUILD half of a hosted frontend: every
// frontend bound to forge.OnHosted is built, assembled, and pushed as an OCI
// release artifact to
// `<push>/static.v1/<frontend>` — where `<push>` is the org's image push base,
// the one registry subtree the control plane admits this org's artifacts from.
//
// The digest is recorded as ordinary build state under the frontend's name,
// with the registry set to `<push>/static.v1`, so the existing harvest turns
// it into a shared OCI release artifact whose URI + name is the real
// repository (and `forge release verify` can fetch its manifest).
//
// It runs only with --push: a site release that is not registry-addressable
// cannot be pinned, promoted or pulled by the platform.
//
// # The artifact carries NO runtime config
//
// A hosted release is promoted between environments by digest. A config.js
// baked in here would carry the BUILD env's values into every env the
// release is promoted to — staging's API origin, served to production users.
// So no document is written into the tree (and the dev copy `forge generate`
// keeps in the frontend's public/ is stripped from it). The environment's
// document is spec instead: the StaticSite's runtimeConfig, which the
// control plane resolves and writes after each sync (hostedStaticSpec).
func buildHostedStaticSites(ctx context.Context, projectDir string, entities *KCLEntities, opts buildOptions) error {
	sites := hostedStaticSites(entities, opts)
	if len(sites) == 0 {
		return nil
	}
	if !opts.pushPlan.push {
		names := make([]string, 0, len(sites))
		for _, f := range sites {
			names = append(names, f.Name)
		}
		return errHostedSiteMustPush(opts.env, names)
	}
	if err := resolveFrontendEntitySources(ctx, projectDir, entities); err != nil {
		return err
	}
	for _, f := range sites {
		if err := checkDeployableFrontendMock(f); err != nil {
			return err
		}
		f.Project = entities.Project
		fe := frontendToStaticSite(f)
		// Environment-agnostic by construction: no document written, and
		// any travelling in the built bundle removed.
		fe.RuntimeConfigJS = ""
		fe.StripRuntimeConfig = true
		// A record left by an earlier build must not be mistaken for this one's.
		if err := deploytarget.ClearSourcemapsState(projectDir, opts.env, f.Name); err != nil {
			return fmt.Errorf("hosted static site %s: %w", f.Name, err)
		}
		fe.SourcemapPublisher = sourcemapPublisherFor(projectDir, opts.env, entities)
		// THE REPOSITORY COMES FROM THE RESOLVED PUSH PLAN, not from the
		// declaration. They agree for a host-bearing reference and differ for
		// a bare one, which ADR-0003 F1 now permits: a bare hosted image
		// resolves to `<image_push_base>/<name>`, and the layout segment is
		// appended to the RESULT.
		//
		// Recomposing it from f.Image here is exactly the "two independent
		// derivations of one address" defect the release-ref rule exists to
		// prevent (see internal/deploytarget's
		// hosted_static_release_ref_test.go): it pushed `web/static.v1`, with
		// no registry at all, while the release and the published spec named
		// the resolved address. Reading the plan means the build pushes to
		// the one place every other half already looks.
		//
		// ONE rule, called here and by the push plan, rather than a lookup
		// into the plan: the plan is resolved from the FULL render and this
		// loop runs over the --target-narrowed set, so a lookup would also
		// have to answer "absent because narrowed" — which is not a
		// question the push needs to ask.
		repository := hostedStaticDestination(opts.pushPlan.pushBase, f.Image)
		fmt.Printf("[build] %s: hosted static site → %s\n", f.Name, repository)
		digest, err := hostedStaticPusher(ctx, projectDir, repository, fe)
		if err != nil {
			return fmt.Errorf("hosted static site %s: %w%s", f.Name, err, deniedPushHint(err, "", repository))
		}
		state := buildtarget.State{
			Service: f.Name,
			// Image is the repository this release was actually PUSHED to —
			// the declared reference plus the platform's layout segment, not
			// the reference alone. It is the release ledger's key, so recording
			// the bare reference here would leave the coverage gate looking up
			// an entry that does not exist and refusing a complete build.
			Image:    repository,
			Tag:      digest,
			PushedAt: nowRFC3339(),
			Digest:   digest,
		}
		if err := buildtarget.WriteState(projectDir, opts.env, state); err != nil {
			return fmt.Errorf("hosted static site %s: record build state: %w", f.Name, err)
		}
		fmt.Printf("[build]   %s release %s\n", f.Name, digest)
	}
	return nil
}

// hostedStaticSites is the set of frontends a build publishes as hosted
// static sites: every frontend bound to forge.OnHosted that --target does not
// narrow away. The ONE selection both the build (buildHostedStaticSites) and
// the plan (planHostedStaticSites) read, so `forge build --plan` lists exactly
// the sites the cut would push.
func hostedStaticSites(entities *KCLEntities, opts buildOptions) []FrontendEntity {
	if entities == nil {
		return nil
	}
	var sites []FrontendEntity
	for _, f := range entities.Frontends {
		if !frontendIsHosted(f) {
			continue
		}
		if opts.buildTarget != "" && opts.buildTarget != "all" && opts.buildTarget != f.Name {
			continue
		}
		sites = append(sites, f)
	}
	return sites
}

// errHostedSiteMustPush is the refusal a build without --push hits when the
// env binds a frontend to forge.OnHosted. Shared with the plan so the two
// report the same remedy.
func errHostedSiteMustPush(env string, names []string) error {
	return fmt.Errorf("env %q binds frontend(s) %s to forge.OnHosted: a hosted site ships as an OCI release artifact, "+
		"so the build must push — run `forge env build %s --push`, which pushes to each frontend's own `image` reference "+
		"(plus the platform's static.v1 layout)",
		env, strings.Join(names, ", "), env)
}
