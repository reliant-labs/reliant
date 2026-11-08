package cli

// The objects forge converges IN THE TARGET CLUSTER so one env's binding can
// apply.
//
// ── ONE IDENTITY PER BINDING, AND NOTHING ELSE ──────────────────────────────
//
// A binding is one env × one connected cluster. The control plane's Flux
// authenticates to this cluster as that binding's OWN identity — never a
// shared one:
//
//   - a ServiceAccount forge mints in this cluster, one per binding, whose
//     token is that binding's only credential. There is no cloud identity.
//
// The apply runs AS that identity. There is no impersonation step and no
// bootstrap write: the identity's rules are derived from what the hub applies
// from the env's render (cluster_connect_scope.go), and a kind the render did not contain is
// Forbidden by the cluster's own access check. The ONLY objects connect writes
// are the grant itself (a Role or ClusterRole, its binding, and the identity's
// ServiceAccount and token Secret) — and the delivery grant, if asked.
//
// Every object carries the labels below, which is how disconnect and a re-run
// find exactly what this binding owns without being told.

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	connectBindingLabelKey = "forge.dev/binding-env"
	connectGrantApply      = "apply"
	connectClusterLabelKey = "forge.dev/connected-cluster"
)

// connectBindingName is the name of every object a binding owns, namespaced
// by cluster AND env: two forge projects connecting one physical cluster under
// two names, or one project binding two envs to it, must not fight over one
// object's subject.
func connectBindingName(cluster, env string) string {
	return "forge-connect-" + cluster + "-" + dnsSegment(env)
}

// dnsSegment lower-cases a name into DNS-1123 label characters.
func dnsSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// connectRBAC is everything the grant render needs. Pure inputs, so the
// manifests are testable with no cluster and `--dry-run` prints exactly what a
// real run applies.
type connectRBAC struct {
	// ClusterName is the connected cluster's forge name (the RPC's `name`).
	ClusterName string
	// Env is the env whose binding this is.
	Env string
	// KubeContext is the operator's context the grant is applied THROUGH.
	KubeContext string
	// TokenNamespace names the namespace the binding's SA lives in.
	TokenNamespace string
	// Scope and Plan: where the identity may write and with what rules.
	Scope applyScope
	Plan  grantPlan
	// DeliverySecrets are the Secrets the control plane may write and renew
	// (see cluster_connect_delivery.go). Empty grants nothing.
	DeliverySecrets []deliverySecret
	// DeliverySubject is an optional dedicated delivery identity, bound
	// alongside the connect identity.
	DeliverySubject *deliverySubject
	// IssuerJWKS adds `get` on the cluster's JWKS path, for the control plane to
	// re-read the signing keys it registered (a non-resource URL, so it can only
	// live in a ClusterRole).
	IssuerJWKS bool
}

func (r connectRBAC) bindingName() string { return connectBindingName(r.ClusterName, r.Env) }

// tokenServiceAccount is the binding's own ServiceAccount.
func (r connectRBAC) tokenServiceAccount() string { return r.bindingName() }

// tokenSecretName is the SA token Secret's name, derived so connect and
// disconnect cannot disagree about which object to delete.
func (r connectRBAC) tokenSecretName() string { return r.tokenServiceAccount() + "-token" }

// labels are stamped on every object of this binding.
func (r connectRBAC) labels(grant string) map[string]any {
	return map[string]any{
		forgeManagedLabelKey:   forgeManagedLabelValue,
		connectClusterLabelKey: r.ClusterName,
		connectBindingLabelKey: dnsSegment(r.Env),
		deliveryGrantLabelKey:  grant,
	}
}

// rbacObject is one rendered RBAC object.
type rbacObject struct {
	Kind      string
	Namespace string
	Name      string
	Body      map[string]any
}

func (o rbacObject) ref() string {
	if o.Namespace != "" {
		return o.Kind + " " + o.Namespace + "/" + o.Name
	}
	return o.Kind + " " + o.Name
}

func (o rbacObject) yaml() string {
	b, err := yaml.Marshal(o.Body)
	if err != nil {
		panic("marshal " + o.ref() + ": " + err.Error())
	}
	return string(b)
}

