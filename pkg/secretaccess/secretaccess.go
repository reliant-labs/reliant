// Package secretaccess derives OpenBao JWT auth mounts, roles and ACL policies
// from the set of secret names each Kubernetes namespace reads from the store.
//
// It is pure: no I/O, no clock, no randomness. The same [Request] always
// yields the same [Document], byte for byte, so the two programs that apply it
// (forge's OpenBao converge Job for a self-run store, and a hosted control
// plane for its own store) cannot disagree about who may read what. Applying
// the Document to a live OpenBao is the caller's job; [Mount], [Role] and
// [Policy] each carry the API path and payload to write.
//
// # Model
//
// One JWT auth mount per cluster (auth/jwt-<cluster id>), trusting that
// cluster's own service-account issuer. One role per (env, cluster, namespace),
// bound to the namespace's reliant-secrets service account and to the
// cluster's audience, issuing 10-minute batch tokens carrying exactly one
// policy. That policy lists, by exact path, the names the namespace's
// ExternalSecrets reference:
//
//	path "secret/data/tenants/<org>/<env>/STRIPE_SECRET_KEY" {
//	  capabilities = ["read"]
//	}
//
// No globs, no list, no metadata: a compromised namespace can read the values
// it already receives and nothing else. The one escape is a [WildcardRead],
// which needs a stated reason and is confined to a single env's directory.
//
// # Changing the name set safely
//
// Apply [Widen] (the union of the old and new shapes) before the workloads
// that need the new names roll out, then [Derive] the new shape alone once they
// converge, and delete whatever [Removed] reports. Narrowing never races a
// rollout that still needs the old name.
//
// # Consuming it
//
// Callers declare their own narrow interface for the thing that writes to
// OpenBao (typically one method, put-policy, and one, put-role). Nothing here
// performs a network call, so there is no client to mock.
//
//forge:exclude-contract: pkg/ library: pure derivation over caller-supplied data, stdlib only, one implementation
package secretaccess

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultKVMount is the KV-v2 mount the component creates.
	DefaultKVMount = "secret"
	// DefaultRoot is the first path segment under the mount for values that
	// are delivered to a cluster. Host-process local envs use "local".
	DefaultRoot = "tenants"
	// DefaultServiceAccount is the service account in each namespace whose
	// projected token logs in. Role subjects are pinned to it.
	DefaultServiceAccount = "reliant-secrets"
	// TokenTTL is the lifetime of every issued token. It is also the maximum:
	// batch tokens are not renewable, ESO logs in again.
	TokenTTL = 10 * time.Minute
	// TokenType is the OpenBao token type roles issue.
	TokenType = "batch"
)

// Scope addresses one env's directory in the KV mount:
// <Mount>/data/<Root>/<Org>/<Env>/<NAME>. For a self-run store Org is the
// project and Env the env name.
type Scope struct {
	// Mount is the KV-v2 mount. Default [DefaultKVMount].
	Mount string
	// Root is "tenants" (delivered to a cluster, the default) or "local".
	Root string
	Org  string
	Env  string
}

var (
	segmentForm   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	secretNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	patternForm   = regexp.MustCompile(`^[A-Za-z0-9_]*\*$`)
	dnsLabelForm  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	clusterIDForm = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	uuidForm      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	shortForm     = regexp.MustCompile(`^[a-z0-9]{1,8}$`)
)

// ValidName reports whether name is a legal secret name: an env-var style
// identifier. It is the rule the stores enforce on write, restated here so a
// bad shape fails before anything is applied.
func ValidName(name string) bool { return secretNameRe.MatchString(name) }

func (s Scope) kv() string {
	if s.Mount == "" {
		return DefaultKVMount
	}
	return s.Mount
}

func (s Scope) root() string {
	if s.Root == "" {
		return DefaultRoot
	}
	return s.Root
}

