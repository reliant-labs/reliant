package cli

// The objects forge converges IN THE TARGET CLUSTER so one env's binding can
// apply, and the one-time cloud IAM grant only the owner can run.
//
// ── ONE IDENTITY PER BINDING, AND NOTHING ELSE ──────────────────────────────
//
// A binding is one env × one connected cluster. The control plane's Flux
// authenticates to this cluster as that binding's OWN identity — never a
// shared one:
//
//   - gcp:   a Google service account the control plane allocates for the
//     binding (ConnectCluster returns it). GKE RBAC accepts it as a
//     `User` subject by email. A KSA mapped to a workforce principal is not
//     an option on GKE RBAC, and Google's federation limitations name
//     service-account impersonation for a Pod calling a different GKE
//     cluster, so one GSA per binding is the mechanism.
//   - token: a ServiceAccount forge mints in this cluster, one per binding,
//     whose token is that binding's only credential.
//
// The apply runs AS that identity. There is no impersonation step and no
// bootstrap write: the identity's rules are derived from what the env's render
// applies (cluster_connect_scope.go), and a kind the render did not contain is
// Forbidden by the cluster's own access check. The ONLY objects connect writes
// are the grant itself (a Role or ClusterRole, its binding, and for the token
// auth the identity's ServiceAccount) — and the delivery grant, if asked.
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
	// Auth selects whether the subject is BindingGSA or the minted SA.
	Auth string
	// BindingGSA is the binding's own Google service account, reported by the
	// control plane at registration (empty until then). Bound as a User.
	BindingGSA string
	// TokenNamespace names the namespace the token auth's SA lives in.
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

// tokenServiceAccount is the binding's own ServiceAccount on the token auth.
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
//	cluster scope:   ClusterRole + ClusterRoleBinding, rules derived from the
//	                 render, plus bind/escalate when the render carries RBAC.
//	namespace scope: a Role + RoleBinding per namespace, and — only when the
//	                 render declares a Namespace object or binds a ClusterRole —
//	                 a ClusterRole whose rules are pinned by resourceNames.
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
		rules := append([]ruleSpec(nil), r.Plan.NamespaceRules[ns]...)
		if len(r.Plan.BindClusterRoles) > 0 {
			// `bind` on a ClusterRole is checked in the RoleBinding's own
			// namespace, so the pin lives in this Role, not a ClusterRole.
			rules = append(rules, ruleSpec{Ref: refClusterRoles, Names: r.Plan.BindClusterRoles, Verbs: []string{"bind"}})
		}
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

// identityManifests are what the token auth needs BEFORE it has a token: the
// namespace, the binding's own ServiceAccount and its token Secret. Empty on
// gcp, where the identity is a Google service account and nothing is minted.
//
// A `kubernetes.io/service-account-token` SECRET, NOT A TokenRequest: nothing
// rotates a bounded token on a schedule, so a cluster nobody re-connects for 90
// days would stop deploying, and the failure arrives as an authentication error
// during an unrelated release. The Secret token has no such deadline, is
// revocable by deleting it (disconnect does), and its blast radius is the
// grant above — the control that matters.
func (r connectRBAC) identityManifests() []string {
	if r.Auth != authToken {
		return nil
	}
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

// writeGrantInstructions prints the one-time grant the OWNER runs, and it is
// the whole point of the response carrying the binding's identity.
//
// forge cannot perform this itself: the gcp half is an IAM write in the
// owner's project, against credentials forge does not have and should not ask
// for. So the command's real output is this text — it is the answer, not a
// footnote under a status line.
func writeGrantInstructions(out io.Writer, r connectRBAC, gke gkeContext) {
	if r.Auth != authGCP {
		// The token path needs no cloud grant: forge already applied the
		// RBAC and sent the token. Say so, rather than printing nothing and
		// leaving the reader wondering what they owe.
		fmt.Fprintf(out, "\nNo cloud IAM grant is needed: this binding authenticates with its own scoped\n"+
			"ServiceAccount token (%s/%s), and forge applied its RBAC through\n"+
			"context %q. The token was sent write-only and is never printed or stored locally.\n",
			r.TokenNamespace, r.tokenServiceAccount(), r.KubeContext)
		return
	}
	if strings.TrimSpace(r.BindingGSA) == "" {
		// EMPTY IS THE NORMAL STATE IN DEV, where the control plane has no
		// cloud identity. Saying so is correct; printing a gcloud command
		// with a blank principal in it is how someone runs a grant that
		// silently binds nothing.
		fmt.Fprintf(out, "\n⚠ This control plane returned no per-binding GCP identity, so there is no principal to grant\n"+
			"  and forge bound nothing. The cluster is registered, but the control plane cannot authenticate\n"+
			"  to it for env %q until it allocates a service account for the binding. A dev control plane\n"+
			"  normally has none — use --auth token against a dev cluster.\n", r.Env)
		return
	}
	fmt.Fprintf(out, `
Run this ONCE, as an owner of project %s. forge cannot: it is an IAM write in
your project, with credentials forge does not hold.

  gcloud projects add-iam-policy-binding %s \
    --member=serviceAccount:%s \
    --role=roles/container.clusterViewer

That is the whole cloud half. forge already applied the in-cluster half through
context %s, bound to this binding's own identity and to nothing shared:
%s
`, gke.Project, gke.Project, r.BindingGSA, r.KubeContext, r.grantSummary())
}

// grantSummary says, in one paragraph, what the in-cluster grant is.
func (r connectRBAC) grantSummary() string {
	subject := r.connectSubject()
	who := subject.Kind + " " + subject.username()
	if r.Plan.Cluster {
		return fmt.Sprintf("  ClusterRole and ClusterRoleBinding %s: %s, scope cluster (cluster-admin equivalent).\n%s",
			r.bindingName(), who, clusterAdminWarning)
	}
	return fmt.Sprintf("  %s: %s, scope namespace:%s.", r.bindingName(), who, strings.Join(r.Plan.Namespaces, ","))
}
