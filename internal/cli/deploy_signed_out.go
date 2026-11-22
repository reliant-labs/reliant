package cli

// A SIGNED-OUT HOSTED DEPLOY NAMES THE NO-ACCOUNT DEPLOY.
//
// `forge env deploy <env>` with no credential used to end on the resolver's
// generic list, whose last line named `forge env deploy <env> --claimable`
// with a placeholder, under the standalone binary's name, whatever the env's
// shape. An agent with no account reads that failure to decide what to run
// next, so the failure says it concretely: this env, this binary's name, and
// only when the env's shape could deploy that way. The --json document
// carries the same commands in next_actions; its error_code and error_reason
// stay the missing credential's.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
)

// signedOutDeployError is a hosted deploy's failure to resolve a credential,
// with the commands that move past it. It wraps the resolver's error, so
// errors.Is(err, cloud.ErrNoCredential) and the document's error facts are
// the wrapped error's.
type signedOutDeployError struct {
	err error
	// hint is the human line naming the no-account deploy; "" when the env
	// cannot deploy that way.
	hint string
	// next are the commands that move past the failure, best first.
	next []string
}

// Error is the resolver's message with its generic no-account line replaced
// by this env's: the concrete command when the env fits, nothing when it
// does not (the generic line would send the reader to a deploy that is
// refused).
func (e *signedOutDeployError) Error() string {
	msg := strings.Replace(e.err.Error(), "\n"+cloud.NoAccountHint, "", 1)
	if e.hint != "" {
		msg += "\n" + e.hint
	}
	return msg
}

func (e *signedOutDeployError) Unwrap() error { return e.err }

// NextActions are the --json document's next_actions (errorFactsOf).
func (e *signedOutDeployError) NextActions() []string { return e.next }

// signedOutDeploy words err for a hosted deploy of env when it is a missing
// credential (error_reason no_control_plane_credential) and returns any other
// error unchanged: a credential helper's or the control plane's own refusal
// names its own fix.
func signedOutDeploy(env string, e *KCLEntities, err error) error {
	var already *signedOutDeployError
	if err == nil || errors.As(err, &already) || errorFactsOf(err).ErrorReason != ReasonNoControlPlaneCredential {
		return err
	}
	claimable := fmt.Sprintf("%s env deploy %s --claimable", Name(), env)
	if declaresClaimable(e) {
		// The env declares itself anonymous: a signed-in deploy of it is
		// refused (refuseClaimableEnvSignedIn), so signing in moves nothing
		// and --claimable is the one way forward whatever the shape —
		// `--claimable` itself says what does not fit the plan.
		return &signedOutDeployError{
			err: err,
			hint: fmt.Sprintf("env %q is declared for an anonymous deploy (control_plane = forge.ControlPlane {claimable = True}): "+
				"deploy it with %s (a live URL plus a 72-hour claim link for your user)", env, claimable),
			next: []string{claimable},
		}
	}
	login := loginThenDeploy(env)
	if !claimableCouldDeploy(env, e) {
		return &signedOutDeployError{err: err, next: []string{login}}
	}
	return &signedOutDeployError{
		err:  err,
		hint: fmt.Sprintf("or deploy with no account: %s (a live URL plus a 72-hour claim link for your user)", claimable),
		next: []string{claimable, login},
	}
}

// claimableCouldDeploy reports whether env's rendered shape fits an anonymous
// deploy: everything it publishes is hosted, and the claimable plan
// (claimableIneligibility: one static site, or one service with its
// migration job and one database) admits it.
//
// It asks the control plane nothing. The caps are the ones this process
// already holds, else forge's compiled-in copy; `--claimable` itself checks
// the platform's published caps before it builds, and says what does not fit.
func claimableCouldDeploy(env string, e *KCLEntities) bool {
	if e == nil || e.ControlPlane == nil || !e.HasHosted() || envAppliesLocally(e) {
		return false
	}
	return len(claimableIneligibility(e, claimableLimitsFor(env, e).limits)) == 0
}
