package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// The replicas handover: keeping a Deployment's pod count when forge stops
// declaring spec.replicas because an autoscaler now owns it.
//
// WHY THIS EXISTS
// ===============
// An autoscaled workload renders its Deployment WITHOUT spec.replicas, beside
// an autoscaling/v2 HorizontalPodAutoscaler whose scaleTargetRef names it: the
// HPA's controller writes the pod count through the scale subresource, and a
// render that kept stating it would fight the controller on every deploy.
//
// Dropping the field is not free, though, because of how server-side apply
// treats a field its applier stops declaring:
//
//  1. Before autoscaling, every forge apply stated `replicas: 4`, so forge's
//     field manager ("kubectl", operation Apply: forgeFieldManager) owns
//     f:spec.f:replicas.
//  2. The first autoscaled deploy omits it. SSA removes a field the applying
//     manager owned and no longer declares, unless ANOTHER manager also owns
//     it; nobody else does, so the field is removed, and the API server's
//     defaulting puts back the Deployment default, 1.
//  3. The Deployment rolls down from 4 pods to 1 at once. The HPA, created by
//     the same apply, raises it again only after its first sync and only to
//     what its metrics justify, so a live service loses most of its capacity
//     for at least a sync period, on the deploy that was meant to scale it.
//
// --force-conflicts changes nothing here: it settles a field the apply
// configuration declares, and this one is declared by no one.
//
// THE FIX: KUBERNETES' OWN HANDOVER RECIPE
// =========================================
// Before the main apply, a second field manager, forge-replicas-handover,
// server-side applies a minimal object that states ONLY spec.replicas, at the
// live value (clamped into the HPA's [minReplicas, maxReplicas]). Two Apply
// managers stating one value share the field, so when the main apply then
// drops it, forge's manager gives up its share and the value stays, owned by
// the handover manager alone. The HPA controller's first write through /scale
// changes the value, which moves ownership to the controller; the handover
// manager then owns nothing, and the API server deletes its managedFields
// entry by itself. This is the procedure the Kubernetes documentation gives
// for exactly this transition (Server-Side Apply, "Transferring ownership":
// "a new manifest containing only the replicas field", applied
// `--server-side --field-manager=handover-to-hpa --validate=false`), with one
// deliberate difference: --force-conflicts. The documented step runs without
// it and says to do nothing on a conflict with the HPA controller. Here a
// conflict can only come from forge's own manager when the clamp CHANGES the
// value (live 12, maxReplicas 10): forge's manager still owns the field at 12,
// and taking it at 10 is the intent. The one other way to conflict is a race
// in which the HPA wrote between this read and this apply; forcing then writes
// a value inside the HPA's own bounds, which the controller corrects on its
// next sync. Neither is worth failing a deploy over.
//
// --validate=false is kept from the documented step, though the kubectl
// measured here does not need it: kubectl v1.36 against a v1.37 API server
// accepts the partial object under the default, --validate=true and
// --validate=strict alike, because those only ask the server to refuse
// unknown or duplicate fields. The flag keeps a kubectl that validates the
// object client-side, as a whole Deployment, from refusing it for the
// selector and template it deliberately lacks; no older kubectl was measured.
//
// A partial apply configuration is valid SSA for an object that exists: the
// request carries only the fields this manager has an opinion on, and the
// server validates the object MERGED with the live one. It is not valid for an
// object that does not exist (measured: "spec.selector: Required value"), and
// that is never sent: a Deployment that is not live yet is created by the main
// apply without replicas, starts at the default 1, and the HPA raises it to
// minReplicas. (One deleted between the read and the handover fails the deploy
// with that error; the retry creates it.)
//
// WHY AFTER convergeFieldOwnership
// ================================
// convergeFieldOwnership (field_ownership.go) runs first, and it can MOVE
// spec.replicas into forge's entry: a Deployment last written by a
// client-side `kubectl apply` has it under kubectl-client-side-apply, which
// convergence takes over. The ownership read below has to see that result, so
// the handover runs between the two, on the same live state the main apply
// will merge with. Convergence never takes the handover's own field back: it
// takes over only managers named kubectl* (and before-first-apply), on the
// main resource.
//
// IDEMPOTENT, AND SELF-RETIRING
// =============================
// The step runs only while forge's manager owns f:spec.f:replicas on a
// Deployment the stream declares without it. After the first autoscaled
// deploy forge's manager no longer owns it, so later deploys read the live
// object and write nothing. A deploy that failed after the handover and before
// the main apply runs it again, harmlessly: the same value under the same
// manager. Turning autoscale OFF needs no step at all: the render states
// replicas again, --force-conflicts takes the field back from whoever holds
// it (the handover manager or the HPA controller), and the owned-object prune
// deletes the HPA the render stopped producing (ownedPrunableKinds).
//
// WHAT WAS MEASURED, AND WHAT WAS NOT
// ===================================
// Against a real kube-apiserver (envtest's k8s 1.37 binaries, kubectl v1.36),
// in replicas_handover_envtest_test.go: a Deployment applied
// at 4 replicas by forge's manager and then applied without replicas drops to
// 1; the same transition through forge's apply path keeps 4, the handover
// manager is left owning spec.replicas and forge's manager is not; a second
// deploy writes nothing; turning autoscale off takes the field back at the
// declared count; and the clamp lowers a count above maxReplicas. The same
// server answers the metrics discovery read with "Error from server
// (NotFound)" when no APIService serves metrics.k8s.io/v1beta1, and with
// "(ServiceUnavailable)" when one is registered but its backend is absent.
// envtest runs no controllers, so the HPA controller's write through /scale,
// and the API server dropping the emptied handover entry after it, are
// Kubernetes' documented behaviour, not something measured here.
//
// Detection reads only the manifest stream: a Deployment that declares no
// spec.replicas, and an HPA in the same namespace whose scaleTargetRef names
// it. It applies to any such pair, a chart's included: the drop to 1 is the
// same for every applier, and so is the cure.

