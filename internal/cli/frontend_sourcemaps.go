package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/hostedimage"
	"github.com/reliant-labs/forge/pkg/release"
)

// Filing a frontend's source maps.
//
// The static stage (internal/deploytarget/sourcemaps.go) moves every map out of
// the tree a deploy publishes. This file decides WHERE they go and records what
// was filed, for the three paths that build a frontend, and adds the verb for a
// build forge did not run:
//
//	forge env deploy   OnFirebase / OnBucket frontends: built at deploy time,
//	                   maps filed before the site goes live
//	forge env build    OnHosted frontends: built and pushed as a release
//	                   artifact; maps filed before the site is pushed, and the
//	                   release cut records them as `<frontend>.sourcemaps`
//	forge frontend sourcemaps push
//	                   a dist directory some other build produced (the Electron
//	                   renderer, built by CI)
//
// The artifact format is documented in internal/deploytarget/sourcemaps_oci.go.

// sourcemapsRegistryPush stores a packed artifact in the registry. A var so the
// CLI tests state the registry's answer instead of needing a registry.
var sourcemapsRegistryPush = deploytarget.PushSourcemapsArtifact

// sourcemapsHosted reports whether the env's frontend observability is hosted:
// whether browser errors from this env land in the platform's ClickHouse, and
// so whether anything will ever symbolicate them.
//
// It is hosted when the env declares a control plane (there is an org, a
// registry and a credential) and has not opted out of the browser category or
// pointed telemetry at a destination of its own. An env that declares no
// observability at all is hosted: the platform's default is everything on, and
// an absent policy means so (release.ShapeObservabilityPolicy).
func sourcemapsHosted(e *KCLEntities) bool {
	if e == nil || e.ControlPlane == nil || strings.TrimSpace(e.ControlPlane.Endpoint) == "" {
		return false
	}
	o := e.Observability
	if o == nil {
		return true
	}
	if !o.Enabled {
		return false
	}
	if o.Policy != nil && slices.Contains(o.Policy.Disabled, release.ObsCategoryBrowser) {
		return false
	}
	if d := o.Destination; d != nil && !d.TokenFromControlPlane {
		return false
	}
	return true
}

// sourcemapPublisherFor picks where env's maps go: the platform registry when
// its observability is hosted, a local directory otherwise.
func sourcemapPublisherFor(projectDir, env string, e *KCLEntities) deploytarget.SourcemapPublisher {
	if !sourcemapsHosted(e) {
		return deploytarget.LocalSourcemaps{ProjectDir: projectDir, Env: env}
	}
	return deploytarget.RegistrySourcemaps{
		ProjectDir: projectDir,
		Env:        env,
		Base:       func(ctx context.Context) (string, error) { return sourcemapsPushBase(ctx, env, e) },
		Push:       sourcemapsRegistryPush,
	}
}

// sourcemapPublisherForEnv is sourcemapPublisherFor for a caller that holds no
// render of env (publishing a frozen snapshot). An env that cannot be rendered
// here keeps the maps locally rather than failing a publish over them, and says so.
func sourcemapPublisherForEnv(ctx context.Context, env string) deploytarget.SourcemapPublisher {
	projectDir := projectDirForKCL()
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		fmt.Printf("  [sourcemaps] env %q did not render (%v): source maps are kept locally, not filed\n", env, err)
		return deploytarget.LocalSourcemaps{ProjectDir: projectDir, Env: env}
	}
	return sourcemapPublisherFor(projectDir, env, entities)
}

// sourcemapsPushBase is the project's subtree in the platform registry, with
// docker logged in to it. It is resolved at the first publish rather than up
// front: a deploy whose build extracts no map never needs the credential.
func sourcemapsPushBase(ctx context.Context, env string, e *KCLEntities) (string, error) {
	org, err := e.ControlPlane.organization(ctx)
	if err != nil {
		return "", err
	}
	project := hostedProjectName()
	base := hostedimage.PushBase(e.ControlPlane.RegistryHost, org, project)
	if base == "" {
		return "", fmt.Errorf("cannot compose the registry subtree for env %q (organization %q, project %q)", env, org, project)
	}
	if err := loginToPlatformRegistry(ctx, env, platformRegistryHost(e), "", declarationFromEntities(e)); err != nil {
		return "", err
	}
	return base, nil
}

// harvestSourcemapsArtifacts records, in a release being cut, the maps the
// last build of each OnHosted frontend filed in the registry:
// `<frontend>.sourcemaps` (see release.SourcemapsArtifactSuffix), and the build
// id on the frontend's own site artifact, so one release names bundle and maps
// under the same key.
//
// Only OnHosted frontends can be recorded here. They are built by `forge env
// build`, before the cut. A Firebase or bucket frontend is built by `forge env
// deploy`, after its release is frozen, and a ledger entry is immutable: its
// maps are findable by (org, project, frontend, build id) and its record is the
// local state file.
//
// Returns how many maps artifacts were recorded.
func harvestSourcemapsArtifacts(projectDir, env string, e *KCLEntities, out map[string]release.Artifact) int {
	if e == nil {
		return 0
	}
	n := 0
	for _, f := range e.Frontends {
		if !frontendIsHosted(f) {
			continue
		}
		var rec *deploytarget.SourcemapsRecord
		for _, key := range buildStateLookupEnvs(env) {
			if r, err := deploytarget.ReadSourcemapsState(projectDir, key, f.Name); err == nil && r != nil {
				rec = r
				break
			}
		}
		if rec == nil || !rec.Pushed() {
			continue
		}
		out[release.SourcemapsArtifactName(f.Name)] = release.Artifact{
			Kind:    release.KindOCI,
			Mode:    release.ModeShared,
			Digests: map[string]string{release.SharedVariant: rec.Digest},
			Version: rec.BuildID,
			URI:     rec.Repository,
		}
		n++
		site := hostedStaticDestinationForDecl(e, imageRepository(f.Image))
		if a, ok := out[site]; ok && a.Version == "" {
			a.Version = rec.BuildID
			out[site] = a
		}
	}
	return n
}

