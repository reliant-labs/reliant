package cli

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/hostedimage"
)

// An image registry is DECLARED on a WORKLOAD, as part of its image, and
// nowhere else. Not on the env, not on a flag, not in a `-D` binding forge
// owns, not in an environment variable or a forge.yaml key.
//
// `forge env build <env> --push` is a switch — "push these builds" — and each
// build's destination is the reference its own workload declares, which is the
// same reference `forge env deploy` pulls. They agree by construction rather
// than because a CI script restated a registry correctly, and two workloads in
// one env can ride two different registries without forge needing a concept
// for that.

// imageDestination is one built image and where it goes: the repository its
// workload declared (registry host included) and the workload that declared
// it, for error messages and the build header.
type imageDestination struct {
	// repository is the push target: `<host>/<path>`, no tag or digest.
	repository string
	// workload names the declaration it came from.
	workload string
}

// host is the registry host `docker login` authenticates against for this
// destination.
func (d imageDestination) host() string { return registryHost(d.repository) }

// pushPlan is where a build's images go, derived from the env's images.
type pushPlan struct {
	// push is true when this build pushes at all. False means the images are
	// built and tagged locally and nothing leaves the machine.
	push bool
	// destinations is one entry per distinct declared repository, sorted, so
	// the header and `forge registry login` enumerate them deterministically.
	destinations []imageDestination
	// env is the environment these images belong to, for the header.
	env string
	// pushBase is the platform registry subtree a BARE hosted image was
	// resolved under (resolveHostedImageBase), or "" when the env has no
	// control plane or declares no organization. Recorded on the plan so a
	// consumer can say WHERE a resolved reference came from rather than
	// leaving the author to guess which half of it they wrote.
	pushBase string
}

// printHeader prints where this build's images are tagged and pushed. One
// line per distinct registry, because there can legitimately be several.
func (p pushPlan) printHeader() {
	if len(p.destinations) == 0 {
		return
	}
	verb := "tagged locally, not pushed"
	if p.push {
		verb = "pushed"
	}
	for _, d := range p.destinations {
		fmt.Printf("[build]   Image:    %s (declared by workload %q; %s)\n", d.repository, d.workload, verb)
	}
}

// repositoryFor is the declared repository a built artifact goes to.
//
// The build knows an artifact by its NAME — the project name for the project
// image, a frontend's name for a frontend image — while a declaration names a
// full reference. They are joined on the reference's last path segment, which
// is the repository's own name: `ghcr.io/acme/shop` IS the artifact `shop`.
//
// Falls back to the bare name when no workload declares it. That is not a
// silent mis-push: a bare name has no registry host, so nothing pushes it, and
// the image is tagged locally exactly as an unbound artifact should be. The
// render is what refuses a bare image on a runtime that must pull one.
func (p pushPlan) repositoryFor(name string) string {
	for _, d := range p.destinations {
		if repositoryName(d.repository) == name {
			return d.repository
		}
	}
	return name
}

// hostedStaticDestination is the ONE rule for where a hosted frontend's site
// release is pushed: the declared reference resolved against the platform's
// push base, with the platform's layout segment appended to the RESULT.
//
// Both halves call it — declaredImageDestinations, so the push plan and
// `forge registry login` name it, and buildHostedStaticSites, so the push goes
// there. That is deliberate and it is not a lookup: a bare image needs the
// base composed on, and recomposing it independently in the build is the
// "two derivations of one address" defect the release-ref rule exists to
// prevent. It pushed `web/static.v1`, with no registry at all, while the
// release and the published spec named the resolved address.
//
// Order matters: the base is composed BEFORE the layout segment, or a bare
// `web` yields `<base>/web/static.v1` versus `web/static.v1` — and only the
// first is pullable.
func hostedStaticDestination(pushBase, image string) string {
	return deploytarget.HostedStaticRepository(imageRepository(resolveHostedImageBase(pushBase, image)))
}

// hostedStaticDestinationForDecl is hostedStaticDestination for the callers
// that hold an env's RENDER rather than a resolved plan: the
// release-coverage gate, the hosted deploy group, and the build plan's
// preview.
//
// They must agree with the build's push to the byte, because the address is
// the ledger's KEY: the coverage gate looks the artifact up under it, the
// deploy pins `<it>@<digest>`, and cp's operator pulls exactly what the spec
// records. Before ADR-0003 F1 each derived it from the declared reference and
// they agreed by accident; once a bare image needs the platform's base
// composed on, agreeing by accident stops working — which is what this
// function exists to prevent.
func hostedStaticDestinationForDecl(e *KCLEntities, image string) string {
	return hostedStaticDestination(platformPushBase(e), image)
}

