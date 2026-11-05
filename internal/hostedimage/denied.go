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
		"  fix: re-run `forge registry login <env>`, and check the token's scopes with `forge cloud token list`", reference)
}