// ─── forge frontend sourcemaps push ──────────────────────────────────────────

func newFrontendGroupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "frontend",
		Short: "Operate on built frontends",
	}
	cmd.AddCommand(newFrontendSourcemapsCmd())
	return cmdutil.StrictGroup(cmd)
}

func newFrontendSourcemapsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sourcemaps",
		Short: "File a frontend's source maps so its browser errors can be symbolicated",
	}
	cmd.AddCommand(newFrontendSourcemapsPushCmd())
	return cmdutil.StrictGroup(cmd)
}

type sourcemapsPushOptions struct {
	dist, frontend, buildID, env, project, basePath string
	dryRun                                          bool
}

func newFrontendSourcemapsPushCmd() *cobra.Command {
	var opts sourcemapsPushOptions
	cmd := &cobra.Command{
		Use:   "push <dist>",
		Short: "File the source maps of a built directory forge did not build",
		Long: `Extract the source maps from <dist> and file them for a frontend whose build
forge did not run (the Electron renderer, built by CI). Everything forge does for
a frontend it builds itself, it does here: the maps are moved out of a COPY of
<dist> (it is not modified), each bundle's sourceMappingURL trailer is dropped
from that copy, a manifest.json is written, and the result is pushed. The
bundle you ship is yours to strip; this command does not touch it.

WHERE THEY GO is decided by --env, as for every command that talks to a control
plane. When the env's observability is hosted, the maps are pushed to the
platform registry:

  <registry>/<org>/<project>/<frontend>-sourcemaps:<build id>

and otherwise they are kept in .forge/sourcemaps/<env>/<frontend>/<build id>/
and nothing is uploaded. The artifact format is documented in
internal/deploytarget/sourcemaps_oci.go.

THE BUILD ID must be the version the bundle reports to telemetry
(service.version): pass the same value the build baked into the bundle. Maps
filed under any other id are never looked up.

--base-path is where <dist> is mounted in the served site. A file's path in the
manifest is its URL path with the origin removed, so a renderer served from
/app has /app/ in front of every path.

The credential is the env's control-plane token (its token_env), the same one
` + "`forge env build --push`" + ` uses. Exit code is non-zero if nothing was
extracted: a build that was meant to emit maps and did not is a failure here,
not a no-op.

Examples:
  forge frontend sourcemaps push web/dist --frontend desktop --build-id 3f9c0e1a2b4d --env prod
  forge frontend sourcemaps push web/dist --frontend desktop --build-id "$VERSION" --env prod --dry-run`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.dist = args[0]
			return runSourcemapsPush(cmd.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.frontend, "frontend", "", "The frontend the maps belong to (required)")
	f.StringVar(&opts.buildID, "build-id", "", "The build id the bundle reports as service.version (required)")
	f.StringVar(&opts.env, "env", "", "The env whose control plane and observability decide where the maps go (required)")
	f.StringVar(&opts.project, "project", "", "The forge project the frontend belongs to (default: forge.yaml's name)")
	f.StringVar(&opts.basePath, "base-path", "", "Where <dist> is mounted in the served site (default: the root)")
	f.BoolVar(&opts.dryRun, "dry-run", false, "Extract and report; push and keep nothing")
	for _, name := range []string{"frontend", "build-id", "env"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func runSourcemapsPush(ctx context.Context, opts sourcemapsPushOptions) error {
	projectDir := projectDirForKCL()
	project := opts.project
	if project == "" {
		project = hostedProjectName()
	}
	// Fail on a name or id the registry cannot hold BEFORE any extraction work.
	if _, err := release.SourcemapsRepository("registry.invalid/org/project", opts.frontend); err != nil {
		return err
	}
	if _, err := release.SourcemapsTag(opts.buildID); err != nil {
		return err
	}
	build, cleanup, err := deploytarget.ExtractDistSourcemaps(deploytarget.DistSourcemaps{
		Dir: opts.dist, Project: project, Frontend: opts.frontend, BuildID: opts.buildID, BasePath: opts.basePath,
	})
	if err != nil {
		return err
	}
	defer cleanup()
	if build.Maps == 0 {
		return fmt.Errorf("no source maps found under %s: a `.map` file that is a source map, or a bundle with an inline one. "+
			"The build must emit them (the scaffolded vite and next configs do when FORGE_SOURCEMAPS=1)", opts.dist)
	}
	entities, err := RenderKCL(ctx, projectDir, opts.env)
	if err != nil {
		return fmt.Errorf("render env %q: %w", opts.env, err)
	}
	if sourcemapsHosted(entities) {
		fmt.Printf("[sourcemaps] %s: %d source map(s), build id %s, env %s: the platform registry\n", opts.frontend, build.Maps, opts.buildID, opts.env)
	} else {
		fmt.Printf("[sourcemaps] %s: %d source map(s), build id %s, env %s: kept locally (its observability is not hosted)\n", opts.frontend, build.Maps, opts.buildID, opts.env)
	}
	if opts.dryRun {
		fmt.Println("[sourcemaps] --dry-run: nothing pushed or kept")
		return nil
	}
	rec, err := sourcemapPublisherFor(projectDir, opts.env, entities).PublishSourcemaps(ctx, build)
	if err != nil {
		return fmt.Errorf("file source maps for %s: %w", opts.frontend, err)
	}
	deploytarget.ReportSourcemapsRecord("sourcemaps", opts.frontend, rec)
	return nil
}
