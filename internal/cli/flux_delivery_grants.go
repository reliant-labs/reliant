package cli

// THE GRANT RULE of a self-hosted delivery (Bundle.applier = "flux"), for a
// release whose bundle needs a kind the apply identities were not granted —
// a new CRD, a first PodDisruptionBudget, a Role in a namespace no earlier
// release used.
//
// GRANTS HAPPEN AT BOOTSTRAP. The bootstrap (`forge env deploy <env> --target
// flux-delivery`) derives the identities' grants from the env's render and
// RECORDS what it granted, one atomic permission per line, in the ConfigMap
// fluxdelivery.GrantsRecord in the delivery namespace. A deploy never needs
// RBAC write: it writes one pointer. That is what keeps a CI deployer a
// pointer-writer, and a leaked deployer token unable to grant itself anything
// (a principal that may set the rules of the role Flux applies as can grant
// that role anything, which is cluster-admin by another name).
//
// EVERY DEPLOY CHECKS FIRST. Before anything is recorded, pushed or pointed,
// the deploy derives the release's needs from the release's own bundle — the
// same projection the bootstrap used — and diffs them against the record.
// Kinds the release no longer needs are fine: the grant stays until the next
// bootstrap prunes it.
//
// A SHORTFALL IS CLOSED WITH THE CALLER'S OWN AUTHORITY, OR REFUSED. When the
// caller's credential can grant the shortfall itself (SelfSubjectAccessReviews:
// escalate and bind on the delivery roles, write on the RBAC kinds and the
// record), the deploy re-runs the bootstrap in the same command, after the
// plan is approved — the new grants are findings on the plan, so --yes or
// --approve <digest> approves them with everything else. That adds no
// authority anyone lacked. Otherwise the deploy refuses with exit 9 (a person
// with more authority must act; re-running changes nothing until they have)
// and names the one command a cluster admin runs.
//
// THE BACKSTOP is the reconciler's own error. A hand-written pointer, or a
// kind whose resource does not derive, fails `Forbidden` impersonating a
// forge-delivery-* identity; the wait and `forge env status` recognize that
// error and print the same message (flux_delivery_observe.go).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// The keys of the grants record.
const (
	fluxDeliveryGrantsKey        = "grants"
	fluxDeliveryGrantsDigestKey  = "digest"
	fluxDeliveryGrantsVersionKey = "forge-version"
)

// fluxDeliveryScopeCluster is a permission held cluster-wide (a ClusterRole);
// a namespaced one is "namespace:<ns>" (a Role in that namespace).
const fluxDeliveryScopeCluster = "cluster"

// fluxDeliveryPermission is ONE atomic permission an apply identity holds: a
// verb on a resource, in a scope, optionally pinned to one name. Atomic so
// that "is the release covered" is set containment, with no rule algebra.
type fluxDeliveryPermission struct {
	Identity string `json:"identity"`
	Scope    string `json:"scope"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Name     string `json:"name,omitempty"`
}

// line is the record's spelling: `<identity> <scope> <group>/<resource> <verb>
// [<name>]`, the core group spelled "core" so every field is one token.
func (p fluxDeliveryPermission) line() string {
	s := fmt.Sprintf("%s %s %s %s", p.Identity, p.Scope, resourceRef{p.Group, p.Resource}, p.Verb)
	if p.Name != "" {
		s += " " + p.Name
	}
	return s
}

func parseFluxDeliveryPermission(line string) (fluxDeliveryPermission, error) {
	f := strings.Fields(line)
	if len(f) != 4 && len(f) != 5 {
		return fluxDeliveryPermission{}, fmt.Errorf("grant line %q: want `<identity> <scope> <group>/<resource> <verb> [<name>]`", line)
	}
	group, resource, ok := strings.Cut(f[2], "/")
	if !ok || resource == "" {
		return fluxDeliveryPermission{}, fmt.Errorf("grant line %q: %q is not <group>/<resource>", line, f[2])
	}
	if group == "core" {
		group = ""
	}
	p := fluxDeliveryPermission{Identity: f[0], Scope: f[1], Group: group, Resource: resource, Verb: f[3]}
	if len(f) == 5 {
		p.Name = f[4]
	}
	return p, nil
}

// scopeLabel is how a person reads a scope: "(cluster)", "(namespace shop)".
func (p fluxDeliveryPermission) scopeLabel() string {
	if ns, ok := strings.CutPrefix(p.Scope, "namespace:"); ok {
		return "(namespace " + ns + ")"
	}
	return "(" + p.Scope + ")"
}

// fluxDeliveryPermissions expands both identities' grants into atomic
// permissions, sorted and unique: the record's content, and a release's needs.
func fluxDeliveryPermissions(grants ...fluxDeliveryGrant) []fluxDeliveryPermission {
	seen := map[string]bool{}
	var out []fluxDeliveryPermission
	add := func(sa, scope string, rules []ruleSpec) {
		for _, r := range rules {
			names := r.Names
			if len(names) == 0 {
				names = []string{""}
			}
			for _, v := range r.Verbs {
				for _, n := range names {
					p := fluxDeliveryPermission{Identity: sa, Scope: scope, Group: r.Ref.Group, Resource: r.Ref.Resource, Verb: v, Name: n}
					if !seen[p.line()] {
						seen[p.line()] = true
						out = append(out, p)
					}
				}
			}
		}
	}
	for _, g := range grants {
		add(g.ServiceAccount, fluxDeliveryScopeCluster, g.ClusterRules)
		for ns, rules := range g.NamespaceRules {
			add(g.ServiceAccount, "namespace:"+ns, rules)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].line() < out[j].line() })
	return out
}

