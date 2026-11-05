// Package identity converges the CONTENT of one Zitadel instance to a declared
// Spec: the project, its roles, the OIDC applications (web, SPA, native and the
// native-sign-in broker app), machine users and their keys, the login /
// password-complexity / lockout policies, SMTP, declared role members and (dev
// and preview only) seed users.
//
// It is the one library behind both halves of managed identity. `forge env up`
// runs it against the dev instance with a bootstrap PAT; the control plane runs
// it against each customer instance with that instance's owner machine key. The
// same code path in both is what makes dev parity real.
//
// # What it deliberately is not
//
// It does not create or delete INSTANCES (that is the System API, the control
// plane's job) and it speaks plain JSON over HTTP. It imports no Zitadel Go
// package: Zitadel is AGPL-3.0 and this library must stay on the permissive
// side of that boundary. Stdlib plus forge packages only.
//
// # Idempotent by construction
//
// Every step reads first, creates only what is missing and repairs drift only
// where that is safe. A second run against a converged instance performs no
// write at all (Report.Changed is false), which is the property the
// reconciler's periodic sweep depends on. Objects are matched by their natural
// key — project name, role key, application name, username, email — never by
// an internal id, because the ids are generated and a declaration cannot name
// them.
//
// # Adoption
//
// An instance that already holds content (one created by hand) is converged in place.
// The natural-key match above is what keeps its objects: nothing is recreated
// because its internal id is not the one a fresh instance would have produced.
// Where another system holds an id (the project id is the token audience; the
// client id is baked into a deployed server's config), pin it with
// ProjectSpec.AdoptID / Application.AdoptClientID: a pin turns "the match
// diverged from what is deployed" into an error instead of a silent outage, and
// finds the object under another name (a console rename) and renames it back.
// Options.Adopt additionally forbids creating the project or an application at
// all. Options.DryRun reports the writes a pass would make, so an adoption can
// be reviewed before it runs.
//
// # Seams
//
// KeyStore is where credentials the library GENERATES live — machine keys,
// the broker's access token, client secrets — and is also its memory: a key is
// kept only while the store still holds it. Outputs is the write-only
// publication of what consumers need (issuer, audience, client ids, the JWKS
// snapshot, the broker token). Dev wires directories and files; the control
// plane wires OpenBao and its delivery path.
package identity