// Validate reports why the scope cannot address a directory. Every segment must
// be a plain path segment: a slash, glob character or ".." would let one env's
// policy name another's values.
func (s Scope) Validate() error {
	var errs []error
	for _, seg := range []struct{ what, v string }{
		{"mount", s.kv()}, {"root", s.root()}, {"org", s.Org}, {"env", s.Env},
	} {
		if !segmentForm.MatchString(seg.v) {
			errs = append(errs, fmt.Errorf("scope %s %q is not a plain path segment", seg.what, seg.v))
		}
	}
	return errors.Join(errs...)
}

// SecretPath is the KV-v2 API path of one secret's current version, the path a
// policy grants and a reader requests. name is not validated.
func (s Scope) SecretPath(name string) string {
	return s.kv() + "/data/" + s.root() + "/" + s.Org + "/" + s.Env + "/" + name
}

// Cluster is one registered cluster: the identity of its auth mount and the
// issuer that vouches for its tokens.
type Cluster struct {
	// ID names the cluster and its mount (auth/jwt-<ID>).
	ID string
	// Audience is the token audience the cluster's SecretStores request and
	// the roles require. It pins a registration: a token minted for another
	// audience is refused.
	Audience string
	// Trust says how OpenBao verifies the cluster's tokens. Leave it zero when
	// the applier fills the mount's config itself (for instance a converge
	// Job that fetches the cluster's JWKS); a non-zero Trust is emitted as
	// the mount's config.
	Trust Trust
}

// Trust is the mount's verification config. At most one of OIDCDiscoveryURL
// and PublicKeys may be set.
type Trust struct {
	// OIDCDiscoveryURL is the issuer's discovery base (GKE publishes it).
	OIDCDiscoveryURL string
	// BoundIssuer is the required "iss" claim. Defaults to OIDCDiscoveryURL.
	BoundIssuer string
	// PublicKeys are PEM keys, for clusters whose issuer is not reachable from
	// OpenBao.
	PublicKeys []string
}

// Binding is the set of names one namespace of one cluster reads.
type Binding struct {
	Cluster   string
	Namespace string
	// Names are the secret names the namespace's ExternalSecrets reference,
	// each granted by exact path.
	Names []string
	// Wildcards are the escape from exact paths; each needs a reason.
	Wildcards []WildcardRead
}

// WildcardRead grants read on every name matching Pattern within the env's own
// directory. It exists for the rare consumer whose names are not known ahead of
// time; every use is a standing exception, so Reason is mandatory and is written
// into the policy as a comment.
type WildcardRead struct {
	// Pattern is a name prefix ending in a single "*" ("STRIPE_*", or "*" for
	// the whole env). It cannot leave the env's directory.
	Pattern string
	// Reason is why exact names are not enough.
	Reason string
}

// Request is everything Derive needs: one env, the clusters that deliver it,
// and what each namespace reads.
type Request struct {
	Scope Scope
	// ServiceAccount is the account roles bind to. Default
	// [DefaultServiceAccount].
	ServiceAccount string
	Clusters       []Cluster
	Bindings       []Binding
}

func (r Request) serviceAccount() string {
	if r.ServiceAccount == "" {
		return DefaultServiceAccount
	}
	return r.ServiceAccount
}

// Document is the full set of objects a Request needs in OpenBao, sorted so the
// same Request always yields an identical Document.
type Document struct {
	Mounts   []Mount
	Policies []Policy
	Roles    []Role
}

