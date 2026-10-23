package openbao

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/pkg/secretaccess"
)

// AccessDoc is everything the converge Job applies to a running OpenBao: the
// KV mount, the JWT auth mounts, the ACL policies and the login roles. It is
// derived from the [Spec] alone (the consumers, the writer, and the bindings
// pkg/secretaccess turns into exact-path policies), serialised into a
// ConfigMap, and executed by converge.sh. Its JSON is canonical: the same spec
// always yields the same bytes, which is what makes the Job content-addressed.
type AccessDoc struct {
	// Self is the id of the cluster OpenBao runs on; its mount (jwt-<Self>)
	// holds the converge, consumer and writer roles and is never removed.
	Self     string   `json:"self"`
	KV       KV       `json:"kv"`
	Mounts   []Mount  `json:"mounts"`
	Policies []Policy `json:"policies"`
	Roles    []Role   `json:"roles"`
}

// Mount is one cluster's JWT auth method.
type Mount struct {
	Name    string `json:"name"`
	Cluster string `json:"cluster"`
	// Trust says how OpenBao verifies the cluster's tokens: "self" (the Job
	// reads the cluster's own JWKS), or "config" (Config is written as is).
	Trust  string         `json:"trust"`
	Config map[string]any `json:"config,omitempty"`
}

// Policy is one ACL policy, kept as rules so a widening can union two of them.
type Policy struct {
	Name  string `json:"name"`
	Rules []Rule `json:"rules"`
}

// Rule grants capabilities on one path.
type Rule struct {
	Path         string   `json:"path"`
	Capabilities []string `json:"capabilities"`
	Reason       string   `json:"reason,omitempty"`
}

// Role is one login role; Payload is the body written to auth/<Mount>/role/<Name>.
type Role struct {
	Mount   string         `json:"mount"`
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
}

// The fixed principals and policies every store gets.
const (
	// PolicyAccessAdmin is what `delegated` grants: author JWT mounts, roles
	// and env-* policies, and nothing under the KV mount.
	PolicyAccessAdmin = "access-admin"
	// PolicyConverge is the converge Job's own: access-admin plus the KV
	// mount, and policies of any name (consumers').
	PolicyConverge = "converge-admin"
	// PolicyWriter is the write-only policy of the secret writer.
	PolicyWriter = "forge-secret-writer"
	// RoleConverge is the converge Job's login role on the self mount.
	RoleConverge = "openbao-converge"
	// ConvergeAudience is the audience of the Job's projected token.
	ConvergeAudience = "openbao-converge"
	// RoleWriter is the writer's role.
	RoleWriter = "forge-secret-writer"
	// ConsumerPrefix names a consumer's role and policy: consumer-<name>.
	ConsumerPrefix = "consumer-"
)

