// Copyright (c) 2025 Reliant Labs
package doctor

// orphanedcluster.go — the "Orphaned Cluster Objects" check.
//
// It reports forge-managed CLUSTER-SCOPED objects that are PRESENT in a
// cluster and rendered by NO declared environment, so no future deploy will
// ever update, reclaim or delete them.
//
// WHY THIS IS A CLASS OF DEFECT AND NOT A ONE-OFF
//
// A namespaced object has an owner that can collect it: `forge env deploy`
// prunes forge-managed Deployments the render no longer contains
// (internal/cluster.Prune), and deleting the namespace takes the rest. A
// cluster-scoped object has neither. It sits outside every namespace, so
// nothing sweeps it up, and it is addressed by name alone — so the instant
// its RENDERED NAME changes, the object under the old name becomes
// unreachable by any forge command. It is not deleted, not updated, and not
// reported. It simply stops being mentioned.
//
// Renaming a cluster-scoped object is not exotic; it is the FIX for a worse
// defect. forge's own `render_cluster_rbac` used to name an operator's
// ClusterRole and ClusterRoleBinding after the operator alone, which made
// them env-invariant singletons: every env in a shared cluster wrote the
// same two objects while the binding's `subjects[0].namespace` was the
// deploying env's, so whichever env deployed last silently stole the
// binding and every other env's controller lost its cluster-scoped grants
// (see kcl/lib/rbac.k, and CheckObjectCollision which reports that shape).
// Suffixing the names with the namespace fixed it — and left the objects
// under the OLD names behind, in every cluster that had ever been deployed
// to.
//
// Measured in control-plane's k3d cluster right after that fix, four
// forge-managed cluster-scoped objects were rendered by none of the four
// declared envs: `workspace-controller-clusterrole`,
// `workspace-controller-clusterrolebinding`, and two
// `workspace-controller-operator-<namespace>` bindings left by a
// hand-rolled workaround that the forge fix allowed the project to delete.
// Nothing in forge named any of them.
//
// WHY IT WARNS, AND WHY THE WORDING IS A CORRECTNESS REQUIREMENT
//
// A listed object MAY STILL BE SERVING A LIVE ENVIRONMENT. An env whose
// last deploy PREDATES the rename is still bound to the old name: its
// controller pods are running, its ServiceAccount is unchanged, and its
// cluster-scoped grants come from the object this check just listed as
// unrendered. Deleting it strips those grants while leaving the pods up —
// leader-election leases and CR watches begin returning `is forbidden` on
// every tick, custom resources stop reconciling, and nothing appears broken
// until someone reads the controller's logs. That is the SAME outage the
// rename was made to end, reached from the other direction.
//
// So this check reports a DELTA, never a disposal list, and it says so on
// the line the reader actually sees. The safe order is always: redeploy
// every environment under the current names FIRST, verify each controller
// holds its own grants, and only THEN remove what is left. A check that
// read as "safe to delete" would cause the outage above, which is why the
// phrasing here is load-bearing rather than a matter of tone.
//
// WHAT IT DELIBERATELY STAYS SILENT ABOUT
//
// `app.kubernetes.io/managed-by=forge` is necessary but NOT sufficient to
// call something an orphan, and treating it as sufficient is how this check
// would have earned a permanent yellow on a correct project. forge stamps
// that label on PLATFORM objects too — the Envoy Gateway chart it installs
// via `forge cluster up` (internal/cluster.stampDocAppLabel), the pinned
// Gateway API CRDs, the `eg` GatewayClass. Those are cluster infrastructure
// every env expects to exist; no env's manifest render contains them and
// none ever will. Measured on the same cluster: a managed-by-only rule
// reported `gatewayclass/eg`, the `envoy-gateway-system` Namespace and the
// chart's own ClusterRole/ClusterRoleBinding alongside the four real
// orphans — four true findings buried in eight, with the false half
// permanent and unfixable.
//
// The discriminator is forge's per-env ownership stamp, `forge.dev/env`
// (kcl/lib/labels.k), which is applied at the manifest-render entry points
// and therefore marks exactly the objects that CAME FROM an environment's
// render. An object carrying `forge.dev/env=<E>` for a declared env E is
// CLAIMED by E, so E's render is the authority on whether it is still
// wanted. An object with no env stamp was not produced by an env render and
// is not judged here — its absence from every render is the normal, correct
// state, and the stamp's absence means nothing on its own (objects deployed
// before the stamp existed do not carry it either).
//
// That narrowing is what makes the report actionable in the other
// direction too: because the stamp names the env, a finding does not merely
// say "this is unrendered" — it says WHICH environment to redeploy to
// reclaim it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
)