// Derive maps a Request to the mounts, roles and policies it needs. It reports
// every problem it finds rather than the first.
func Derive(req Request) (Document, error) {
	var errs []error
	if err := req.Scope.Validate(); err != nil {
		errs = append(errs, err)
	}
	if !dnsLabelForm.MatchString(req.serviceAccount()) {
		errs = append(errs, fmt.Errorf("service account %q is not a DNS label", req.serviceAccount()))
	}

	clusters := map[string]Cluster{}
	var doc Document
	for _, c := range req.Clusters {
		switch {
		case !clusterIDForm.MatchString(c.ID):
			errs = append(errs, fmt.Errorf("cluster id %q is not a plain identifier", c.ID))
			continue
		case clusters[c.ID].ID != "":
			errs = append(errs, fmt.Errorf("cluster %q declared twice", c.ID))
			continue
		case c.Audience == "" || strings.ContainsAny(c.Audience, " \t\r\n"):
			errs = append(errs, fmt.Errorf("cluster %q needs an audience without whitespace", c.ID))
		}
		if c.Trust.OIDCDiscoveryURL != "" && len(c.Trust.PublicKeys) > 0 {
			errs = append(errs, fmt.Errorf("cluster %q sets both an OIDC discovery URL and public keys; choose one", c.ID))
		}
		clusters[c.ID] = c
		doc.Mounts = append(doc.Mounts, Mount{Cluster: c.ID, Trust: c.Trust})
	}

	seen := map[string]bool{}
	for _, b := range req.Bindings {
		where := fmt.Sprintf("binding %s/%s", b.Cluster, b.Namespace)
		c, ok := clusters[b.Cluster]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: cluster is not declared", where))
			continue
		}
		if !dnsLabelForm.MatchString(b.Namespace) || len(b.Namespace) > 63 {
			errs = append(errs, fmt.Errorf("%s: namespace is not a DNS label", where))
			continue
		}
		key := b.Cluster + "\x00" + b.Namespace
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s: declared twice; merge the names (see Widen)", where))
			continue
		}
		seen[key] = true
		if len(b.Names) == 0 && len(b.Wildcards) == 0 {
			errs = append(errs, fmt.Errorf("%s: reads no names; drop the binding instead of granting nothing", where))
			continue
		}

		var rules []Rule
		bad := false
		for _, n := range dedupe(b.Names) {
			if !ValidName(n) {
				errs = append(errs, fmt.Errorf("%s: %q is not a valid secret name", where, n))
				bad = true
				continue
			}
			rules = append(rules, Rule{Path: req.Scope.SecretPath(n), Capabilities: []string{"read"}})
		}
		for _, w := range b.Wildcards {
			switch {
			case !patternForm.MatchString(w.Pattern):
				errs = append(errs, fmt.Errorf("%s: wildcard %q must be a name prefix ending in one \"*\"", where, w.Pattern))
				bad = true
			case strings.TrimSpace(w.Reason) == "":
				errs = append(errs, fmt.Errorf("%s: wildcard %q needs a reason", where, w.Pattern))
				bad = true
			default:
				rules = append(rules, Rule{
					Path: req.Scope.SecretPath(w.Pattern), Capabilities: []string{"read"}, WildcardReadReason: w.Reason,
				})
			}
		}
		if bad {
			continue
		}

		name := req.Scope.PolicyName(b.Cluster, b.Namespace)
		policy := Policy{Name: name, Rules: rules}
		policy.sort()
		doc.Policies = append(doc.Policies, policy)
		doc.Roles = append(doc.Roles, Role{
			Mount:          MountName(b.Cluster),
			Name:           name,
			BoundAudiences: []string{c.Audience},
			BoundSubject:   "system:serviceaccount:" + b.Namespace + ":" + req.serviceAccount(),
			Policies:       []string{name},
		})
	}
	if len(errs) > 0 {
		return Document{}, errors.Join(errs...)
	}

	slices.SortFunc(doc.Mounts, func(a, b Mount) int { return cmp.Compare(a.Cluster, b.Cluster) })
	slices.SortFunc(doc.Policies, func(a, b Policy) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(doc.Roles, func(a, b Role) int {
		return cmp.Or(cmp.Compare(a.Mount, b.Mount), cmp.Compare(a.Name, b.Name))
	})
	if err := checkCollisions(req.Scope, doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// checkCollisions refuses two bindings that derive one name. Names embed a hash
// of the full (org, env, cluster, namespace) tuple, so this needs a 32-bit
// collision on top of two others; it is checked anyway because the cost of
// being wrong is one namespace reading another's values.
func checkCollisions(s Scope, doc Document) error {
	owner := map[string]string{}
	for _, r := range doc.Roles {
		k := r.Mount + "/" + r.Name
		if prev, ok := owner[k]; ok && prev != r.BoundSubject {
			return fmt.Errorf("role %s derives for both %s and %s", k, prev, r.BoundSubject)
		}
		owner[k] = r.BoundSubject
	}
	return nil
}

// Widen returns the union of two requests for the same env: every cluster and
// every namespace of either, with the names of a namespace present in both
// merged. Deriving the union before a promotion and the new request alone after
// it keeps a rollout that still needs an old name from losing access.
func Widen(old, next Request) (Request, error) {
	if old.Scope != next.Scope || old.serviceAccount() != next.serviceAccount() {
		return Request{}, errors.New("secretaccess: Widen needs requests for the same scope and service account")
	}
	out := Request{Scope: next.Scope, ServiceAccount: next.ServiceAccount}

	byID := map[string]Cluster{}
	for _, c := range append(slices.Clone(old.Clusters), next.Clusters...) {
		if prev, ok := byID[c.ID]; ok {
			if !clusterEqual(prev, c) {
				return Request{}, fmt.Errorf("secretaccess: cluster %q differs between the requests; resolve its registration first", c.ID)
			}
			continue
		}
		byID[c.ID] = c
		out.Clusters = append(out.Clusters, c)
	}

	idx := map[string]int{}
	for _, b := range append(slices.Clone(old.Bindings), next.Bindings...) {
		k := b.Cluster + "\x00" + b.Namespace
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out.Bindings)
			out.Bindings = append(out.Bindings, Binding{
				Cluster: b.Cluster, Namespace: b.Namespace,
				Names: dedupe(b.Names), Wildcards: slices.Clone(b.Wildcards),
			})
			continue
		}
		m := &out.Bindings[i]
		m.Names = dedupe(append(m.Names, b.Names...))
		for _, w := range b.Wildcards {
			if !slices.Contains(m.Wildcards, w) {
				m.Wildcards = append(m.Wildcards, w)
			}
		}
	}
	return out, nil
}

