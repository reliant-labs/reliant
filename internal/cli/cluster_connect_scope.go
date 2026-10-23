package cli

// What the apply identity of one connected-cluster binding may do, derived
// from what the env's render actually applies there.
//
// THE OWNER CHOOSES THE SCOPE; THE RENDER CHOOSES THE KINDS. `--apply-scope`
// bounds WHERE the identity may write (named namespaces, or the whole
// cluster), and the env's own render bounds WHAT it may write there: one rule
// per (apiGroup, resource) the render contains, nothing else. A release that
// adds a kind is Forbidden by the cluster's own access check until the owner
// re-runs connect — a tripwire that needs no code of ours in the path.
//
// The default is namespace scope over the namespaces the render targets, and
// it refuses an env whose render contains cluster-scoped kinds rather than
// quietly widening: `cluster` is an explicit owner choice, and the output says
// what it is (cluster-admin equivalent).

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/reliant-labs/forge/internal/bundle"
)

// applyScope is the owner's `--apply-scope`.
type applyScope struct {
	// Cluster is `--apply-scope cluster`.
	Cluster bool
	// Namespaces are the named namespaces, sorted. Empty with Cluster false
	// means the flag was omitted: namespace scope over every namespace the
	// render targets.
	Namespaces []string
}

func (s applyScope) String() string {
	switch {
	case s.Cluster:
		return "cluster"
	case len(s.Namespaces) == 0:
		return "namespace (every namespace the env renders for this cluster)"
	}
	return "namespace:" + strings.Join(s.Namespaces, ",")
}

// label is the scope as the diff header prints it.
func (s applyScope) label() string {
	if s.Cluster {
		return "cluster"
	}
	if len(s.Namespaces) == 0 {
		return "namespace (default: every namespace the env renders)"
	}
	return "namespace:" + strings.Join(s.Namespaces, ",")
}

var scopeNamespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// parseApplyScope reads `--apply-scope`: "" (default), "cluster", or
// "namespace:<ns>[,<ns>…]".
func parseApplyScope(spec string) (applyScope, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return applyScope{}, nil
	case spec == "cluster":
		return applyScope{Cluster: true}, nil
	case strings.HasPrefix(spec, "namespace:"):
		seen := map[string]bool{}
		var out []string
		for _, ns := range strings.Split(strings.TrimPrefix(spec, "namespace:"), ",") {
			ns = strings.TrimSpace(ns)
			if !scopeNamespaceRE.MatchString(ns) {
				return applyScope{}, fmt.Errorf("--apply-scope %q: %q is not a namespace name (DNS-1123 label)\n"+
					"fix: --apply-scope namespace:<ns>[,<ns>…] | cluster", spec, ns)
			}
			if !seen[ns] {
				seen[ns] = true
				out = append(out, ns)
			}
		}
		sort.Strings(out)
		return applyScope{Namespaces: out}, nil
	}
	return applyScope{}, fmt.Errorf("--apply-scope %q is not namespace:<ns>[,<ns>…] or cluster\n"+
		"namespace scope is the default and grants a Role per namespace; cluster scope grants a ClusterRole "+
		"and is cluster-admin equivalent, so it is only ever an explicit choice", spec)
}

// resourceRef is one (apiGroup, resource) pair — what an RBAC rule names.
type resourceRef struct{ Group, Resource string }

func (r resourceRef) String() string {
	g := r.Group
	if g == "" {
		g = "core"
	}
	return g + "/" + r.Resource
}

// grantNeeds is what an env's render applies on ONE cluster.
type grantNeeds struct {
	// Namespaced: namespace -> the resources applied in it.
	Namespaced map[string]map[resourceRef]bool
	// Cluster are the cluster-scoped resources applied, Namespace objects
	// excluded (they are tracked by name below).
	Cluster map[resourceRef]string // resource -> an example kind/name, for messages
	// NamespaceObjects are the names of the Namespace objects the render
	// declares.
	NamespaceObjects map[string]bool
	// ClusterRoleRefs are the ClusterRoles a namespaced RoleBinding in the
	// render points at: binding one needs `bind` on that exact ClusterRole.
	ClusterRoleRefs map[string]bool
	Objects         int
}

var (
	refNamespaces   = resourceRef{"", "namespaces"}
	refPods         = resourceRef{"", "pods"}
	refReplicaSets  = resourceRef{"apps", "replicasets"}
	refRoles        = resourceRef{"rbac.authorization.k8s.io", "roles"}
	refRoleBindings = resourceRef{"rbac.authorization.k8s.io", "rolebindings"}
	refClusterRoles = resourceRef{"rbac.authorization.k8s.io", "clusterroles"}
)