// jwksRule is the one non-resource rule a binding may hold.
func jwksRule() map[string]any {
	return map[string]any{"nonResourceURLs": []any{oidcJWKSPath}, "verbs": []any{"get"}}
}

func rulesBody(rules []ruleSpec) []any {
	out := make([]any, 0, len(rules)+1)
	for _, r := range rules {
		rule := map[string]any{
			"apiGroups": []any{r.Ref.Group},
			"resources": []any{r.Ref.Resource},
			"verbs":     strsToAny(r.Verbs),
		}
		if len(r.Names) > 0 {
			rule["resourceNames"] = strsToAny(r.Names)
		}
		out = append(out, rule)
	}
	return out
}

func strsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func (r connectRBAC) subjectBody() []any {
	s := r.connectSubject()
	if s.Kind == subjectKindServiceAccount {
		return []any{map[string]any{"kind": s.Kind, "name": s.Name, "namespace": s.Namespace}}
	}
	return []any{map[string]any{"kind": s.Kind, "name": s.Name, "apiGroup": "rbac.authorization.k8s.io"}}
}

func (r connectRBAC) object(kind, namespace, name string, extra map[string]any) rbacObject {
	meta := map[string]any{"name": name, "labels": r.labels(connectGrantApply)}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	body := map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind, "metadata": meta}
	for k, v := range extra {
		body[k] = v
	}
	return rbacObject{Kind: kind, Namespace: namespace, Name: name, Body: body}
}

func (r connectRBAC) binding(kind, namespace, roleKind string) rbacObject {
	name := r.bindingName()
	return r.object(kind, namespace, name, map[string]any{
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": roleKind, "name": name},
		"subjects": r.subjectBody(),
	})
}

// grantObjects are the apply grant's RBAC objects, sorted by kind and name.
//
//	cluster scope:   ClusterRole + ClusterRoleBinding, rules derived from what
//	                 the hub applies, with escalate/bind pinned by name.
//	namespace scope: a Role + RoleBinding per namespace, and — only when the
//	                 render declares a Namespace object or the JWKS read is
//	                 asked for — a ClusterRole whose rules are pinned by
//	                 resourceNames or non-resource URLs.
func (r connectRBAC) grantObjects() []rbacObject {
	name := r.bindingName()
	var out []rbacObject
	// clusterRules wraps a ClusterRole's rules, adding the JWKS read when asked.
	clusterRules := func(rules []ruleSpec) map[string]any {
		body := rulesBody(rules)
		if r.IssuerJWKS {
			body = append(body, jwksRule())
		}
		return map[string]any{"rules": body}
	}
	if r.Plan.Cluster {
		out = append(out,
			r.object("ClusterRole", "", name, clusterRules(r.Plan.ClusterRules)),
			r.binding("ClusterRoleBinding", "", "ClusterRole"))
		return sortObjects(out)
	}
	for _, ns := range r.Plan.Namespaces {
		rules := r.Plan.NamespaceRules[ns]
		if len(rules) == 0 {
			continue
		}
		out = append(out,
			r.object("Role", ns, name, map[string]any{"rules": rulesBody(rules)}),
			r.binding("RoleBinding", ns, "Role"))
	}
	if len(r.Plan.PinnedNamespaces) > 0 || r.IssuerJWKS {
		// Namespace is cluster-scoped. get/update/patch pinned by name lets
		// Flux server-side-apply the Namespace object the render carries;
		// create cannot be pinned, so the namespace must already exist (connect
		// checks), and delete is withheld so a prune can never remove one.
		// The same ClusterRole carries the JWKS read: a non-resource URL has no
		// namespace to be granted in.
		var rules []ruleSpec
		if len(r.Plan.PinnedNamespaces) > 0 {
			rules = []ruleSpec{{Ref: refNamespaces, Names: r.Plan.PinnedNamespaces, Verbs: []string{"get", "patch", "update"}}}
		}
		out = append(out,
			r.object("ClusterRole", "", name, clusterRules(rules)),
			r.binding("ClusterRoleBinding", "", "ClusterRole"))
	}
	return sortObjects(out)
}

