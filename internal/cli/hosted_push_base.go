package cli

// Resolving an env's platform push base.
//
// There is one rule — `<registry_host>/<org>/<project>` — and it lives in
// internal/hostedimage.PushBase. The registry host and the project come from
// the declaration; the org comes from the CREDENTIAL (hosted_org.go). What
// lives here is the KCLEntities-shaped adapter over it, plus the errors a
// caller needs when a base cannot be composed.
//
// EVERY CONSUMER READS THIS, AND NOTHING ELSE. The build, the deploy, the
// release-coverage gate, `forge env render` and `forge lint` all resolve a
// hosted address through this one function, so the address a build pushes to is
// the address a deploy pins and the address a render judges.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/hostedimage"
)

// platformPushBase is the platform registry subtree this env's hosted
// artifacts go to: the declared registry host, the org the credential acts for,
// and the project.
//
// "" when the env declares no control plane, or its org is not knowable right
// now (no credential, or the control plane did not answer). Both are
// legitimate states for this BEST-EFFORT read — render and lint run with no
// credential — so neither is an error here. The authenticated entry points
// resolve the org strictly first (requireOrg), which is where a missing
// credential is reported.
func platformPushBase(e *KCLEntities) string {
	if e == nil || e.ControlPlane == nil {
		return ""
	}
	return hostedimage.PushBase(e.ControlPlane.RegistryHost, e.ControlPlane.knownOrganization(), hostedProjectName())
}

// deniedPushHint is the realm-mismatch explanation to append to a failed
// push, or "" when the failure was not a refusal.
//
// It returns a SUFFIX rather than wrapping, so each push path keeps its own
// error shape and the hint is purely additive — a push that failed for any
// other reason reads exactly as it did before. output is the subprocess
// output for a `docker push` and "" for an in-process oras push, which
// carries its status in the error itself.
func deniedPushHint(err error, output, reference string) string {
	if !hostedimage.IsDenied(err, output) {
		return ""
	}
	return "\n\n" + hostedimage.DeniedHint(reference)
}

// dockerPush runs `docker push <reference>`, streaming the daemon's output
// through as it always did, and appends the realm-mismatch hint when the
// registry refused it.
//
// ONE helper for every `docker push` forge makes, which is the only way the
// hint can be reliable: there were four copies of this loop, and a hint added
// to three of them would be a hint that appears or not depending on which
// artifact failed — the least debuggable possible behaviour.
//
// The output is TEE'd rather than captured: the author must still see
// docker's own progress and message in real time, and the copy exists only so
// IsDenied can read the distribution error code off it. A subprocess's exit
// status is just "failed", so that text is the only signal available.
func dockerPush(ctx context.Context, reference string) error {
	var captured bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "push", reference)
	cmd.Stdout = io.MultiWriter(os.Stdout, &captured)
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	err := cmd.Run()
	if err == nil {
		return nil
	}
	return fmt.Errorf("docker push %s: %w%s", reference, err,
		deniedPushHint(err, captured.String(), reference))
}

// errHostedImageNeedsPushBase is a bare hosted image in an env that resolves no
// push base: forge could not learn which organization the credential acts for,
// so there is no registry subtree to compose the image under.
//
// The remedy is a credential, NOT a declaration and NOT a full reference: a
// hosted author's registry is the platform's, so telling them to transcribe a
// host would be telling them to restate a value forge already knows. It names
// the workload rather than the rule, since a project may declare several and
// only one of them is bare.
func errHostedImageNeedsPushBase(env, owner, image string) error {
	return fmt.Errorf("workload %q declares image %q, which names no registry host, and it is bound to forge.OnHosted.\n"+
		"  forge resolves it to %s/<your org>/<project>/%s, but it could not learn your org: that comes from your control-plane credential.\n"+
		"  fix: authenticate (%s, or set the env's token_env) and retry — env %q reads its org from that credential",
		owner, image, hostedimage.DefaultRegistryHost, image, cloud.SignInHint(), env)
}