// replicasHandoverManager is the field manager that holds spec.replicas
// between forge's last apply that stated it and the HPA controller's first
// scale. It owns nothing else, ever.
const replicasHandoverManager = "forge-replicas-handover"

// replicasPath is f:spec.f:replicas in managedFields terms.
var replicasPath = fieldpath.MakePathOrDie("spec", "replicas")

// autoscaledDeployment is one Deployment the stream declares without
// spec.replicas, paired with the HPA that scales it.
type autoscaledDeployment struct {
	Name string
	// Namespace as the Deployment document declares it; "" takes the
	// apply's default namespace.
	Namespace string
	// MinReplicas and MaxReplicas are the HPA's bounds. A missing
	// minReplicas is the API's default, 1.
	MinReplicas int64
	MaxReplicas int64
	// ForgeWorkload is true when the Deployment or its HPA carries
	// forge.dev/kind: a workload forge rendered from a forge.Workload
	// that declares autoscale, rather than a chart's or a hand-written one.
	ForgeWorkload bool
}

// forgeKindLabel is forge.dev/kind (pkg/deploy LabelKind), which forge stamps
// on every object one forge workload renders.
const forgeKindLabel = "forge.dev/kind"

// autoscaledDeployments finds every autoscaled Deployment in a manifest
// stream, in stream order. namespace is the apply's default namespace, which
// a document that names none lands in; an HPA and its target must resolve to
// the same one, because a scaleTargetRef never crosses namespaces.
func autoscaledDeployments(manifests, namespace string) []autoscaledDeployment {
	type hpaBounds struct {
		min, max int64
		forge    bool
	}
	type ref struct{ ns, name string }
	var deps []autoscaledDeployment
	var depRefs []ref
	hpas := map[ref]hpaBounds{}
	resolve := func(ns string) string {
		if ns == "" {
			return namespace
		}
		return ns
	}
	for _, doc := range splitDocs(manifests) {
		m, err := docJSON(doc)
		if err != nil || m == nil {
			continue
		}
		apiVersion, _ := m["apiVersion"].(string)
		kind, _ := m["kind"].(string)
		md, _ := m["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		ns, _ := md["namespace"].(string)
		spec, _ := m["spec"].(map[string]any)
		if name == "" {
			continue
		}
		switch {
		case kind == "Deployment" && apiGroup(apiVersion) == "apps":
			// kubectl strips an explicit null before sending, so a null
			// replicas is the same as an absent one.
			if r, ok := spec["replicas"]; ok && r != nil {
				continue
			}
			deps = append(deps, autoscaledDeployment{Name: name, Namespace: ns, ForgeWorkload: hasLabel(md, forgeKindLabel)})
			depRefs = append(depRefs, ref{resolve(ns), name})
		case kind == "HorizontalPodAutoscaler" && apiGroup(apiVersion) == "autoscaling":
			target, _ := spec["scaleTargetRef"].(map[string]any)
			tKind, _ := target["kind"].(string)
			tVersion, _ := target["apiVersion"].(string)
			tName, _ := target["name"].(string)
			if tKind != "Deployment" || apiGroup(tVersion) != "apps" || tName == "" {
				continue
			}
			b := hpaBounds{min: 1, forge: hasLabel(md, forgeKindLabel)}
			if v, ok := int64At(spec, "minReplicas"); ok {
				b.min = v
			}
			b.max, _ = int64At(spec, "maxReplicas")
			hpas[ref{resolve(ns), tName}] = b
		}
	}
	var out []autoscaledDeployment
	seen := map[ref]bool{}
	for i, d := range deps {
		b, ok := hpas[depRefs[i]]
		if !ok || seen[depRefs[i]] {
			continue
		}
		seen[depRefs[i]] = true
		d.MinReplicas, d.MaxReplicas = b.min, b.max
		d.ForgeWorkload = d.ForgeWorkload || b.forge
		out = append(out, d)
	}
	return out
}

func hasLabel(md map[string]any, key string) bool {
	labels, _ := md["labels"].(map[string]any)
	_, ok := labels[key]
	return ok
}

// int64At reads an integer field of a decoded object (json.Number, as
// docJSON and decodeObjects decode, or a float64).
func int64At(m map[string]any, key string) (int64, bool) {
	switch v := m[key].(type) {
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	}
	return 0, false
}

// clampReplicas puts a live replica count inside the HPA's bounds. A maximum
// below 1 is no bound (the API refuses one, and forge's validation caps it).
func clampReplicas(v, minReplicas, maxReplicas int64) int64 {
	if v < minReplicas {
		v = minReplicas
	}
	if maxReplicas >= 1 && v > maxReplicas {
		v = maxReplicas
	}
	return v
}

// forgeOwnsReplicas reports whether forge's own apply manager owns
// f:spec.f:replicas on the live object: the condition under which dropping
// the field from the render would reset it to 1.
func forgeOwnsReplicas(live liveObject) (bool, error) {
	for _, e := range live.entries {
		if e.Manager != forgeFieldManager || e.Operation != metav1.ManagedFieldsOperationApply || e.Subresource != "" {
			continue
		}
		s, err := fieldSet(e)
		if err != nil {
			return false, fmt.Errorf("%s: %w", live.key, err)
		}
		if s.Has(replicasPath) {
			return true, nil
		}
	}
	return false, nil
}

// replicasHandoverManifest is the whole apply configuration the handover
// sends: the Deployment's identity and spec.replicas, nothing else.
func replicasHandoverManifest(name, namespace string, replicas int64) []byte {
	md := map[string]any{"name": name}
	if namespace != "" {
		md["namespace"] = namespace
	}
	b, _ := json.Marshal(map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   md,
		"spec":       map[string]any{"replicas": replicas},
	})
	return b
}

