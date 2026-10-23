package cli

// Two checks over the images a hosted env declares, both pure and offline.
//
// They answer opposite halves of one question — "can the platform pull this?"
// — and are separate because the remedies differ:
//
//   - checkHostedImagesResolve: a BARE image with no push base to resolve it
//     under. There is nowhere to push, so a build is REFUSED.
//   - hostedOffBaseImageFindings: a HOST-BEARING image outside the push base.
//     The bytes can be pushed, but the control plane admits only its own
//     registry — so this surfaces at render and lint time rather than at
//     publish time, which is where it surfaced before.
//
// The rule, the finding and its wording live in internal/hostedimage, because
// `forge lint` reports the same thing and cannot import this package. These
// functions are the KCLEntities-shaped adapters over it.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/hostedimage"
)

// hostedImageItems is every hosted item of an env that carries an image.
//
// THE REFERENCE THE PLATFORM WOULD PULL IS Spec.Image, NOT Image. The two
// differ on purpose: WorkloadEntity.Image is the registry-less artifact key
// forge BUILDS into (empty for a workload forge does not build), while
// Spec.Image is the resolved reference a runtime pulls — and for a hosted
// workload the render keeps the author's declaration there verbatim. Judging
// Image alone silently skipped every hosted workload whose image CI pushes,
// which is most of them.
func hostedImageItems(e *KCLEntities) []hostedimage.Item {
	if e == nil {
		return nil
	}
	var out []hostedimage.Item
	for _, w := range e.Workloads {
		if w.Runtime.Type != RuntimeHosted {
			continue
		}
		if image := hostedPullReference(w); image != "" {
			out = append(out, hostedimage.Item{Owner: w.Name, Image: image})
		}
	}
	for _, f := range e.Frontends {
		if f.Runtime.Type == RuntimeHosted && f.Image != "" {
			out = append(out, hostedimage.Item{Owner: f.Name, Image: f.Image})
		}
	}
	return out
}

// hostedPullReference is the reference a hosted workload's image resolves to:
// the rendered spec's, falling back to the build artifact key for a workload
// whose runtime resolved none.
func hostedPullReference(w WorkloadEntity) string {
	if w.Spec.Image != "" && w.Spec.Image != "-" {
		return w.Spec.Image
	}
	return w.Image
}

// checkHostedImagesResolve refuses every hosted item whose image names no
// registry host when there is no push base to resolve it under.
//
// Every offending item is reported together: a build refused one at a time is
// a build that takes N attempts to learn N things.
//
// A bare image on any OTHER runtime is not this check's business — KCL refuses
// a bare cluster image at render, and reporting it here too would give one
// mistake two different messages.
func checkHostedImagesResolve(env string, e *KCLEntities, pushBase string) error {
	if e == nil || hostedimage.NormalizeBase(pushBase) != "" {
		return nil
	}
	var errs []error
	for _, it := range hostedImageItems(e) {
		if !hostedimage.HasRegistryHost(hostedimage.Repository(it.Image)) {
			errs = append(errs, errHostedImageNeedsPushBase(env, it.Owner, it.Image))
		}
	}
	return errors.Join(errs...)
}

// hostedOffBaseImageFindings is every host-bearing image on a hosted item of
// this env, judged against the env's push base when it is known.
//
// THE BASE IS RESOLVED ONLY WHEN THERE IS A HOSTED IMAGE TO JUDGE. Composing
// it asks the control plane which organization the credential acts for
// (hosted_org.go), and with nothing hosted no answer could change the result.
// It used to be resolved first, unconditionally, so `forge env render prod` —
// control-plane's prod declares a control plane and hosts nothing — called
// admin.reliantapi.com from a script that was meant to be hermetic.
func hostedOffBaseImageFindings(e *KCLEntities) []hostedimage.Finding {
	items := hostedImageItems(e)
	if len(items) == 0 {
		return nil
	}
	return hostedimage.OffBase(items, platformPushBase(e))
}

// verifiedOffBaseImages is the subset forge actually PROVED is off-base.
func verifiedOffBaseImages(findings []hostedimage.Finding) []hostedimage.Finding {
	return hostedimage.Verified(findings)
}

// errHostedImagesOffBase is the render-time REFUSAL for a hosted env, built
// from the same findings lint reports.
//
// Error on render, finding on lint, for one reason: `forge env render <env>`
// is what the deploy applies, so printing manifests the platform will refuse
// makes the render a document nobody can act on. `forge lint` judges a whole
// project, including envs nobody is deploying right now, where the same fact
// is something to fix rather than something to stop for.
func errHostedImagesOffBase(env string, findings []hostedimage.Finding) error {
	if len(findings) == 0 {
		return nil
	}
	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		lines = append(lines, "  "+f.Message())
	}
	return fmt.Errorf("env %q: %d hosted image(s) the control plane would refuse to publish:\n%s",
		env, len(findings), strings.Join(lines, "\n"))
}