// fluxDeliveryGrantsRecordObject is the record the bootstrap writes beside the
// grants it records: it rides the same chart, so the two cannot disagree.
func fluxDeliveryGrantsRecordObject(fd FluxDeliveryEntity, perms []fluxDeliveryPermission) rbacObject {
	lines := make([]string, 0, len(perms))
	for _, p := range perms {
		lines = append(lines, p.line())
	}
	body := strings.Join(lines, "\n")
	sum := sha256.Sum256([]byte(body))
	labels := map[string]any{}
	for k, v := range fluxDeliveryLabels(fd.Env) {
		labels[k] = v
	}
	return rbacObject{Kind: "ConfigMap", Namespace: fd.Namespace, Name: fluxdelivery.GrantsRecord, Body: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": fluxdelivery.GrantsRecord, "namespace": fd.Namespace, "labels": labels},
		"data": map[string]any{
			fluxDeliveryGrantsKey:        body,
			fluxDeliveryGrantsDigestKey:  "sha256:" + hex.EncodeToString(sum[:]),
			fluxDeliveryGrantsVersionKey: buildinfo.Version(),
		},
	}}
}

// fluxDeliveryGrantsRecord is the record as a deploy reads it back.
type fluxDeliveryGrantsRecord struct {
	// Found is false when the delivery namespace holds no record: a
	// bootstrap by a forge that wrote none, or none at all. Nothing is
	// known to be granted then.
	Found        bool
	Permissions  map[string]bool
	ForgeVersion string
}

func parseFluxDeliveryGrantsRecord(data map[string]string) (fluxDeliveryGrantsRecord, error) {
	rec := fluxDeliveryGrantsRecord{Found: true, Permissions: map[string]bool{}, ForgeVersion: data[fluxDeliveryGrantsVersionKey]}
	for _, line := range strings.Split(data[fluxDeliveryGrantsKey], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p, err := parseFluxDeliveryPermission(line)
		if err != nil {
			return fluxDeliveryGrantsRecord{}, err
		}
		rec.Permissions[p.line()] = true
	}
	return rec, nil
}

// covers reports whether the record grants p. A cluster-scoped grant of a
// namespaced resource (delivery_scope = "cluster") covers it in every
// namespace.
func (r fluxDeliveryGrantsRecord) covers(p fluxDeliveryPermission) bool {
	if r.Permissions[p.line()] {
		return true
	}
	if p.Scope != fluxDeliveryScopeCluster {
		wide := p
		wide.Scope = fluxDeliveryScopeCluster
		return r.Permissions[wide.line()]
	}
	return false
}

// fluxDeliveryShortfall is every needed permission the record does not grant.
func fluxDeliveryShortfall(needs []fluxDeliveryPermission, granted fluxDeliveryGrantsRecord) []fluxDeliveryPermission {
	var out []fluxDeliveryPermission
	for _, p := range needs {
		if !granted.covers(p) {
			out = append(out, p)
		}
	}
	return out
}