// replicasHandoverArgs is the handover's kubectl argv (after the --context
// pair). The manifest names its namespace, so no -n is passed.
func replicasHandoverArgs() []string {
	return []string{"apply", "--server-side", "--force-conflicts",
		"--field-manager=" + replicasHandoverManager, "--validate=false", "-f", "-"}
}

// handoverReplicas runs the handover (see the comment at the top of this
// file) for every autoscaled Deployment in the stream that is live and whose
// spec.replicas forge's manager still owns. namespace is the apply's default
// namespace. out receives one line per Deployment handed over.
func handoverReplicas(ctx context.Context, run kubectlRun, namespace, manifests string, out io.Writer) error {
	targets := autoscaledDeployments(manifests, namespace)
	if len(targets) == 0 {
		return nil
	}
	docs := make([]string, 0, len(targets))
	byName := map[string][]autoscaledDeployment{}
	for _, t := range targets {
		docs = append(docs, string(replicasIdentity(t.Name, t.Namespace)))
		byName[t.Name] = append(byName[t.Name], t)
	}
	live, err := readLive(ctx, run, namespace, docs)
	if err != nil {
		return fmt.Errorf("reading the autoscaled Deployments before handing their replicas to the HPA: %w", err)
	}
	for _, o := range live {
		t, ok := matchTarget(byName[o.key.Name], o.key.Namespace, namespace)
		if !ok || o.key.Kind != "Deployment" {
			continue
		}
		owned, err := forgeOwnsReplicas(o)
		if err != nil {
			return err
		}
		if !owned {
			continue
		}
		spec, _ := o.raw["spec"].(map[string]any)
		current, ok := int64At(spec, "replicas")
		if !ok {
			current = t.MinReplicas
		}
		want := clampReplicas(current, t.MinReplicas, t.MaxReplicas)
		_, stderr, err := run(ctx, replicasHandoverArgs(), replicasHandoverManifest(o.key.Name, o.key.Namespace, want))
		if err != nil {
			return fmt.Errorf("hand spec.replicas of %s to its HorizontalPodAutoscaler: %w: %s", o.key, err, strings.TrimSpace(string(stderr)))
		}
		if out != nil {
			note := ""
			if want != current {
				note = fmt.Sprintf(", clamped from %d into the autoscaler's [%d, %d]", current, t.MinReplicas, t.MaxReplicas)
			}
			fmt.Fprintf(out, "[deploy]   %s: keeping spec.replicas at %d%s while its HorizontalPodAutoscaler takes it over (field manager %s)\n",
				o.key, want, note, replicasHandoverManager)
		}
	}
	return nil
}