// hostedImageForDecl resolves any hosted item's image against the env's
// declared push base. The workload twin of hostedStaticDestinationForDecl.
func hostedImageForDecl(e *KCLEntities, image string) string {
	return resolveHostedImageBase(platformPushBase(e), image)
}

// repositoryName is a repository's last path segment — the artifact name
// (`ghcr.io/acme/shop` → `shop`).
func repositoryName(repository string) string {
	if slash := strings.LastIndex(repository, "/"); slash >= 0 {
		return repository[slash+1:]
	}
	return repository
}

// hosts is the set of distinct registry hosts these destinations name, sorted.
// What `forge registry login` authenticates against.
func (p pushPlan) hosts() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range p.destinations {
		h := d.host()
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// renderBuildInputs renders the env (renderBuildEntities) and resolves where
// each image goes from the FULL render — a --target narrowing can drop the
// workload whose image the header should name. The resolved plan is written
// back into opts.pushPlan so every downstream push reads one resolution.
// Returns the narrowed entity set the build acts on.
//
// It is also where a hosted build AUTHENTICATES (ADR-0003 F3), and that
// placement is the point: the plan is resolved, so forge knows whether this
// invocation pushes and to which hosts, and nothing has been compiled yet, so
// a missing credential costs a second rather than a whole build. Every pushing
// verb comes through here — `forge env build <env> --push`, `forge env up`,
// and `forge env deploy`, which builds through runBuild — so there is one
// hook rather than one per push path.
func renderBuildInputs(ctx context.Context, cfg *config.ProjectConfig, opts *buildOptions) (*KCLEntities, pushPlan, error) {
	declared, entities, err := renderBuildEntities(ctx, cfg, *opts)
	if err != nil {
		return nil, pushPlan{}, err
	}
	plan, err := resolvePushPlan(*opts, declared)
	if err != nil {
		return nil, pushPlan{}, err
	}
	// --plan preflights and writes nothing, so it must not authenticate:
	// a dry run that fails for want of a credential is a dry run that
	// cannot be used on a laptop with no login.
	if !opts.plan {
		if err := autoLoginForPush(ctx, opts.env, declared, plan); err != nil {
			return nil, pushPlan{}, err
		}
	}
	opts.pushPlan = plan
	return entities, plan, nil
}

// resolvePushPlan is the ONE place a build decides where its images go: each
// workload's declared image, or a runbook naming the workload with no usable
// one. There is no other source and no precedence to reason about.
//
// declared is the env's FULL render, before --target narrowing. nil when there
// is no env or the env has no KCL directory.
//
// `forge env up` (opts.pushIfDeclared) pushes to the same declarations, but an
// env whose workloads declare no pullable image builds locally instead of
// failing: a host-only env has no cluster to pull from.
func resolvePushPlan(opts buildOptions, declared *KCLEntities) (pushPlan, error) {
	// The platform's push base: the declared registry host, the org the
	// credential acts for, and the project. A bare hosted image resolves
	// under it; an env with no hosted item never reads it.
	//
	// The org is resolved STRICTLY here, because resolving a push destination
	// for a hosted env is the first thing a build does and a missing
	// credential should be said before anything is compiled. A --plan
	// preflight resolves nothing: it must run on a laptop with no login.
	if !opts.plan {
		if err := requireOrg(context.Background(), declared); err != nil {
			return pushPlan{}, err
		}
	}
	pushBase := platformPushBase(declared)
	// A --plan with no credential has no org and so no base for a bare hosted
	// image: that is not a refusal, it is a preflight that cannot resolve what
	// it was never allowed to learn. The pushing run refuses (above).
	if !(opts.plan && pushBase == "") {
		if err := checkHostedImagesResolve(opts.env, declared, pushBase); err != nil {
			return pushPlan{}, err
		}
	}
	dests := declaredImageDestinationsWithBase(declared, pushBase)
	plan := pushPlan{env: opts.env, destinations: dests, pushBase: pushBase}
	if !opts.push {
		plan.push = opts.pushIfDeclared && opts.env != "" && len(dests) > 0
		return plan, nil
	}
	if opts.env == "" {
		return pushPlan{}, errPushNeedsEnv()
	}
	if len(dests) == 0 {
		return pushPlan{}, noPushableImagesError(fmt.Sprintf("forge env build %s --push", opts.env), opts.env, declared)
	}
	plan.push = true
	return plan, nil
}

// resolveHostedImageBase is the ONE rule for what a bare image on a hosted
// item resolves to (ADR-0003 F1): `<image_push_base>/<name>`.
//
// WHY A BARE IMAGE IS NOW LEGITIMATE HERE, AND ONLY HERE. The registry is
// declared on the workload and nowhere else, which is right for every runtime
// whose registry the AUTHOR chooses. forge.OnHosted is the one case where they
// do not choose it: the control plane admits images from exactly one subtree,
// `<registry_base>/<org>` (the org the credential acts for), and refuses everything else (checkImagePushBase).
// So a hosted author writing a host was transcribing a value the platform
// already knew, and getting it wrong produced a publish-time refusal — the
// defect this closes. A host-bearing reference is still used VERBATIM, because
// an author who named one meant it, and the admit check is what judges it.
//
// base "" means the credential's org was not learned, so no base composes. That
// is NOT a licence to invent one: a bare image with nowhere to go is refused,
// naming the credential to supply (errHostedImageNeedsPushBase).
//
// Non-hosted runtimes are untouched. A bare image on a cluster workload is
// still refused at render, by KCL, because no platform owns that registry.
func resolveHostedImageBase(base, image string) string {
	return hostedimage.ResolveBase(base, image)
}

// declaredImageDestinations is every distinct repository the env's workloads
// declare for an image forge BUILDS — the single resolution `forge build
// --push`, `forge registry login`, `forge registry ref` and `forge env up`
// share.
//
// Two filters, each closing a way this could ask for a credential nothing
// needs:
//
//  1. Only a workload forge BUILDS contributes. A third-party image
//     (`docker.io/library/nats:2.10`) is pulled, never pushed, so including it
//     would have `forge registry login` demand docker.io credentials.
//  2. Only a workload on a runtime that involves a REGISTRY contributes. A
//     workload bound OnHost or OnCompose runs from the local filesystem or a
//     compose file — nothing pulls an image for it, so its declared reference
//     is not a push destination in that env. This is why `forge registry login
//     dev` on a host-only dev env correctly reports nothing to log in to,
//     even though those workloads carry perfectly good references for the envs
//     that DO deploy them.
//
// A hosted FRONTEND counts too. Its static release is pushed to a registry
// exactly as a backend image is — it just lands under the platform's own layout
// segment, which forge appends rather than asking the author for. An env whose
// only publishable thing is a hosted site still has a push destination, and
// omitting it made `forge env build <env> --push` refuse a project that had one.
//
// A BARE image on a hosted item resolves under pushBase
// (resolveHostedImageBase); pushBase "" leaves it bare, which keeps it out of
// the destination set exactly as before, and the caller that needs to REFUSE
// that reports it (checkHostedImagesResolve).
//
// Sorted by repository so every consumer enumerates the same order.
func declaredImageDestinations(e *KCLEntities) []imageDestination {
	return declaredImageDestinationsWithBase(e, "")
}

func declaredImageDestinationsWithBase(e *KCLEntities, pushBase string) []imageDestination {
	if e == nil {
		return nil
	}
	seen := map[string]string{}
	add := func(repo, owner string) {
		if registryHost(repo) == "" {
			// No host: not a pushable destination. The render refuses a
			// hostless image on any runtime that PULLS one, so reaching here
			// means the declaration belongs to something that pushes nothing.
			return
		}
		if _, dup := seen[repo]; !dup {
			seen[repo] = owner
		}
	}
	for _, w := range e.Workloads {
		if w.Build.Type == "" || w.Image == "" {
			continue
		}
		switch w.Runtime.Type {
		case RuntimeHost, RuntimeCompose:
			continue
		}
		image := imageRepository(w.Image)
		if w.Runtime.Type == RuntimeHosted {
			image = resolveHostedImageBase(pushBase, image)
		}
		add(image, w.Name)
	}
	for _, f := range e.Frontends {
		if f.Image == "" || f.Runtime.Type != RuntimeHosted {
			continue
		}
		// One rule, shared with the build's own push
		// (hostedStaticDestination), so the plan and the push cannot name
		// two different addresses.
		add(hostedStaticDestination(pushBase, imageRepository(f.Image)), f.Name)
	}
	out := make([]imageDestination, 0, len(seen))
	for repo, workload := range seen {
		out = append(out, imageDestination{repository: repo, workload: workload})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repository < out[j].repository })
	return out
}