// orphanedClusterCheckName is the display name.
const orphanedClusterCheckName = "Orphaned Cluster Objects"

const (
	// orphanProbeTimeout bounds ONE `kubectl get <kind>` call, for the same
	// reason clusterProbeTimeout does: a k3d cluster that is up but not
	// serving, or a VPN-less GKE context, hangs for kubectl's own default.
	orphanProbeTimeout = 6 * time.Second

	// orphanContextsTimeout bounds the one kubeconfig read. It touches no
	// network — it parses a local file — so it needs only to not hang on a
	// pathological kubeconfig.
	orphanContextsTimeout = 4 * time.Second
)

// clusterScopedKinds are the cluster-scoped kinds forge renders or
// installs, each with the kubectl resource name to list it by.
//
// An explicit list, not a discovery call. `kubectl api-resources
// --namespaced=false` would enumerate every cluster-scoped kind the API
// server knows — a hundred-plus on a cluster with operators — and this
// check has no business forming an opinion about objects forge never
// emits. Every entry below is a kind forge's own KCL or helm path can
// produce (kcl/lib/rbac.k, lib/crd.k, lib/gateway.k, render.k,
// workloads/render.k), so the probe set is bounded by what forge can
// actually have created.
//
// Namespace is in the list and is the most consequential entry: an
// orphaned Namespace is one whose env renders a DIFFERENT name now, and it
// holds every workload the old name ever had. It is exactly the object a
// reader most needs to see and least may delete casually, which is the
// check's whole thesis in one row.
var clusterScopedKinds = []string{
	"clusterrole",
	"clusterrolebinding",
	"namespace",
	"customresourcedefinition",
	"runtimeclass",
	"gatewayclass",
	"clusterissuer",
	"priorityclass",
	"storageclass",
	"validatingwebhookconfiguration",
	"mutatingwebhookconfiguration",
}

// liveClusterObject is one cluster-scoped object as the cluster reports it.
type liveClusterObject struct {
	// Kind is the object's own `kind`, as returned by the API server, so
	// the report says "ClusterRoleBinding" rather than the lowercase
	// resource name the probe asked by.
	Kind   string
	Name   string
	Labels map[string]string
}

// envTag is the environment that RENDERED this object, or "" when it did
// not come from an env render at all.
func (o liveClusterObject) envTag() string {
	return strings.TrimSpace(o.Labels[forgeEnvLabel])
}

// orphanProbe is the seam to the outside world, as two funcs rather than
// an interface: production supplies kubectl, tests supply fabricated
// objects and fabricated failures. Fabricated failure is the only way to
// pin "context present but unreachable ⇒ UNDETERMINED" without an
// unreachable cluster.
type orphanProbe struct {
	// contexts reports the kubectl context names the kubeconfig knows.
	// Read ONCE per run and used only to tell a context this machine has
	// never heard of (SKIP — not this machine's cluster to answer for)
	// from one it knows but could not reach (UNDETERMINED — it is, and
	// forge could not look). Collapsing those two into one status is what
	// would make this check either permanently yellow in CI or silently
	// blind on a developer machine.
	contexts func(ctx context.Context) (map[string]bool, error)
	// list returns the forge-managed objects of one cluster-scoped kind on
	// one context. A kind whose CRD is not installed is NOT an error: it
	// answers with no objects, because a kind the cluster does not know
	// cannot be holding an orphan.
	list func(ctx context.Context, kctx, kind string) ([]liveClusterObject, error)
}