// fluxDeliveryGrantRow is one line of the shortfall as a person reads it: an
// identity, a resource, a scope, and the names it is pinned to when every
// missing verb is name-pinned.
type fluxDeliveryGrantRow struct {
	Identity string   `json:"identity"`
	Resource string   `json:"resource"`
	Scope    string   `json:"scope"`
	Verbs    []string `json:"verbs"`
	Names    []string `json:"names,omitempty"`
}

// fluxDeliveryGrantRows groups atomic permissions by (identity, resource,
// scope), sorted.
func fluxDeliveryGrantRows(perms []fluxDeliveryPermission) []fluxDeliveryGrantRow {
	type key struct{ id, res, scope string }
	verbs := map[key]map[string]bool{}
	names := map[key]map[string]bool{}
	var keys []key
	for _, p := range perms {
		k := key{p.Identity, resourceRef{p.Group, p.Resource}.String(), p.scopeLabel()}
		if verbs[k] == nil {
			verbs[k], names[k] = map[string]bool{}, map[string]bool{}
			keys = append(keys, k)
		}
		verbs[k][p.Verb] = true
		if p.Name != "" {
			names[k][p.Name] = true
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.id != b.id {
			// pre first: it is the phase that applies first.
			return a.id > b.id
		}
		if a.res != b.res {
			return a.res < b.res
		}
		return a.scope < b.scope
	})
	out := make([]fluxDeliveryGrantRow, 0, len(keys))
	for _, k := range keys {
		row := fluxDeliveryGrantRow{Identity: k.id, Resource: k.res, Scope: k.scope, Verbs: sortedSet(verbs[k]), Names: sortedSet(names[k])}
		out = append(out, row)
	}
	return out
}

// writeFluxDeliveryGrantRows prints rows as the aligned table both the
// refusal and the plan show.
func writeFluxDeliveryGrantRows(w io.Writer, indent string, rows []fluxDeliveryGrantRow) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		res := r.Resource
		if len(r.Names) > 0 {
			res += " [" + strings.Join(r.Verbs, ",") + " on " + strings.Join(r.Names, ",") + "]"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\n", indent, r.Identity, res, r.Scope)
	}
	_ = tw.Flush()
}

// fluxDeliveryGrantRefusal is the refusal: the release needs grants the
// delivery identities lack, and nothing in this command can close the gap.
// The deploy's preflight returns it before anything is written; the wait and
// `forge env status` return it (FromReconciler) when Flux reports the same gap
// as `Forbidden`.
type fluxDeliveryGrantRefusal struct {
	Env     string                 `json:"env"`
	Release string                 `json:"release,omitempty"`
	Missing []fluxDeliveryGrantRow `json:"missing"`
	// Why the deploy could not grant them itself, or "" when the reconciler
	// reported the gap.
	CannotGrant string `json:"cannot_grant,omitempty"`
	// Reporter names the Kustomization whose Forbidden was mapped, and
	// Detail is Flux's own message, when the reconciler reported it.
	Reporter string `json:"reporter,omitempty"`
	Detail   string `json:"detail,omitempty"`
	// Bootstrap is the command a cluster admin runs.
	Bootstrap string `json:"bootstrap"`
}

func newFluxDeliveryGrantRefusal(env, rel string, missing []fluxDeliveryPermission) *fluxDeliveryGrantRefusal {
	return &fluxDeliveryGrantRefusal{
		Env: env, Release: rel, Missing: fluxDeliveryGrantRows(missing),
		Bootstrap: fmt.Sprintf("forge env deploy %s --target %s", env, fluxdelivery.ChartName),
	}
}

// ExitCode is exitNeedsHuman: a person with more authority than this
// credential must act, and re-running changes nothing until they have.
//
// Reported by the reconciler instead, it is exitWrong: the release is recorded
// and pointed, and the env was looked at and is not running it.
func (e *fluxDeliveryGrantRefusal) ExitCode() int {
	if e.Reporter != "" {
		return exitWrong
	}
	return exitNeedsHuman
}

