// Package devidp is the sign-in broker's client for a Zitadel instance: it
// drives the OIDC authorization-code flow with PKCE on the server's behalf
// (BeginFlow, SignIn, SignUp, RedeemCode), creates and finalizes sessions with
// the login broker's token, and resets passwords. The token may come from a
// file that is re-read on each use, so a rotated credential needs no restart.
//
// It does NOT converge an instance. Registering the project, applications,
// roles, users and the broker account is pkg/identity's job: `forge env up`
// runs it against the dev IdP and the control plane runs it against hosted
// instances.
package devidp