// CheckOrphanedClusterObjects reports forge-managed cluster-scoped objects
// that exist in a cluster but are rendered by no declared environment. See
// the file comment for the defect, the measured incident, and why the
// "may still be serving a live environment" caveat is a correctness
// requirement rather than a hedge.
func CheckOrphanedClusterObjects(ctx context.Context, env *Environment) CheckResult {
	return checkOrphanedClusterObjects(ctx, env, orphanProbe{
		contexts: kubectlContexts,
		list:     kubectlClusterScoped,
	})
}

// checkOrphanedClusterObjects is the whole check minus the cluster, so the
// tests can state both halves of its contract — a planted orphan is found,
// and a fully-rendered cluster is silent — without a cluster.
func checkOrphanedClusterObjects(ctx context.Context, env *Environment, probe orphanProbe) CheckResult {
	scope, early := renderScopeOf(env, "orphaned cluster-scoped objects", StatusUnknown)
	if early != nil {
		return *early
	}

	// A PARTIAL render must never produce an orphan LIST, and this is the
	// one place that rule is stricter than renderScope.fold's.
	//
	// fold keeps a Warn and prefixes it with the scope, which is right for
	// a check whose findings are independent of the missing env: a
	// plaintext credential in prod is one whether or not staging rendered.
	// Here the finding is the ABSENCE of an object from every render, so an
	// env that went unread does not narrow the answer — it FABRICATES it.
	// Every object that env renders, and only that env, is indistinguishable
	// from an orphan. Those are the entries most likely to be load-bearing
	// (the env still deploys them) and a scope prefix on a list of them is
	// no protection at all: the reader acts on the list, not the preamble.
	//
	// So an incomplete render yields no list. This is a hole, reported as
	// one, and it names the env to fix first.
	if len(scope.unread) > 0 {
		return CheckResult{
			Status: StatusUnknown,
			Message: fmt.Sprintf(
				"%scannot tell an orphan from a live object: every object %s renders would look unrendered",
				scope.scopePrefix(), strings.Join(scope.unreadNames(), ", ")),
			Evidence: "An object is judged orphaned by being absent from EVERY environment's render, so an\n" +
				"environment that did not render cannot be distinguished from one that renders nothing.\n" +
				"Listing orphans now would invent them — and the invented ones would be exactly the\n" +
				"objects that environment still deploys. Fix the render first:\n\n" +
				scope.unreadEvidence(),
		}
	}

	rendered, contexts := renderedClusterScoped(scope.usable)
	declared := declaredEnvNames(scope.all)
	if len(contexts) == 0 {
		return CheckResult{
			Status:  StatusSkip,
			Message: "no environment declares a cluster — there is no cluster-scoped state to own",
		}
	}

	known, cerr := probe.contexts(ctx)
	if cerr != nil {
		// The kubeconfig is this check's only means of telling "not my
		// cluster" from "my cluster, unreachable", so without it neither
		// answer can be given honestly.
		return CheckResult{
			Status: StatusUnknown,
			Message: fmt.Sprintf("could not read the kubectl contexts, so the %d declared cluster(s) could not be inspected",
				len(contexts)),
			Evidence: cerr.Error(),
		}
	}

	var orphans, foreign, holes, skipped []string
	inspected := 0
	for _, kctx := range contexts {
		if !known[kctx] {
			// This machine's kubeconfig has never heard of the context, so
			// it is not the machine that deploys this env and has nothing
			// to clean up. SKIP, not UNDETERMINED: a CI runner with no
			// kubeconfig must not yellow the report over a cluster it is
			// not responsible for.
			skipped = append(skipped, fmt.Sprintf("%s: not in this machine's kubeconfig — not a cluster this checkout deploys to", kctx))
			continue
		}
		live, lerr := listClusterScoped(ctx, probe, kctx)
		if lerr != nil {
			// The context IS declared and IS in the kubeconfig, so this
			// machine does deploy here and forge could not look.
			holes = append(holes, fmt.Sprintf("%s: %v", kctx, lerr))
			continue
		}
		inspected++
		o, f := judgeCluster(kctx, live, rendered, declared)
		orphans = append(orphans, o...)
		foreign = append(foreign, f...)
	}
	sort.Strings(orphans)
	sort.Strings(foreign)
	sort.Strings(holes)
	sort.Strings(skipped)

	return summariseOrphans(orphanSummary{
		orphans:   orphans,
		foreign:   foreign,
		holes:     holes,
		skipped:   skipped,
		inspected: inspected,
		clusters:  len(contexts),
		envs:      len(scope.usable),
		rendered:  len(rendered),
	})
}