// builtinClusterScopedKinds are the kinds a render commonly applies that live
// outside any namespace. A CustomResourceDefinition in the render overrides
// this for its own kind, and a `Cluster*` kind with no namespace is treated
// as cluster-scoped (ClusterIssuer, ClusterSecretStore, ClusterPolicy…).
var builtinClusterScopedKinds = map[string]bool{
	"Namespace": true, "Node": true, "PersistentVolume": true,
	"ClusterRole": true, "ClusterRoleBinding": true,
	"CustomResourceDefinition":         true,
	"StorageClass":                     true,
	"CSIDriver":                        true,
	"PriorityClass":                    true,
	"RuntimeClass":                     true,
	"IngressClass":                     true,
	"APIService":                       true,
	"ValidatingWebhookConfiguration":   true,
	"MutatingWebhookConfiguration":     true,
	"ValidatingAdmissionPolicy":        true,
	"ValidatingAdmissionPolicyBinding": true,
	"MutatingAdmissionPolicy":          true,
	"MutatingAdmissionPolicyBinding":   true,
	"GatewayClass":                     true,
	"PodSecurityPolicy":                true,
}

// irregularPlurals are kinds whose resource name is not the guessed plural.
var irregularPlurals = map[string]string{"Endpoints": "endpoints"}

func apiGroupOf(apiVersion string) (group, version string) {
	if g, v, ok := strings.Cut(apiVersion, "/"); ok {
		return g, v
	}
	return "", apiVersion
}

// deriveGrantNeeds reads an annotated render stream and returns what it
// applies on the cluster named by kubeContext. Secrets never ride a bundle
// (the delivery path syncs them) and hosted-tier records are the control
// plane's, so neither is a need.
func deriveGrantNeeds(stream, kubeContext string) (grantNeeds, error) {
	objs, err := bundle.ParseRenderedStream(stream)
	if err != nil {
		return grantNeeds{}, err
	}
	type crd struct {
		plural        string
		clusterScoped bool
	}
	crds := map[string]crd{}
	for _, o := range objs {
		if o.Kind != "CustomResourceDefinition" || o.Body == nil {
			continue
		}
		spec, _ := o.Body["spec"].(map[string]any)
		names, _ := spec["names"].(map[string]any)
		group, _ := spec["group"].(string)
		kind, _ := names["kind"].(string)
		plural, _ := names["plural"].(string)
		scope, _ := spec["scope"].(string)
		if group != "" && kind != "" && plural != "" {
			crds[group+"/"+kind] = crd{plural: plural, clusterScoped: scope == "Cluster"}
		}
	}

	needs := grantNeeds{
		Namespaced:       map[string]map[resourceRef]bool{},
		Cluster:          map[resourceRef]string{},
		NamespaceObjects: map[string]bool{},
		ClusterRoleRefs:  map[string]bool{},
	}
	for _, o := range objs {
		if o.Kind == "Secret" || !hasString(o.Clusters, kubeContext) {
			continue
		}
		group, version := apiGroupOf(o.APIVersion)
		if o.APIVersion == "" {
			return grantNeeds{}, fmt.Errorf("%s %q has no apiVersion, so the grant cannot name its resource", o.Kind, o.Name)
		}
		var plural string
		clusterScoped := builtinClusterScopedKinds[o.Kind] || (o.Namespace == "" && strings.HasPrefix(o.Kind, "Cluster"))
		if c, ok := crds[group+"/"+o.Kind]; ok {
			plural, clusterScoped = c.plural, c.clusterScoped
		} else if p, ok := irregularPlurals[o.Kind]; ok {
			plural = p
		} else {
			gvr, _ := meta.UnsafeGuessKindToResource(schema.GroupVersionKind{Group: group, Version: version, Kind: o.Kind})
			plural = gvr.Resource
		}
		ref := resourceRef{Group: group, Resource: plural}
		needs.Objects++
		switch {
		case o.Kind == "Namespace":
			needs.NamespaceObjects[o.Name] = true
		case clusterScoped:
			if _, ok := needs.Cluster[ref]; !ok {
				needs.Cluster[ref] = o.Kind + "/" + o.Name
			}
		default:
			ns := o.Namespace
			if ns == "" {
				ns = "default" // a namespaced object with no namespace is applied to `default`
			}
			if o.Kind == "RoleBinding" && group == "rbac.authorization.k8s.io" {
				ref, _ := o.Body["roleRef"].(map[string]any)
				if k, _ := ref["kind"].(string); k == "ClusterRole" {
					if n, _ := ref["name"].(string); n != "" {
						needs.ClusterRoleRefs[n] = true
					}
				}
			}
			if needs.Namespaced[ns] == nil {
				needs.Namespaced[ns] = map[resourceRef]bool{}
			}
			needs.Namespaced[ns][ref] = true
		}
	}
	return needs, nil
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ruleSpec is one RBAC rule, always a single (group, resource).
type ruleSpec struct {
	Ref   resourceRef
	Names []string
	Verbs []string
}

var (
	applyVerbs = []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	readVerbs  = []string{"get", "list", "watch"}
)

type ruleKey struct {
	ref   resourceRef
	names string
}

// ruleSet unions verbs per (resource, resourceNames).
type ruleSet map[ruleKey]map[string]bool

func (s ruleSet) add(ref resourceRef, names []string, verbs ...string) {
	k := ruleKey{ref: ref, names: strings.Join(names, ",")}
	if s[k] == nil {
		s[k] = map[string]bool{}
	}
	for _, v := range verbs {
		s[k][v] = true
	}
}

func (s ruleSet) rules() []ruleSpec {
	out := make([]ruleSpec, 0, len(s))
	for k, verbs := range s {
		r := ruleSpec{Ref: k.ref}
		if k.names != "" {
			r.Names = strings.Split(k.names, ",")
		}
		for v := range verbs {
			r.Verbs = append(r.Verbs, v)
		}
		sort.Strings(r.Verbs)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref != out[j].Ref {
			return out[i].Ref.String() < out[j].Ref.String()
		}
		return strings.Join(out[i].Names, ",") < strings.Join(out[j].Names, ",")
	})
	return out
}

