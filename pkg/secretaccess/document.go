package secretaccess

import (
	"slices"
	"strconv"
	"strings"
)

// MountPrefix starts every JWT auth mount name; the cluster id follows.
const MountPrefix = "jwt-"

// MountName is the auth mount name of a cluster: "jwt-<cluster id>".
func MountName(clusterID string) string { return MountPrefix + clusterID }

// Mount is one cluster's JWT auth method.
type Mount struct {
	Cluster string
	Trust   Trust
}

// Name is the mount's name under auth/, as `bao auth enable -path=<Name>` takes it.
func (m Mount) Name() string { return MountName(m.Cluster) }

// EnablePath is the API path that enables the mount (PUT; payload
// [Mount.EnablePayload]). OpenBao answers 400 "path is already in use" when the
// mount exists, so an applier that reruns lists sys/auth first and enables only
// the mounts that are missing; the config, role and policy writes are
// idempotent overwrites.
func (m Mount) EnablePath() string { return "sys/auth/" + m.Name() }

// EnablePayload is the body of the enable call.
func (m Mount) EnablePayload() map[string]any {
	return map[string]any{"type": "jwt", "description": "Kubernetes service-account tokens of cluster " + m.Cluster}
}

// ConfigPath is the API path of the mount's verification config (PUT; payload
// [Mount.ConfigPayload]).
func (m Mount) ConfigPath() string { return "auth/" + m.Name() + "/config" }

// ConfigPayload is the verification config, or nil when the cluster declared
// no Trust and the applier supplies the mount's config itself.
func (m Mount) ConfigPayload() map[string]any {
	t := m.Trust
	switch {
	case t.OIDCDiscoveryURL != "":
		p := map[string]any{"oidc_discovery_url": t.OIDCDiscoveryURL}
		if t.BoundIssuer != "" {
			p["bound_issuer"] = t.BoundIssuer
		}
		return p
	case len(t.PublicKeys) > 0:
		p := map[string]any{"jwt_validation_pubkeys": slices.Clone(t.PublicKeys)}
		if t.BoundIssuer != "" {
			p["bound_issuer"] = t.BoundIssuer
		}
		return p
	}
	return nil
}

// Role is one namespace's login role on a cluster's mount. It carries the
// same name as its policy.
type Role struct {
	// Mount is the auth mount name ("jwt-<cluster>").
	Mount          string
	Name           string
	BoundAudiences []string
	// BoundSubject is the exact service-account subject:
	// system:serviceaccount:<namespace>:<service account>.
	BoundSubject string
	Policies     []string
}

// Path is the API path of the role (PUT; payload [Role.Payload]).
func (r Role) Path() string { return "auth/" + r.Mount + "/role/" + r.Name }

// Payload is the role's body: a JWT role bound to one audience and one subject
// that issues short-lived batch tokens (verified against OpenBao 2.6.2).
// The token also carries OpenBao's built-in default policy, which only lets it
// look itself up; ESO's Vault provider relies on that, so it is kept.
func (r Role) Payload() map[string]any {
	ttl := int(TokenTTL.Seconds())
	return map[string]any{
		"role_type":       "jwt",
		"user_claim":      "sub",
		"bound_audiences": slices.Clone(r.BoundAudiences),
		"bound_subject":   r.BoundSubject,
		"token_policies":  slices.Clone(r.Policies),
		"token_type":      TokenType,
		"token_ttl":       ttl,
		"token_max_ttl":   ttl,
	}
}

// Rule grants capabilities on one path.
type Rule struct {
	Path         string
	Capabilities []string
	// WildcardReadReason is set on a rule whose Path ends in "*". It is
	// written into the policy as a comment.
	WildcardReadReason string
}

// Policy is one ACL policy.
type Policy struct {
	Name  string
	Rules []Rule
}

func (p *Policy) sort() {
	slices.SortFunc(p.Rules, func(a, b Rule) int { return strings.Compare(a.Path, b.Path) })
}

// Path is the API path of the policy (PUT; payload [Policy.Payload]).
func (p Policy) Path() string { return "sys/policies/acl/" + p.Name }

// Payload is the policy's body.
func (p Policy) Payload() map[string]any { return map[string]any{"policy": p.HCL()} }

// HCL renders the policy. Rules are sorted by path, so the text is stable.
func (p Policy) HCL() string {
	var b strings.Builder
	for i, r := range p.Rules {
		if i > 0 {
			b.WriteByte('\n')
		}
		if r.WildcardReadReason != "" {
			b.WriteString("# wildcard read: " + oneLine(r.WildcardReadReason) + "\n")
		}
		caps := make([]string, len(r.Capabilities))
		for j, c := range r.Capabilities {
			caps[j] = strconv.Quote(c)
		}
		b.WriteString("path " + strconv.Quote(r.Path) + " {\n  capabilities = [" + strings.Join(caps, ", ") + "]\n}\n")
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