// clusterAddress is a cluster-scoped object's identity: the cluster it
// lives on, its kind, and its name. No namespace — having none is what
// makes these objects unreclaimable, and it is the whole subject of this
// check.
//
// The cluster is part of the key deliberately. An object rendered into
// cluster A does not excuse an identically-named object left behind in
// cluster B; they are two objects, and only one of them is wanted.
type clusterAddress struct {
	cluster string
	kind    string
	name    string
}

// renderedClusterScoped collects every cluster-scoped address the declared
// environments render, and the clusters they render onto.
//
// An object is taken as cluster-scoped when its render carries no
// `metadata.namespace`. A NAMESPACED object rendered without one (relying
// on the apply-time default) is therefore also collected — harmlessly: it
// adds a name to the wanted set that no cluster-scoped listing can match,
// which can only ever suppress a finding, never invent one. Erring in that
// direction is deliberate; the opposite error is what deletes something
// live.
func renderedClusterScoped(renders []envRender) (map[clusterAddress]string, []string) {
	wanted := map[clusterAddress]string{}
	seen := map[string]bool{}
	for _, r := range renders {
		for _, o := range r.objects {
			kind, name := strings.TrimSpace(o.Kind), strings.TrimSpace(o.Metadata.Name)
			if kind == "" || name == "" {
				continue // not addressable — CheckDeployManifests' finding
			}
			for _, c := range r.clustersOf(o) {
				if c == "" {
					continue
				}
				seen[c] = true
				if strings.TrimSpace(o.Metadata.Namespace) != "" {
					continue
				}
				wanted[clusterAddress{cluster: c, kind: strings.ToLower(kind), name: name}] = r.env
			}
		}
	}
	// Every cluster any env deploys to, whether or not it renders a
	// cluster-scoped object there. An env that renders none TODAY is
	// precisely an env that may have left some behind.
	for _, r := range renders {
		for _, c := range r.clusters {
			if c != "" {
				seen[c] = true
			}
		}
	}
	contexts := make([]string, 0, len(seen))
	for c := range seen {
		contexts = append(contexts, c)
	}
	sort.Strings(contexts)
	return wanted, contexts
}

// declaredEnvNames is every environment the project declares, failures
// included. Read from the DECLARATIONS rather than from the successful
// renders so a stamp naming a declared env is never mistaken for a stamp
// naming a stranger.
func declaredEnvNames(all []envRender) map[string]bool {
	names := map[string]bool{}
	for _, r := range all {
		if n := strings.TrimSpace(r.env); n != "" {
			names[n] = true
		}
	}
	return names
}