var healthWorkloads = []resourceRef{
	{"apps", "deployments"}, {"apps", "statefulsets"}, {"apps", "daemonsets"}, {"apps", "replicasets"},
}

// addHealthReads adds the reads Flux's health check makes beyond the objects
// it applied: its status poller follows a workload to its ReplicaSets and
// Pods.
func addHealthReads(s ruleSet, have map[resourceRef]bool) {
	for _, w := range healthWorkloads {
		if have[w] {
			s.add(refReplicaSets, nil, readVerbs...)
			s.add(refPods, nil, readVerbs...)
			return
		}
	}
}

// grantPlan is the resolved grant: which rules where.
type grantPlan struct {
	Cluster bool
	// ClusterRules are the cluster scope's ClusterRole rules.
	ClusterRules []ruleSpec
	// Namespaces, sorted, and the Role rules in each (namespace scope).
	Namespaces     []string
	NamespaceRules map[string][]ruleSpec
	// PinnedNamespaces are Namespace objects the render declares inside the
	// scope: a cluster-scoped kind, so a ClusterRole pinned by resourceNames
	// (get, update, patch: RBAC cannot pin create, so the namespace must exist).
	PinnedNamespaces []string
	// BindClusterRoles are the ClusterRoles an in-scope RoleBinding references,
	// each pinned by name for `bind`.
	BindClusterRoles []string
	Objects          int
}

var rbacKinds = map[resourceRef]bool{refRoles: true, refRoleBindings: true, refClusterRoles: true, {"rbac.authorization.k8s.io", "clusterrolebindings"}: true}

func anyRBAC(refs map[resourceRef]bool) bool {
	for r := range refs {
		if rbacKinds[r] {
			return true
		}
	}
	return false
}

