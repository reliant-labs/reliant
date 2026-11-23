package hostedimage

// The push base: WHERE a hosted artifact goes.
//
// `<registry_host>/<org>/<project>` is the ONE subtree the platform admits for
// an org's project, and every hosted address forge computes hangs off it —
// images at `<base>/<image>`, a site release at `<base>/<site>/static.v1`, a
// config bundle at `<base>/bundle.v1/<env>`. That grammar is the registry's own
// (ADR-0003); forge composes the same strings so the address it pushes to and
// the address the platform admits cannot be two derivations of one fact.
//
// THE ORG COMES FROM THE CREDENTIAL, NOT FROM A DECLARATION. forge asks the
// control plane which organization the token acts for (cloud.ResolveOrganization)
// and passes the answer here; nothing in KCL names it. The registry host is a
// forge DEFAULT (DefaultRegistryHost) overridable in KCL, and the project is
// forge.yaml's name. This package makes no call itself: the surfaces that have
// no credential (`forge lint`, a render on a fresh checkout) hold no org and so
// compose no base, which every consumer reads as "nothing to compare against"
// rather than guessing one.

import "strings"

// DefaultRegistryHost is Reliant's hosted container registry — what
// `forge.ControlPlane {}` means when it names no registry_host.
//
// One fact, twice: kcl/schema.k's RELIANT_REGISTRY_HOST is the KCL copy,
// which is what a render actually reads. This is the Go copy, for the
// surfaces that have no KCL in front of them. A test renders the schema and
// fails if the two disagree (TestRegistryDefault_KCLMatchesGo).
//
// It is a HOST, with no scheme and no trailing slash, because it is joined
// into an image reference.
const DefaultRegistryHost = "registry.reliantapi.com"

// PushBase composes `<registryHost>/<organization>/<project>`.
//
// It returns "" when any segment is missing, and that is deliberately not an
// error here: this function has no env to name and no opinion about whether
// the caller can proceed. The callers decide, and they decide differently — a
// build refuses a bare image it cannot resolve, a render reports what it could
// not compare.
//
// registryHost defaults to DefaultRegistryHost when empty, so a control plane
// that names no registry resolves to Reliant's — the overwhelmingly common case.
func PushBase(registryHost, organization, project string) string {
	host := strings.TrimSpace(registryHost)
	if host == "" {
		host = DefaultRegistryHost
	}
	org := strings.TrimSpace(organization)
	proj := strings.TrimSpace(project)
	if org == "" || proj == "" {
		return ""
	}
	return NormalizeBase(host) + "/" + org + "/" + proj
}