// listClusterScoped lists every forge-managed object of every
// cluster-scoped kind on one context.
//
// Each kind is probed SEPARATELY, and that is not a style choice.
// `kubectl get clusterrole,clusterissuer` against a cluster without
// cert-manager fails the WHOLE request with `the server doesn't have a
// resource type "clusterissuer"` and returns nothing at all — verified
// against k3d-control-plane. One combined call would therefore have
// reported an UNDETERMINED on every cluster missing any optional CRD,
// which is most of them, and this check would never have produced a
// finding on a real cluster.
func listClusterScoped(ctx context.Context, probe orphanProbe, kctx string) ([]liveClusterObject, error) {
	var all []liveClusterObject
	var failures []string
	for _, kind := range clusterScopedKinds {
		objs, err := probe.list(ctx, kctx, kind)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s (%v)", kind, err))
			continue
		}
		all = append(all, objs...)
	}
	// A partial listing is a hole, never a clean answer: the kinds that
	// failed are exactly where an unlisted orphan would be hiding, and
	// reporting the kinds that DID answer as the whole picture is the
	// false all-clear this check exists to prevent.
	if len(failures) > 0 {
		return nil, fmt.Errorf("could not list %d of %d cluster-scoped kind(s): %s",
			len(failures), len(clusterScopedKinds), strings.Join(failures, "; "))
	}
	return all, nil
}

// judgeCluster splits one cluster's live objects into orphans and
// not-judgeable foreign-stamped objects. Everything else — an object with
// no env stamp, or one whose address a declared env still renders — is
// silent by design.
func judgeCluster(kctx string, live []liveClusterObject, rendered map[clusterAddress]string, declared map[string]bool) (orphans, foreign []string) {
	for _, o := range live {
		name := strings.TrimSpace(o.Name)
		kind := strings.TrimSpace(o.Kind)
		if name == "" || kind == "" {
			continue
		}
		tag := o.envTag()
		if tag == "" {
			// Not produced by an environment's manifest render: the Envoy
			// Gateway chart, the pinned Gateway API CRDs, the `eg`
			// GatewayClass, or anything predating the stamp. Being absent
			// from every render is its normal state. See the file comment.
			continue
		}
		addr := clusterAddress{cluster: kctx, kind: strings.ToLower(kind), name: name}
		if _, wanted := rendered[addr]; wanted {
			continue
		}
		if !declared[tag] {
			// Stamped by an env this project does not declare — another
			// checkout, another branch, a deleted env. forge cannot render
			// what it cannot see, so it cannot call this unwanted.
			foreign = append(foreign, fmt.Sprintf("%s  %s/%s  (stamped forge.dev/env=%s, which this project does not declare)",
				kctx, kind, name, tag))
			continue
		}
		orphans = append(orphans, fmt.Sprintf("%s  %s/%s  (rendered by no env; stamped forge.dev/env=%s — redeploy %s to reclaim it)",
			kctx, kind, name, tag, tag))
	}
	return orphans, foreign
}

// orphanSummary is everything one run learned.
type orphanSummary struct {
	orphans   []string
	foreign   []string
	holes     []string
	skipped   []string
	inspected int
	clusters  int
	envs      int
	rendered  int
}

// theCaveat is the sentence that makes this check safe to act on, and it
// appears on every result that lists anything.
//
// It is not a hedge. An environment whose last deploy predates a rename is
// still bound to the OLD name: its pods are up, its ServiceAccount is
// unchanged, and its cluster-scoped grants come from the object listed
// here. Deleting that object strips the grants and leaves the pods
// running, so leases and watches start returning `is forbidden` and
// nothing looks broken until someone reads the logs — the same outage the
// rename was made to end. A reader who acts on this list as a disposal
// list causes it.
const theCaveat = "AN OBJECT LISTED HERE MAY STILL BE SERVING A LIVE ENVIRONMENT. \"No env renders it\" is a\n" +
	"fact about the CURRENT render, not about the cluster: an env whose last deploy predates a\n" +
	"rename is still bound to the OLD name, with its pods up and its grants coming from the\n" +
	"object below. Deleting it strips those grants while the pods keep running — leases and\n" +
	"watches begin returning `is forbidden` and nothing appears broken until someone reads the\n" +
	"logs.\n\n" +
	"This is a DELTA, not a disposal list. Safe order: (1) redeploy EVERY declared env so each\n" +
	"writes its current names, (2) verify each workload is granted THROUGH AN OBJECT THE\n" +
	"CURRENT RENDER PRODUCES, (3) only then delete what is still listed.\n\n" +
	"STEP 2 CANNOT BE DONE WITH `kubectl auth can-i`. That answers \"does this identity have\n" +
	"the permission\", which is YES while the orphan is present — whether or not the redeploy\n" +
	"landed. It gives the same answer in the state you are trying to reach and the state you\n" +
	"are trying to leave, so it cannot distinguish them. Ask which binding grants the subject\n" +
	"instead, and read its roleRef:\n\n" +
	"  kubectl get clusterrolebinding -o json | jq -r '.items[]\n" +
	"    | select(.subjects[]? | .kind==\"ServiceAccount\" and .name==\"<sa>\" and .namespace==\"<ns>\")\n" +
	"    | \"\\(.metadata.name) -> \\(.roleRef.name)\"'\n\n" +
	"The env stands on its own only when a name from the CURRENT render appears there."