// imageRepository strips any `:tag` / `@digest` from an image reference,
// keeping the registry host. The Go twin of kcl/lib/images.k's `repository`,
// and the key build state and the release ledger record — so a build and a
// deploy name the same repository by applying the same rule.
func imageRepository(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	lastSlash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > lastSlash {
		return image[:colon]
	}
	return image
}

// buildEnvArgRe is the shape of every env name (validateEnvName's rule): a
// deploy/kcl/<env>/ directory and a KCL identifier segment.
var buildEnvArgRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// validateBuildEnvArg refuses a `forge build` positional that cannot be an
// env name. Without it, a registry passed as --push's value the way older
// forges took it (ghcr.io/acme, written with a space) parses as a bool --push
// plus an ENV named "ghcr.io/acme", renders nothing for it, and would build
// under a nonsense env instead of saying the registry belongs on the workload.
func validateBuildEnvArg(arg string) error {
	if buildEnvArgRe.MatchString(arg) {
		return nil
	}
	if strings.ContainsAny(arg, ".:/") {
		return cliutil.UserErr("forge build",
			fmt.Sprintf("%q is not an environment name — it looks like an image registry, and forge build takes no registry", arg),
			"",
			"the registry is part of a workload's image: set `image = \"<registry>/<name>\"` on the workload in "+
				"deploy/kcl/workloads.k and run forge env build <env> --push")
	}
	return cliutil.UserErr("forge build",
		fmt.Sprintf("invalid environment name %q", arg),
		"",
		"an env name is lowercase letters, digits and hyphens, starting with a letter — one deploy/kcl/<env>/ directory")
}