func sortObjects(objs []rbacObject) []rbacObject {
	sort.SliceStable(objs, func(i, j int) bool {
		a, b := objs[i], objs[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return objs
}

// identityManifests are what the binding needs BEFORE it has a token: the
// namespace, its own ServiceAccount and its token Secret.
//
// A `kubernetes.io/service-account-token` SECRET, NOT A TokenRequest: nothing
// rotates a bounded token on a schedule, so a cluster nobody re-connects for 90
// days would stop deploying, and the failure arrives as an authentication error
// during an unrelated release. The Secret token has no such deadline, is
// revocable by deleting it (disconnect does), and its blast radius is the
// grant above — the control that matters.
func (r connectRBAC) identityManifests() []string {
	labels := r.labels("identity")
	obj := func(body map[string]any) string {
		b, _ := yaml.Marshal(body)
		return strings.TrimRight(string(b), "\n")
	}
	return []string{
		obj(map[string]any{"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": r.TokenNamespace, "labels": labels}}),
		obj(map[string]any{"apiVersion": "v1", "kind": "ServiceAccount",
			"metadata": map[string]any{"name": r.tokenServiceAccount(), "namespace": r.TokenNamespace, "labels": labels}}),
		obj(map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/service-account-token",
			"metadata": map[string]any{
				"name": r.tokenSecretName(), "namespace": r.TokenNamespace, "labels": labels,
				"annotations": map[string]any{"kubernetes.io/service-account.name": r.tokenServiceAccount()},
			}}),
	}
}

// bootstrapManifests renders the objects forge applies THROUGH THE OWNER'S OWN
// CONTEXT — the one write in this command that needs the owner's credentials.
func (r connectRBAC) bootstrapManifests() string {
	return strings.Join(r.bootstrapStages(), "\n---\n") + "\n"
}

// bootstrapStages is bootstrapManifests split into the applies it is made of,
// in order. kubectl carries on past a failed object in one stream, so the
// delivery admission policy goes in its own stage ahead of the Roles that rely
// on it: a failure there stops the grant instead of landing it ungated.
func (r connectRBAC) bootstrapStages() []string {
	stages := []string{r.baseManifests()}
	if r.deliveryEnabled() {
		stages = append(stages,
			strings.Join(r.deliveryGuardManifests(), "\n---\n")+"\n",
			strings.Join(r.deliveryGrantManifests(), "\n---\n")+"\n")
	}
	return stages
}

func (r connectRBAC) baseManifests() string {
	docs := r.identityManifests()
	for _, o := range r.grantObjects() {
		docs = append(docs, strings.TrimRight(o.yaml(), "\n"))
	}
	return strings.Join(docs, "\n---\n") + "\n"
}

// writeGrantSummary says what was applied in the cluster and that nothing is
// owed afterwards.
func writeGrantSummary(out io.Writer, r connectRBAC) {
	fmt.Fprintf(out, "\nNo cloud IAM grant is needed: this binding authenticates with its own scoped\n"+
		"ServiceAccount token (%s/%s), and forge applied its RBAC through\n"+
		"context %q. The token was sent write-only and is never printed or stored locally.\n"+
		"Revoke it with `forge cluster disconnect`; rotate it by re-running connect.\n%s\n",
		r.TokenNamespace, r.tokenServiceAccount(), r.KubeContext, r.grantSummary())
}

// grantSummary says, in one paragraph, what the in-cluster grant is.
func (r connectRBAC) grantSummary() string {
	subject := r.connectSubject()
	who := subject.Kind + " " + subject.username()
	switch {
	case r.Plan.Empty:
		return fmt.Sprintf("  %s: %s, no apply rules (the hub applies nothing on this cluster).", r.bindingName(), who)
	case r.Plan.Cluster:
		return fmt.Sprintf("  ClusterRole and ClusterRoleBinding %s: %s, scope cluster.\n%s",
			r.bindingName(), who, clusterAdminWarning)
	}
	return fmt.Sprintf("  %s: %s, scope namespace:%s.", r.bindingName(), who, strings.Join(r.Plan.Namespaces, ","))
}