// BuildAccess derives the access document from a spec.
func BuildAccess(s Spec) (AccessDoc, error) {
	a := s.Access
	req := secretaccess.Request{
		Scope:          secretaccess.Scope{Mount: s.KV.Mount, Root: a.Root, Org: a.Org, Env: a.Env},
		ServiceAccount: a.ServiceAccount,
	}
	var errs []error

	var self *AccessCluster
	seen := map[string]bool{}
	for i := range a.Clusters {
		c := a.Clusters[i]
		if seen[c.ID] {
			errs = append(errs, fmt.Errorf("cluster %q declared twice", c.ID))
			continue
		}
		seen[c.ID] = true
		if c.ID == a.SelfCluster {
			self = &a.Clusters[i]
		}
		t := secretaccess.Trust{OIDCDiscoveryURL: c.OIDCDiscoveryURL, BoundIssuer: c.BoundIssuer, PublicKeys: c.PublicKeys}
		if c.ID != a.SelfCluster && t.OIDCDiscoveryURL == "" && len(t.PublicKeys) == 0 {
			errs = append(errs, fmt.Errorf("cluster %q is not the cluster OpenBao runs on and declares no oidcDiscoveryURL or publicKeys: its mount could not verify a token. Register its issuer (forge cluster connect), or drop its bindings", c.ID))
		}
		req.Clusters = append(req.Clusters, secretaccess.Cluster{ID: c.ID, Audience: c.Audience, Trust: t})
	}
	if self == nil {
		errs = append(errs, fmt.Errorf("access.selfCluster %q is not among access.clusters: its mount holds the converge, consumer and writer roles", a.SelfCluster))
	}
	for _, b := range a.Bindings {
		sb := secretaccess.Binding{Cluster: b.Cluster, Namespace: b.Namespace, Names: b.Names}
		for _, w := range b.Wildcards {
			sb.Wildcards = append(sb.Wildcards, secretaccess.WildcardRead{Pattern: w.Pattern, Reason: w.Reason})
		}
		req.Bindings = append(req.Bindings, sb)
	}
	if len(errs) > 0 {
		return AccessDoc{}, errors.Join(errs...)
	}
	derived, err := secretaccess.Derive(req)
	if err != nil {
		return AccessDoc{}, err
	}

	doc := AccessDoc{Self: a.SelfCluster, KV: s.KV}
	selfMount := secretaccess.MountName(a.SelfCluster)
	for _, m := range derived.Mounts {
		md := Mount{Name: m.Name(), Cluster: m.Cluster, Trust: "config", Config: m.ConfigPayload()}
		if m.Cluster == a.SelfCluster && md.Config == nil {
			md.Trust = "self"
		}
		doc.Mounts = append(doc.Mounts, md)
	}
	for _, p := range derived.Policies {
		pd := Policy{Name: p.Name}
		for _, r := range p.Rules {
			pd.Rules = append(pd.Rules, Rule{Path: r.Path, Capabilities: slices.Clone(r.Capabilities), Reason: r.WildcardReadReason})
		}
		doc.Policies = append(doc.Policies, pd)
	}
	for _, r := range derived.Roles {
		doc.Roles = append(doc.Roles, Role{Mount: r.Mount, Name: r.Name, Payload: r.Payload()})
	}

	scope := req.Scope
	kv := s.KV.Mount
	root := cmp.Or(scope.Root, secretaccess.DefaultRoot)
	envDir := fmt.Sprintf("%s/%s/%s", root, a.Org, a.Env)

	doc.Policies = append(doc.Policies,
		Policy{Name: PolicyAccessAdmin, Rules: accessAdminRules()},
		Policy{Name: PolicyConverge, Rules: convergeRules(kv)},
	)
	selfAud := self.Audience
	doc.Roles = append(doc.Roles, Role{Mount: selfMount, Name: RoleConverge, Payload: map[string]any{
		"role_type": "jwt", "user_claim": "sub",
		"bound_audiences": []string{ConvergeAudience},
		"bound_subject":   "system:serviceaccount:" + s.Namespace + ":" + ConvergeServiceAccount(s),
		"token_policies":  []string{PolicyConverge},
		"token_ttl":       900, "token_max_ttl": 900,
	}})

	for _, c := range s.Consumers {
		ns := c.Namespace
		if ns == "" {
			ns = s.Namespace
		}
		name := ConsumerPrefix + c.Name
		pols := []string{}
		if len(c.Grants) > 0 {
			p := Policy{Name: name}
			for _, g := range c.Grants {
				p.Rules = append(p.Rules, Rule{Path: g.Path, Capabilities: slices.Clone(g.Capabilities), Reason: strings.Join(strings.Fields(g.WildcardReadReason), " ")})
			}
			sortRules(p.Rules)
			doc.Policies = append(doc.Policies, p)
			pols = append(pols, name)
		}
		if c.Delegated {
			pols = append(pols, PolicyAccessAdmin)
		}
		doc.Roles = append(doc.Roles, Role{Mount: selfMount, Name: name, Payload: map[string]any{
			"role_type": "jwt", "user_claim": "sub",
			"bound_audiences": []string{selfAud},
			"bound_subject":   "system:serviceaccount:" + ns + ":" + c.Workload,
			"token_policies":  pols,
			"token_ttl":       3600, "token_max_ttl": 14400,
		}})
	}

	if s.Writer.Type == "service_account" {
		doc.Policies = append(doc.Policies, Policy{Name: PolicyWriter, Rules: []Rule{
			{Path: kv + "/data/" + envDir + "/*", Capabilities: []string{"create", "update"}},
			{Path: kv + "/metadata/" + envDir + "/*", Capabilities: []string{"list", "read"}},
			{Path: kv + "/metadata/" + envDir, Capabilities: []string{"list"}},
		}})
		doc.Roles = append(doc.Roles, Role{Mount: selfMount, Name: RoleWriter, Payload: map[string]any{
			"role_type": "jwt", "user_claim": "sub",
			"bound_audiences": []string{selfAud},
			"bound_subject":   "system:serviceaccount:" + s.Namespace + ":" + s.Writer.ServiceAccount,
			"token_policies":  []string{PolicyWriter},
			"token_type":      secretaccess.TokenType,
			"token_ttl":       int(secretaccess.TokenTTL.Seconds()), "token_max_ttl": int(secretaccess.TokenTTL.Seconds()),
		}})
	}

	for i := range doc.Policies {
		sortRules(doc.Policies[i].Rules)
	}
	slices.SortFunc(doc.Mounts, func(a, b Mount) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(doc.Policies, func(a, b Policy) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(doc.Roles, func(a, b Role) int { return cmp.Or(cmp.Compare(a.Mount, b.Mount), cmp.Compare(a.Name, b.Name)) })
	if err := checkNames(doc); err != nil {
		return AccessDoc{}, err
	}
	return doc, nil
}

// ConvergeServiceAccount is the converge Jobs' service account.
func ConvergeServiceAccount(s Spec) string { return s.Name + "-converge" }

func accessAdminRules() []Rule {
	return []Rule{
		{Path: "sys/auth", Capabilities: []string{"read"}},
		{Path: "sys/auth/jwt-*", Capabilities: []string{"create", "update", "delete", "sudo"}},
		{Path: "auth/jwt-*", Capabilities: []string{"create", "read", "update", "delete", "list"}},
		{Path: "sys/policies/acl", Capabilities: []string{"list"}},
		{Path: "sys/policies/acl/env-*", Capabilities: []string{"create", "read", "update", "delete"}},
	}
}

// convergeRules: access-admin, the KV mount itself, and policies of any name.
// No capability on the KV mount's data: the Job cannot read a value.
func convergeRules(kv string) []Rule {
	r := accessAdminRules()
	r = slices.DeleteFunc(r, func(x Rule) bool { return x.Path == "sys/policies/acl/env-*" })
	return append(r,
		Rule{Path: "sys/mounts", Capabilities: []string{"read"}},
		Rule{Path: "sys/mounts/" + kv, Capabilities: []string{"create", "update", "read"}},
		Rule{Path: kv + "/config", Capabilities: []string{"create", "update", "read"}},
		Rule{Path: "sys/policies/acl/*", Capabilities: []string{"create", "read", "update", "delete"}},
	)
}

func sortRules(r []Rule) {
	slices.SortFunc(r, func(a, b Rule) int { return cmp.Compare(a.Path, b.Path) })
}

// checkNames refuses two roles or policies that share a name: the second
// write would silently replace the first.
func checkNames(d AccessDoc) error {
	var errs []error
	pol := map[string]bool{}
	for _, p := range d.Policies {
		if pol[p.Name] {
			errs = append(errs, fmt.Errorf("policy %q is derived twice", p.Name))
		}
		pol[p.Name] = true
	}
	role := map[string]bool{}
	for _, r := range d.Roles {
		k := r.Mount + "/" + r.Name
		if role[k] {
			errs = append(errs, fmt.Errorf("role %q is derived twice", k))
		}
		role[k] = true
	}
	return errors.Join(errs...)
}

// JSON is the canonical serialisation the Job reads.
func (d AccessDoc) JSON() ([]byte, error) { return json.MarshalIndent(d, "", "  ") }

// Hash is a short digest of the document.
func (d AccessDoc) Hash() string {
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:10]
}

// Widen is the union of the document applied before a rollout and the new
// one: every mount, policy and role of either, with a policy present in both
// holding the union of its rules. Applying it before the workloads that need a
// new name roll out, and the new document alone after they converge, narrows
// without racing a rollout that still needs an old name. converge.sh does this
// with jq against the document it stored; this is the reference it is tested
// against.
func Widen(old, next AccessDoc) AccessDoc {
	out := AccessDoc{Self: next.Self, KV: next.KV}
	out.Mounts = unionBy(old.Mounts, next.Mounts, func(m Mount) string { return m.Name })
	out.Roles = unionBy(old.Roles, next.Roles, func(r Role) string { return r.Mount + "/" + r.Name })
	byName := map[string]Policy{}
	for _, p := range old.Policies {
		byName[p.Name] = p
	}
	for _, p := range next.Policies {
		if o, ok := byName[p.Name]; ok {
			p = Policy{Name: p.Name, Rules: unionRules(o.Rules, p.Rules)}
		}
		byName[p.Name] = p
	}
	for _, p := range byName {
		out.Policies = append(out.Policies, p)
	}
	slices.SortFunc(out.Policies, func(a, b Policy) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// unionBy keeps the later value on a key present in both.
func unionBy[T any](old, next []T, key func(T) string) []T {
	m := map[string]T{}
	for _, v := range old {
		m[key(v)] = v
	}
	for _, v := range next {
		m[key(v)] = v
	}
	out := make([]T, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	return out
}

func unionRules(a, b []Rule) []Rule {
	byPath := map[string]Rule{}
	for _, r := range append(slices.Clone(a), b...) {
		if prev, ok := byPath[r.Path]; ok {
			caps := append(slices.Clone(prev.Capabilities), r.Capabilities...)
			slices.Sort(caps)
			r.Capabilities = slices.Compact(caps)
			r.Reason = cmp.Or(r.Reason, prev.Reason)
		}
		byPath[r.Path] = r
	}
	out := make([]Rule, 0, len(byPath))
	for _, r := range byPath {
		out = append(out, r)
	}
	sortRules(out)
	return out
}

// Removed is what next no longer contains: the mounts, roles and policies
// present in old and absent from next. After narrowing, deleting them is what
// takes access away from a namespace or cluster that left the shape. The self
// mount is never removed.
func Removed(old, next AccessDoc) AccessDoc {
	var out AccessDoc
	for _, m := range old.Mounts {
		if m.Cluster != next.Self && !slices.ContainsFunc(next.Mounts, func(n Mount) bool { return n.Name == m.Name }) {
			out.Mounts = append(out.Mounts, m)
		}
	}
	for _, p := range old.Policies {
		if !slices.ContainsFunc(next.Policies, func(n Policy) bool { return n.Name == p.Name }) {
			out.Policies = append(out.Policies, p)
		}
	}
	for _, r := range old.Roles {
		if !slices.ContainsFunc(next.Roles, func(n Role) bool { return n.Mount == r.Mount && n.Name == r.Name }) {
			out.Roles = append(out.Roles, r)
		}
	}
	return out
}