func clusterEqual(a, b Cluster) bool {
	return a.ID == b.ID && a.Audience == b.Audience && a.Trust.OIDCDiscoveryURL == b.Trust.OIDCDiscoveryURL &&
		a.Trust.BoundIssuer == b.Trust.BoundIssuer && slices.Equal(a.Trust.PublicKeys, b.Trust.PublicKeys)
}

// Removed returns what next no longer contains: the mounts, roles and policies
// present in old and absent from next. After narrowing, deleting them is what
// takes access away from a namespace or cluster that left the shape.
func Removed(old, next Document) Document {
	var out Document
	for _, m := range old.Mounts {
		if !slices.ContainsFunc(next.Mounts, func(n Mount) bool { return n.Cluster == m.Cluster }) {
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

// PolicyName is the name of the policy, and of the role that carries it, for
// one namespace of one cluster:
//
//	env-<env>-<cluster>-<hash>
//
// env and cluster are short readable labels (see label); hash is eight hex
// digits of a digest of the whole (org, env, cluster, namespace) tuple, so two
// bindings that share both labels still get distinct names. Policies are global
// in OpenBao, which is why the env is part of the name.
func (s Scope) PolicyName(clusterID, namespace string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{s.Org, s.Env, clusterID, namespace}, "\x00")))
	return "env-" + label(s.Env) + "-" + label(clusterID) + "-" + hex.EncodeToString(sum[:])[:8]
}

// label shortens an identifier to at most eight characters that stay readable:
// a UUID becomes its first group, a short lowercase alphanumeric stays as is,
// anything else becomes eight hex digits of its digest.
func label(id string) string {
	switch {
	case uuidForm.MatchString(id):
		return id[:8]
	case shortForm.MatchString(id):
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:8]
}

func dedupe(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}