// planGrant resolves the owner's scope against what the render needs.
func planGrant(needs grantNeeds, scope applyScope) (grantPlan, error) {
	plan := grantPlan{Objects: needs.Objects}
	if needs.Objects == 0 {
		return grantPlan{}, fmt.Errorf("the env's render applies nothing to this cluster, so there is nothing to derive a grant from\n" +
			"fix: bind the cluster in the env's KCL first (ClusterTarget.cluster = <this context>, " +
			"connected_cluster = <name>), then re-run connect")
	}
	union := map[resourceRef]bool{}
	for _, refs := range needs.Namespaced {
		for r := range refs {
			union[r] = true
		}
	}
	for r := range needs.Cluster {
		union[r] = true
	}

	if scope.Cluster {
		rs := ruleSet{}
		for r := range union {
			rs.add(r, nil, applyVerbs...)
		}
		if len(needs.NamespaceObjects) > 0 {
			rs.add(refNamespaces, nil, applyVerbs...)
		}
		addHealthReads(rs, union)
		// bind and escalate are what lets the identity create the Roles and
		// bindings a release carries; with them it can grant itself anything,
		// which is why cluster scope is cluster-admin equivalent. Only a
		// render that carries RBAC kinds needs them.
		if anyRBAC(union) {
			rs.add(refRoles, nil, "bind", "escalate")
			rs.add(refClusterRoles, nil, "bind", "escalate")
		}
		plan.Cluster = true
		plan.ClusterRules = rs.rules()
		return plan, nil
	}

	namespaces := scope.Namespaces
	if len(namespaces) == 0 {
		seen := map[string]bool{}
		for ns := range needs.Namespaced {
			seen[ns] = true
		}
		for ns := range needs.NamespaceObjects {
			seen[ns] = true
		}
		for ns := range seen {
			namespaces = append(namespaces, ns)
		}
		sort.Strings(namespaces)
	}
	inScope := map[string]bool{}
	for _, ns := range namespaces {
		inScope[ns] = true
	}

	var problems []string
	if len(needs.Cluster) > 0 {
		var kinds []string
		for r, example := range needs.Cluster {
			kinds = append(kinds, fmt.Sprintf("%s (%s)", r, example))
		}
		sort.Strings(kinds)
		problems = append(problems, "cluster-scoped kinds no namespace grant can cover: "+strings.Join(kinds, ", "))
	}
	var outside []string
	for ns := range needs.Namespaced {
		if !inScope[ns] {
			outside = append(outside, ns)
		}
	}
	for ns := range needs.NamespaceObjects {
		if !inScope[ns] {
			outside = append(outside, "Namespace/"+ns)
		}
	}
	if len(outside) > 0 {
		sort.Strings(outside)
		problems = append(problems, "objects outside --apply-scope: "+strings.Join(outside, ", "))
	}
	for _, ns := range namespaces {
		if len(needs.Namespaced[ns]) == 0 && !needs.NamespaceObjects[ns] {
			problems = append(problems, fmt.Sprintf("namespace %q is in scope but the env renders nothing there (a typo?)", ns))
		}
	}
	if len(problems) > 0 {
		return grantPlan{}, fmt.Errorf("the env's render does not fit namespace scope:\n  - %s\n"+
			"fix: widen the list (--apply-scope namespace:<ns>,<ns>), or choose --apply-scope cluster "+
			"(cluster-admin equivalent) if the env really needs cluster-scoped kinds", strings.Join(problems, "\n  - "))
	}

	plan.Namespaces = namespaces
	plan.NamespaceRules = map[string][]ruleSpec{}
	for _, ns := range namespaces {
		have := needs.Namespaced[ns]
		rs := ruleSet{}
		for r := range have {
			rs.add(r, nil, applyVerbs...)
		}
		addHealthReads(rs, have)
		if have[refRoles] || have[refRoleBindings] {
			// A Role or RoleBinding in the release can only be created by an
			// identity that holds what it grants, or may escalate/bind. These
			// are namespaced: they reach Roles in this namespace only.
			rs.add(refRoles, nil, "bind", "escalate")
		}
		if len(rs) > 0 {
			plan.NamespaceRules[ns] = rs.rules()
		}
		if needs.NamespaceObjects[ns] {
			plan.PinnedNamespaces = append(plan.PinnedNamespaces, ns)
		}
	}
	for n := range needs.ClusterRoleRefs {
		plan.BindClusterRoles = append(plan.BindClusterRoles, n)
	}
	sort.Strings(plan.BindClusterRoles)
	sort.Strings(plan.PinnedNamespaces)
	return plan, nil
}

// describe is the one-line summary printed beside the scope.
func (p grantPlan) describe() string {
	if p.Cluster {
		return fmt.Sprintf("a ClusterRole of %d rule(s) over %d rendered object(s), plus bind and escalate on roles", len(p.ClusterRules), p.Objects)
	}
	rules := 0
	for _, r := range p.NamespaceRules {
		rules += len(r)
	}
	extra := ""
	if len(p.PinnedNamespaces) > 0 || len(p.BindClusterRoles) > 0 {
		extra = fmt.Sprintf(", plus a name-pinned ClusterRole (%d Namespace object(s), %d ClusterRole bind(s))", len(p.PinnedNamespaces), len(p.BindClusterRoles))
	}
	return fmt.Sprintf("%d namespace Role(s) of %d rule(s) over %d rendered object(s)%s", len(p.Namespaces), rules, p.Objects, extra)
}

// clusterAdminWarning is printed whenever the grant is cluster scope, because
// that is what it is.
const clusterAdminWarning = "WARNING: --apply-scope cluster is equivalent to cluster-admin. The identity can create ClusterRoles and\n" +
	"bindings (bind and escalate), so it can grant itself anything. It is derived from the env's render so an\n" +
	"unexpected kind is Forbidden, but that is a tripwire, not a boundary. Prefer --apply-scope namespace:<ns> where the env fits."