// errPushNeedsEnv is --push with no environment argument: without an env there
// is nothing to render, so no workload's image can be read.
func errPushNeedsEnv() error {
	return cliutil.UserErr("forge env build --push",
		"--push pushes each built image to the reference its workload declares, and no environment argument was given",
		"",
		"name the env whose workloads to build: forge env build <env> --push")
}

// noPushableImagesError is the runbook for an env with nothing to push,
// shaped by WHY it has nothing. context is the command that needed a
// destination (`forge env build prod --push`, `forge registry login prod`).
func noPushableImagesError(context, env string, declared *KCLEntities) error {
	mainK := fmt.Sprintf("deploy/kcl/%s/main.k", env)
	workloadsK := "deploy/kcl/workloads.k"
	switch {
	case declared == nil:
		return cliutil.UserErr(context,
			fmt.Sprintf("env %q has no %s, so it declares no workloads and no images", env, mainK),
			"",
			fmt.Sprintf("create the env (forge env new %s), and declare each workload's image in %s", env, workloadsK))
	case len(declared.Workloads) == 0:
		return cliutil.UserErr(context,
			fmt.Sprintf("env %q declares no workloads, so there is no image to push", env),
			mainK,
			fmt.Sprintf("bind a workload in %s — the env names what IT runs", mainK))
	default:
		// Distinguish the two shapes, because the fix is different. Every
		// workload bound OnHost / OnCompose is not a misconfiguration at all —
		// nothing pulls an image for those, so there is genuinely nothing to
		// push, and telling the author to declare an image would send them to
		// fix a file that is already correct.
		if !hasRegistryBoundWorkload(declared) {
			return cliutil.UserErr(context,
				fmt.Sprintf("env %q runs every workload on the host or under compose, so nothing pulls an image and there is nothing to push", env),
				mainK,
				fmt.Sprintf("this is not a misconfiguration: bind a workload to forge.OnCluster or forge.OnHosted in %s "+
					"if it should ship an image, or run this against the env that deploys it", mainK))
		}
		return cliutil.UserErr(context,
			fmt.Sprintf("no workload in env %q declares an image forge builds AND pushes", env),
			workloadsK,
			"set `image` on each workload forge builds, with its registry in it — e.g. "+
				"`image = \"ghcr.io/<owner>/<name>\"`. That one reference is where the build pushes and where "+
				"the deploy pulls, so they cannot disagree.")
	}
}

// hasRegistryBoundWorkload reports whether any workload is bound to a runtime
// that pulls an image from a registry.
func hasRegistryBoundWorkload(e *KCLEntities) bool {
	if e == nil {
		return false
	}
	for _, w := range e.Workloads {
		switch w.Runtime.Type {
		case RuntimeCluster, RuntimeHosted:
			return true
		}
	}
	return false
}
