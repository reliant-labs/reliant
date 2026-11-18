package hostedimage

// What a DENIED push to the platform registry means, said in terms of the
// credential rather than the protocol.
//
// WHY THIS EXISTS. The registry answers a refused push with
// `denied: requested access to the resource is denied`, which reads as a
// credential problem but not WHICH one. forge says what it knows.

import (
	"errors"
	"fmt"
	"strings"

	"oras.land/oras-go/v2/registry/remote/errcode"
)

// IsDenied reports whether err is a registry refusal to let this credential
// write here — as opposed to a network fault, a missing credential store, or
// a bad reference.
//
// Both transports forge pushes over are covered, because both reach the same
// registry and the author cannot be expected to care which one carried their
// image:
//
//   - oras (bundles, static-site releases) returns a typed
//     *errcode.ErrorResponse, so the status is read rather than matched.
//   - `docker push` is a subprocess whose exit status is just "failed", so
//     the only signal is the daemon's own message on the pipe. Matched on the
//     OCI distribution error CODE (`denied`, `UNAUTHORIZED`), which is part
//     of the spec rather than of docker's phrasing.
func IsDenied(err error, output string) bool {
	if err == nil {
		return false
	}
	var resp *errcode.ErrorResponse
	if errors.As(err, &resp) {
		return resp.StatusCode == 401 || resp.StatusCode == 403
	}
	lower := strings.ToLower(output + " " + err.Error())
	return strings.Contains(lower, "denied") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "authentication required")
}

// DeniedHint is the sentence to append to a refused push.
//
// The registry's realm scopes a credential to its OWN organization's subtree,
// and forge composes the push address from that same organization (it asks the
// control plane which one the token acts for). So a denial here is never a
// mis-declared org: it is a credential that has expired, or one that holds
// deploy:read but not the deploy:write a push needs.
func DeniedHint(reference string) string {
	return fmt.Sprintf("The registry refused the push to %s.\n"+
		"  forge composed that address from the organization your credential acts for, so the "+
		"likely causes are a credential that has expired, or one that carries deploy:read but not "+
		"deploy:write (a push needs both the organization's subtree and write access).\n"+
		"  fix: re-run `forge registry login <env>`, and check the token's scopes with `forge cloud status <env>` or in the web app (Settings → API tokens)", reference)
}

// PushError is a push to Repository that failed. Its text is the cause's,
// unchanged; it exists so that a message composed further out — where several
// pushes share one error path — can name the repository that actually
// refused instead of the one the caller was about to push.
type PushError struct {
	Repository string
	Err        error
}

func (e *PushError) Error() string { return e.Err.Error() }

func (e *PushError) Unwrap() error { return e.Err }

// FailedRepository is the repository of the PushError in err's chain, or ""
// when err carries none.
func FailedRepository(err error) string {
	var pe *PushError
	if errors.As(err, &pe) {
		return pe.Repository
	}
	return ""
}

// ClaimableDeniedHint is DeniedHint for an anonymous (--claimable) deploy.
//
// None of DeniedHint's remedies exist there: the author has no login, no API
// token and no scopes to inspect. The deploy's credential is minted for the
// deploy and pushed by forge alone, and the platform revokes it when the
// deploy is claimed, taken down or expires — so those are what to check.
func ClaimableDeniedHint(reference string) string {
	return fmt.Sprintf("The registry refused the push to %s.\n"+
		"  This is an anonymous (--claimable) deploy: forge pushed with the deploy's own credential, for this run only, "+
		"so no login or API token of yours is involved. That credential stops working once the deploy is claimed, "+
		"taken down or expires; `forge env status <env> --claimable` says which.\n"+
		"  fix: if it was claimed, sign in as the account that claimed it (`forge login`) and deploy without --claimable. "+
		"If it is still live, the refusal is a forge defect: report it with `forge report-bug`", reference)
}