// replicasIdentity is the get-by-file identity of one Deployment.
func replicasIdentity(name, namespace string) []byte {
	md := map[string]any{"name": name}
	if namespace != "" {
		md["namespace"] = namespace
	}
	b, _ := json.Marshal(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": md})
	return b
}

// matchTarget picks the declared target a live object answers for: the one
// whose namespace resolves to the live object's. A namespace that resolved to
// nothing (no -n, no metadata.namespace) went to kubectl's own default, which
// only the live object knows, so it matches on the name alone.
func matchTarget(cands []autoscaledDeployment, liveNS, defaultNS string) (autoscaledDeployment, bool) {
	for _, c := range cands {
		ns := c.Namespace
		if ns == "" {
			ns = defaultNS
		}
		if ns == "" || ns == liveNS {
			return c, true
		}
	}
	return autoscaledDeployment{}, false
}

// metricsAPIPath is the resource-metrics API the HPA controller reads CPU
// utilization from. metrics-server serves it, through an APIService.
const metricsAPIPath = "/apis/metrics.k8s.io/v1beta1"

// refuseAutoscaleWithoutMetrics refuses a stream that declares autoscaled
// forge workloads when the target cluster does not serve the resource-metrics
// API. An HPA there can never read CPU utilization, so it never scales, and
// the workload sits at whatever count it had, silently: a deploy that cannot
// do what it declares should fail before it writes, not look healthy.
//
// It is a read (one discovery GET) and runs only when the stream has a forge
// workload's autoscaled Deployment. A chart's or a hand-written HPA is not
// judged: forge did not promise that one would scale, and refusing it would
// fail deploys that work today.
//
// It refuses only on a CONFIRMED absence: the server answering NotFound for
// the API's discovery path, which is what an apiserver with no APIService for
// metrics.k8s.io/v1beta1 says (measured; see the file comment). Any other
// failure (the APIService is registered
// but its backend is not ready yet, as right after a k3d cluster boots; RBAC
// refusing the discovery read; the API server unreachable) cannot say the API
// is missing, so it warns and lets the apply proceed: the HPA starts scaling
// when metrics-server is up, and an unreachable server fails the apply on its
// own.
func refuseAutoscaleWithoutMetrics(ctx context.Context, run kubectlRun, kctx, namespace, manifests string, out io.Writer) error {
	var workloads []string
	for _, d := range autoscaledDeployments(manifests, namespace) {
		if d.ForgeWorkload {
			workloads = append(workloads, d.Name)
		}
	}
	if len(workloads) == 0 {
		return nil
	}
	_, stderr, err := run(ctx, []string{"get", "--raw", metricsAPIPath}, nil)
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(stderr))
	if strings.Contains(msg, "(NotFound)") {
		return fmt.Errorf("%s", metricsMissingMessage(workloads, kctx))
	}
	if out != nil {
		fmt.Fprintf(out, "[deploy]   warning: could not confirm that cluster %s serves the metrics.k8s.io API that autoscaled workload(s) %s need (%v: %s); applying anyway — their HorizontalPodAutoscalers scale once metrics-server answers\n",
			kctx, strings.Join(workloads, ", "), err, msg)
	}
	return nil
}

// metricsMissingMessage is the refusal for an autoscaled workload on a cluster
// without the resource-metrics API.
func metricsMissingMessage(workloads []string, kctx string) string {
	return fmt.Sprintf("workload(s) %s declare autoscale, but cluster %s does not serve the metrics.k8s.io API (metrics-server); "+
		"a HorizontalPodAutoscaler there could never scale. Install metrics-server (k3d ships it unless created with "+
		"--disable=metrics-server), or remove autoscale", strings.Join(workloads, ", "), kctx)
}