func (e *fluxDeliveryGrantRefusal) Error() string {
	var b strings.Builder
	subject := "release " + e.Release
	if e.Release == "" {
		subject = "this release"
	}
	if e.Reporter != "" {
		fmt.Fprintf(&b, "env %s's in-cluster Flux cannot apply %s: it needs permissions the delivery identities were not granted:\n", e.Env, subject)
	} else {
		fmt.Fprintf(&b, "%s for env %s needs permissions the delivery identities were not granted:\n", subject, e.Env)
	}
	writeFluxDeliveryGrantRows(&b, "  ", e.Missing)
	if e.Reporter != "" {
		fmt.Fprintf(&b, "Kustomization %s reports: %s\n", e.Reporter, strings.TrimSpace(e.Detail))
	}
	if e.CannotGrant != "" {
		fmt.Fprintf(&b, "this credential cannot grant them (%s).\n", e.CannotGrant)
	}
	fmt.Fprintf(&b, "Ask a cluster admin to run:   %s\n", e.Bootstrap)
	if e.Reporter != "" {
		b.WriteString("Flux retries on its own interval and converges once the grant lands; nothing needs re-deploying.")
	} else {
		b.WriteString("then re-run this deploy. Nothing was recorded, pushed or pointed.")
	}
	return b.String()
}

// fluxDeliveryGrantsOf reads the record from the delivery namespace. A missing
// record is Found=false, never an error: an env bootstrapped before forge
// recorded its grants is an env whose grants are unknown.
var fluxDeliveryReadGrants = func(ctx context.Context, fd FluxDeliveryEntity) (fluxDeliveryGrantsRecord, error) {
	out, err := cluster.KubectlCommand(ctx, "--context", fd.Cluster, "-n", fd.Namespace,
		"get", "configmap", fluxdelivery.GrantsRecord, "-o", "json", "--ignore-not-found").Output()
	if err != nil {
		return fluxDeliveryGrantsRecord{}, fmt.Errorf("read the delivery grants record %s/%s on %s: %w",
			fd.Namespace, fluxdelivery.GrantsRecord, fd.Cluster, kubectlErr(err))
	}
	if strings.TrimSpace(string(out)) == "" {
		return fluxDeliveryGrantsRecord{}, nil
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &cm); err != nil {
		return fluxDeliveryGrantsRecord{}, fmt.Errorf("decode the delivery grants record: %w", err)
	}
	return parseFluxDeliveryGrantsRecord(cm.Data)
}

// kubectlErr folds kubectl's stderr into its exit error.
func kubectlErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

// fluxDeliveryAccessCheck is one SelfSubjectAccessReview.
type fluxDeliveryAccessCheck struct {
	Verb, Group, Resource, Namespace, Name string
}

func (c fluxDeliveryAccessCheck) String() string {
	s := c.Verb + " " + resourceRef{c.Group, c.Resource}.String()
	if c.Name != "" {
		s += "/" + c.Name
	}
	if c.Namespace != "" {
		s += " in namespace " + c.Namespace
	}
	return s
}

