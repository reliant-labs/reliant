package cli

// The registry push-access preflight: before a pushing build builds anything,
// ask the platform registry's token realm whether the credential docker now
// holds for it may push to the repositories this build's in-process (oras)
// pushes will write — each hosted static site's release repository and its
// source-map repository. A refusal used to arrive after the Go, Docker and
// frontend builds, at the first site push; now it arrives first, naming the
// repository and the fix. The probe pushes nothing
// (deploytarget.ProbePushAccess has the method and why it can only refuse on
// an authoritative answer).
//
// WHERE IT RUNS. In preflightRealBuild, after renderBuildInputs has logged
// docker in (autoLoginForPush). That is the one point every pushing build —
// `forge env build --release`, `forge env deploy <env>` and its --plan-only
// (which builds and cuts as usual), and an anonymous `--claimable` deploy —
// passes with the credential its pushes will present, and before any of them
// builds. `forge env deploy --claimable --plan-only` cannot ask: it holds no
// identity by design, and the anonymous credential exists only once the real
// deploy has minted it (it says so).
//
// The image pushes are left to `docker push`: this closes the gap where the
// in-process pushes read a credential the image push did not.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/hostedimage"
	"github.com/reliant-labs/forge/pkg/release"
)

// registryPushProbe asks a registry's realm about push access. A var so a
// test states the realm's answer; TestMain makes every other test's answer
// "not probed", so none reaches a real registry.
var registryPushProbe = deploytarget.ProbePushAccess

// preflightRegistryPushAccess refuses a pushing build whose credential the
// platform registry would refuse for one of its in-process pushes. An answer
// that is not authoritative is a note, and the build proceeds.
func preflightRegistryPushAccess(ctx context.Context, out io.Writer, entities *KCLEntities, opts buildOptions) error {
	host, repositories := registryPushProbeTargets(entities, opts)
	if len(repositories) == 0 {
		return nil
	}
	res := registryPushProbe(ctx, host, repositories)
	switch res.Verdict {
	case deploytarget.PushAccessGranted:
		fmt.Fprintf(out, "[build] registry push access verified on %s: %s\n", host, strings.Join(repositories, ", "))
		return nil
	case deploytarget.PushAccessDenied:
		return registryPushDeniedError(res, repositories)
	default:
		fmt.Fprintf(out, "[build] note: registry push access was not verified before the build (%s); the push itself decides\n", res.Reason)
		return nil
	}
}

// registryPushProbeTargets is the platform registry host and the repositories
// on it this build's in-process pushes write: every hosted static site's
// release repository (the address pushHostedStaticSite pushes to) and, when
// its maps are filed in the registry, its source-map repository. Maps are
// filed under the platform's push base even for a site whose own image names
// another registry, so they are asked about either way; a repository on
// another host is not, since this realm cannot answer for it.
func registryPushProbeTargets(entities *KCLEntities, opts buildOptions) (string, []string) {
	if !opts.pushPlan.push || entities == nil || entities.ControlPlane == nil {
		return "", nil
	}
	host := strings.TrimSpace(entities.ControlPlane.RegistryHost)
	if host == "" {
		return "", nil
	}
	sites := hostedStaticSites(entities, opts)
	mapsHosted := sourcemapsHosted(entities) && opts.pushPlan.pushBase != ""
	var repositories []string
	add := func(r string) {
		if registryHost(r) == host && !containsString(repositories, r) {
			repositories = append(repositories, r)
		}
	}
	for _, f := range sites {
		add(hostedStaticDestination(opts.pushPlan.pushBase, f.Image))
		if mapsHosted && f.Sourcemaps != deploytarget.SourcemapsOff {
			if r, err := release.SourcemapsRepository(opts.pushPlan.pushBase, f.Name); err == nil {
				add(r)
			}
		}
	}
	return host, repositories
}

// registryPushDeniedError is the refusal, with the same remedy the push's own
// refusal prints (deniedPushHint), for the first repository refused.
func registryPushDeniedError(res deploytarget.PushAccess, repositories []string) error {
	reference := repositories[0]
	if len(res.Missing) > 0 {
		reference = res.Missing[0]
	}
	hint := hostedimage.DeniedHint(reference)
	if cloud.ClaimableActive() {
		hint = hostedimage.ClaimableDeniedHint(reference)
	}
	return exitCodeError{code: exitRefused, msg: fmt.Sprintf("registry push access: %s. Nothing was built or pushed.\n\n%s", res.Reason, hint)}
}