// summariseOrphans rolls the run up.
//
// Precedence is WARN(orphans) > UNDETERMINED(holes) > PASS, which inverts
// CheckClusterWorkloads' Fail > Unknown > Warn on purpose. There, a hole
// outranks a warning because the hole was the defect being closed. Here
// the orphan list IS the deliverable — the delta exists precisely because
// nothing in forge was reporting it — so demoting a real finding to
// "undetermined" over an unreachable second cluster would reintroduce the
// invisibility. The hole is not dropped: it rides on the one line the
// reader sees, the way clusterhealth carries its own.
func summariseOrphans(s orphanSummary) CheckResult {
	res := CheckResult{}
	switch {
	case len(s.orphans) > 0:
		res.Status = StatusWarn
		res.Message = fmt.Sprintf(
			"%d forge-managed cluster-scoped object(s) are rendered by NO declared environment — no deploy will ever update or remove them, and one may still be serving an env that has not redeployed%s",
			len(s.orphans), orphanHoleSuffix(s.holes))
		res.Evidence = evidenceLines(s.orphans) + "\n\n" + theCaveat
		res.Evidence += orphanFootnotes(s)
	case len(s.holes) > 0:
		res.Status = StatusUnknown
		res.Message = fmt.Sprintf(
			"%d of %d declared cluster(s) could not be inspected, so whether any cluster-scoped object is orphaned is unknown",
			len(s.holes), s.clusters)
		res.Evidence = "Could not obtain these facts:\n" + evidenceLines(s.holes) + orphanFootnotes(s)
	case s.inspected == 0:
		res.Status = StatusSkip
		res.Message = fmt.Sprintf("none of the %d declared cluster(s) is reachable from this machine — no cluster-scoped state to answer for here",
			s.clusters)
		res.Evidence = evidenceLines(s.skipped) + orphanFootnotes(s)
	default:
		res.Status = StatusPass
		res.Message = fmt.Sprintf(
			"every forge-rendered cluster-scoped object in %d of %d cluster(s) is still rendered by a declared env (%d cluster-scoped address(es) across %d env(s))",
			s.inspected, s.clusters, s.rendered, s.envs)
		res.Evidence = orphanFootnotes(s)
	}
	return res
}

// orphanHoleSuffix carries an unreachable cluster onto a line that already
// has a finding. A report that says "4 orphaned" while silently dropping
// "and one cluster never answered" is a smaller copy of the invisibility
// this check closes.
func orphanHoleSuffix(holes []string) string {
	if len(holes) == 0 {
		return ""
	}
	return fmt.Sprintf("  + %d cluster(s) NOT INSPECTED (-v)", len(holes))
}