// fluxDeliveryRegrantChecks is what the caller must be allowed to do to re-run
// the bootstrap's grants for missing: write and escalate/bind the roles that
// carry them, write their bindings, and write the record (and the bootstrap
// chart's inventory in kube-system).
func fluxDeliveryRegrantChecks(fd FluxDeliveryEntity, missing []fluxDeliveryPermission) []fluxDeliveryAccessCheck {
	const rbac = "rbac.authorization.k8s.io"
	seen := map[fluxDeliveryAccessCheck]bool{}
	var out []fluxDeliveryAccessCheck
	add := func(c fluxDeliveryAccessCheck) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, p := range missing {
		role := fd.Namespace + "-apply"
		if p.Identity == fluxdelivery.PreServiceAccount {
			role = fd.Namespace + "-pre"
		}
		roles, bindings, ns := "clusterroles", "clusterrolebindings", ""
		if n, ok := strings.CutPrefix(p.Scope, "namespace:"); ok {
			roles, bindings, ns = "roles", "rolebindings", n
		}
		add(fluxDeliveryAccessCheck{Verb: "create", Group: rbac, Resource: roles, Namespace: ns})
		for _, v := range []string{"patch", "escalate", "bind"} {
			add(fluxDeliveryAccessCheck{Verb: v, Group: rbac, Resource: roles, Namespace: ns, Name: role})
		}
		add(fluxDeliveryAccessCheck{Verb: "create", Group: rbac, Resource: bindings, Namespace: ns})
		add(fluxDeliveryAccessCheck{Verb: "patch", Group: rbac, Resource: bindings, Namespace: ns, Name: role})
	}
	add(fluxDeliveryAccessCheck{Verb: "patch", Resource: "configmaps", Namespace: fd.Namespace, Name: fluxdelivery.GrantsRecord})
	add(fluxDeliveryAccessCheck{Verb: "create", Resource: "configmaps", Namespace: "kube-system"})
	sort.SliceStable(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// fluxDeliveryAccessReview runs each check as a SelfSubjectAccessReview with
// the caller's own credential and returns the denied ones. A var so the
// preflight is testable without a cluster.
var fluxDeliveryAccessReview = func(ctx context.Context, kubeContext string, checks []fluxDeliveryAccessCheck) ([]fluxDeliveryAccessCheck, error) {
	var denied []fluxDeliveryAccessCheck
	for _, c := range checks {
		review := map[string]any{
			"apiVersion": "authorization.k8s.io/v1",
			"kind":       "SelfSubjectAccessReview",
			"spec": map[string]any{"resourceAttributes": map[string]any{
				"verb": c.Verb, "group": c.Group, "resource": c.Resource, "namespace": c.Namespace, "name": c.Name,
			}},
		}
		body, err := json.Marshal(review)
		if err != nil {
			return nil, err
		}
		// --validate=false: client-side validation lists CRDs first, which a
		// pointer-only credential may not, and a review is never persisted.
		cmd := cluster.KubectlCommand(ctx, "--context", kubeContext, "create", "--validate=false", "-f", "-", "-o", "json")
		cmd.Stdin = strings.NewReader(string(body))
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("ask %s whether this credential may %s: %w", kubeContext, c, kubectlErr(err))
		}
		var res struct {
			Status struct {
				Allowed bool `json:"allowed"`
			} `json:"status"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			return nil, fmt.Errorf("decode the access review for %s: %w", c, err)
		}
		if !res.Status.Allowed {
			denied = append(denied, c)
		}
	}
	return denied, nil
}

// fluxDeliveryNeeds derives a release's needs from its bundle's objects: the
// grants the bootstrap would make for them, expanded.
func fluxDeliveryNeeds(objs []bundle.RenderedObject, fd FluxDeliveryEntity) ([]fluxDeliveryPermission, error) {
	pre, apply, _, err := fluxDeliveryGrantsOf(objs, fd)
	if err != nil {
		return nil, err
	}
	return fluxDeliveryPermissions(pre, apply), nil
}

// fluxDeliveryGrantPlan is the grant preflight's verdict for one release.
type fluxDeliveryGrantPlan struct {
	// Missing is what the release needs and the record lacks; empty: the
	// release is covered.
	Missing []fluxDeliveryPermission
	// Regrant is true when Missing is non-empty and the caller's credential
	// can close it by re-running the bootstrap.
	Regrant bool
}

// planFluxDeliveryGrants is the preflight: diff the release's needs against
// the record, and when short, decide between re-granting and refusing.
func planFluxDeliveryGrants(ctx context.Context, env, rel string, fd FluxDeliveryEntity, objs []bundle.RenderedObject) (fluxDeliveryGrantPlan, error) {
	if !fd.ApplyIdentities {
		// kustomize-controller applies as itself: there is no grant to fall
		// short of.
		return fluxDeliveryGrantPlan{}, nil
	}
	needs, err := fluxDeliveryNeeds(objs, fd)
	if err != nil {
		return fluxDeliveryGrantPlan{}, fmt.Errorf("derive release %s's grant needs: %w", rel, err)
	}
	granted, err := fluxDeliveryReadGrants(ctx, fd)
	if err != nil {
		return fluxDeliveryGrantPlan{}, err
	}
	missing := fluxDeliveryShortfall(needs, granted)
	if len(missing) == 0 {
		return fluxDeliveryGrantPlan{}, nil
	}
	denied, err := fluxDeliveryAccessReview(ctx, fd.Cluster, fluxDeliveryRegrantChecks(fd, missing))
	if err != nil {
		return fluxDeliveryGrantPlan{}, err
	}
	if len(denied) > 0 {
		refusal := newFluxDeliveryGrantRefusal(env, rel, missing)
		refusal.CannotGrant = "it may not " + denied[0].String()
		if len(denied) > 1 {
			refusal.CannotGrant += fmt.Sprintf(", and %d more", len(denied)-1)
		}
		if !granted.Found {
			refusal.CannotGrant += "; the delivery namespace holds no grants record either, so the bootstrap may predate this forge"
		}
		return fluxDeliveryGrantPlan{Missing: missing}, refusal
	}
	return fluxDeliveryGrantPlan{Missing: missing, Regrant: true}, nil
}