// orphanFootnotes appends the things that are context rather than finding:
// clusters not inspected, and objects forge deliberately declined to judge.
func orphanFootnotes(s orphanSummary) string {
	var b strings.Builder
	if len(s.holes) > 0 && len(s.orphans) > 0 {
		fmt.Fprintf(&b, "\n\nNot inspected (so this list may be incomplete):\n%s", evidenceLines(s.holes))
	}
	if len(s.skipped) > 0 {
		fmt.Fprintf(&b, "\n\nNot this machine's clusters:\n%s", evidenceLines(s.skipped))
	}
	if len(s.foreign) > 0 {
		fmt.Fprintf(&b, "\n\nNot judged — stamped by an environment this project does not declare, so forge\n"+
			"cannot know whether it is still wanted (another checkout or branch may own it):\n%s",
			evidenceLines(s.foreign))
	}
	return b.String()
}

// kubectlContexts reads the context names out of the kubeconfig. It makes
// no network call: `config get-contexts` parses the local file, which is
// why it can answer "does this machine even know this cluster" without
// waiting on an unreachable API server.
func kubectlContexts(ctx context.Context) (map[string]bool, error) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return nil, fmt.Errorf("kubectl is not on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, orphanContextsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "config", "get-contexts", "-o", "name")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl config get-contexts: %s", firstStderrLine(stderr.String(), err))
	}
	names := map[string]bool{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if n := strings.TrimSpace(line); n != "" {
			names[n] = true
		}
	}
	return names, nil
}

// kubectlClusterScoped lists the forge-managed objects of ONE
// cluster-scoped kind on ONE declared context.
//
// The managed-by filter is applied by the API SERVER, not in Go, the same
// way internal/cluster.ListManagedDeployments does it: the label selector
// is what keeps a user's hand-applied ClusterRole out of a report that
// suggests deleting things.
//
// The context is always explicit and an empty one is refused rather than
// becoming kubectl's current context — k3d flips current-context, and a
// check that reported orphans from a cluster the project has nothing to do
// with would be recommending the deletion of someone else's objects.
func kubectlClusterScoped(ctx context.Context, kctx, kind string) ([]liveClusterObject, error) {
	if strings.TrimSpace(kctx) == "" {
		return nil, fmt.Errorf("no kubectl context declared (forge never falls back to the current context)")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return nil, fmt.Errorf("kubectl is not on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, orphanProbeTimeout)
	defer cancel()

	args := cluster.KubectlArgs(kctx,
		"get", kind,
		"-l", "app.kubernetes.io/managed-by=forge",
		"-o", "json",
		"--request-timeout="+orphanProbeTimeout.String())
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A kind the cluster does not serve is NOT a hole. cert-manager's
		// ClusterIssuer and the Gateway API's GatewayClass are optional
		// CRDs, absent on most clusters, and a kind the API server has
		// never heard of cannot be holding an orphan of that kind. Treating
		// it as an error would make every cluster without cert-manager
		// report UNDETERMINED forever.
		if missingResourceKind(stderr.String()) {
			return nil, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("context %q did not answer within %s", kctx, orphanProbeTimeout)
		}
		return nil, fmt.Errorf("%s", firstStderrLine(stderr.String(), err))
	}
	// kubectl prints the "doesn't have a resource type" message on stderr
	// and still EXITS 0 (verified against k3d-control-plane), so the
	// success path has to test for it too — otherwise the empty body below
	// parses as "no objects" and a whole kind goes silently unlisted.
	if missingResourceKind(stderr.String()) {
		return nil, nil
	}

	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		return nil, fmt.Errorf("could not parse kubectl output for %s: %w", kind, err)
	}
	out := make([]liveClusterObject, 0, len(list.Items))
	for _, it := range list.Items {
		k := strings.TrimSpace(it.Kind)
		if k == "" {
			// A List response can omit per-item kind. Fall back to the kind
			// asked for, so the object is still reported rather than
			// silently dropped for lacking a field nobody promised.
			k = kind
		}
		out = append(out, liveClusterObject{
			Kind:   k,
			Name:   it.Metadata.Name,
			Labels: it.Metadata.Labels,
		})
	}
	return out, nil
}

// missingResourceKind recognises kubectl's "this cluster has no such kind"
// message, in both spellings it uses.
func missingResourceKind(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "the server doesn't have a resource type") ||
		strings.Contains(s, "no matches for kind")
}
