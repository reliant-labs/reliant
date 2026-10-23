package deploytarget

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
)

// Observe reads each service's Deployment status out of the cluster.
//
// # Why the Deployment's status and not the pods
//
// A Deployment's status subresource is the cluster's OWN summary of a
// rollout: the controller maintains updatedReplicas / readyReplicas /
// availableReplicas against the current pod template. Listing pods and
// counting them would re-derive that summary, and re-derive it WRONGLY
// during a rollout — a pod from the previous ReplicaSet is ready, and
// counting it as ready would report a rollout complete while half of it
// is still the old version. updatedReplicas is the field that carries
// that distinction, and it exists only on the Deployment.
//
// # Why the digest comes from the pod template
//
// The image on `.spec.template.spec.containers[0].image` is what the
// Deployment is CURRENTLY ASKING FOR. An image pinned by digest
// (`repo@sha256:…`) yields a content-addressed identity a reconciler can
// compare against a release ledger; an image pinned by mutable tag yields
// nothing comparable, so Digest stays empty rather than carrying a tag a
// caller might mistake for an identity. That empty is not a failure —
// forge's own --no-digest path produces it deliberately — so it does not
// downgrade health on its own.
//
// # Context is declarative, exactly as it is for a write
//
// The kubectl context is group.Cluster, with NO fallback to the active
// context. A write (cluster.Apply) refuses outright on an empty context; a read cannot corrupt anything, but it CAN report another
// cluster's state as this environment's, which is a worse outcome than
// no answer. So an empty context reports unknown rather than reading
// whatever context happens to be selected.
func (p K8sClusterProvider) Observe(ctx context.Context, group ServiceGroup) (Observed, error) {
	if group.Namespace == "" {
		return unsupported(p.Name(),
			"ServiceGroup.Namespace is empty (forge.yaml or K8sCluster.namespace must declare it)",
			observableNames(group))
	}
	kctx := p.declaredContext(group)
	if strings.TrimSpace(kctx) == "" {
		return unsupported(p.Name(),
			"no declared kubectl context (forge.K8sCluster.cluster in the env's KCL); "+
				"forge will not read a cluster it was not pointed at",
			observableNames(group))
	}

	out := Observed{
		ProviderID: p.Name(),
		ObservedAt: time.Now().UTC(),
		Items:      make([]ObservedItem, 0, len(group.Services)),
	}
	runner := p.runner()
	for _, svc := range group.Services {
		out.Items = append(out.Items, p.observeOne(ctx, runner, kctx, group.Namespace, svc))
	}
	return out, nil
}

// ownedClaims returns the PVCs forge emitted for this service, or nil.
// A service whose spec is absent (a misrouted group) owns nothing, which
// is the same answer as an ordinary cluster service — neither has a
// claim forge is accountable for.
func ownedClaims(svc ResolvedService) []string {
	if svc.K8sCluster == nil {
		return nil
	}
	return svc.K8sCluster.OwnedClaims
}

// observeOne reads one Deployment. Every failure path produces an item
// rather than an error, because one unreadable service must not erase the
// observations of its siblings — and an item that says "unknown, because
// X" is strictly more useful to a caller than a group-level error that
// says nothing about which service it was.
func (p K8sClusterProvider) observeOne(ctx context.Context, runner commandRunner, kctx, namespace string, svc ResolvedService) ObservedItem {
	name := svc.Name
	args := cluster.KubectlArgs(kctx, "get", "deployment", name, "-n", namespace, "-o", "json")
	raw, err := runner.Output(ctx, "kubectl", args...)
	if err != nil {
		// kubectl distinguishes "the object is not there" from "I could
		// not talk to the cluster" only in its stderr text. The
		// distinction matters enormously to a reconciler — absent means
		// "create it", unreachable means "do not touch anything" — so it
		// is worth reading the message rather than collapsing both into
		// unknown.
		if isKubectlNotFound(raw) {
			return ObservedItem{
				Name:   name,
				Health: HealthAbsent,
				Detail: fmt.Sprintf("no Deployment %q in namespace %q", name, namespace),
			}
		}
		return ObservedItem{
			Name:   name,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("kubectl get deployment failed: %v", err),
		}
	}

	var dep k8sDeployment
	if jerr := json.Unmarshal(raw, &dep); jerr != nil {
		return ObservedItem{
			Name:   name,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("could not parse kubectl output: %v", jerr),
		}
	}

	counts := ReplicaCounts{
		Desired:   dep.Spec.Replicas,
		Ready:     dep.Status.ReadyReplicas,
		Updated:   dep.Status.UpdatedReplicas,
		Available: dep.Status.AvailableReplicas,
	}
	item := ObservedItem{
		Name:     name,
		Digest:   digestFromImageRef(dep.firstImage()),
		Replicas: &counts,
		Health:   k8sHealth(counts),
	}
	if item.Health != HealthHealthy {
		item.Detail = fmt.Sprintf("%d/%d ready, %d updated, %d available",
			counts.Ready, counts.Desired, counts.Updated, counts.Available)
	}
	return p.withClaims(ctx, runner, kctx, namespace, ownedClaims(svc), item)
}

// withClaims folds the state of every PVC forge OWNS for this workload
// into the item, and is a no-op for the services that own none — which
// is every ordinary cluster service, so nothing about the existing
// observation changes shape.
//
// A claim that is not Bound DOWNGRADES health, and that is the whole
// reason this exists rather than being a cosmetic extra field. A
// Deployment blocked on an unbound PVC reports "0/1 ready", which is
// true and useless: it names the symptom while the cause sits one object
// away, and the two most common causes (no default StorageClass on this
// cluster; a storage_gib change the class refused) are invisible from
// the Deployment alone.
//
// It NEVER upgrades health. A bound claim beside a broken rollout is
// still a broken rollout, so the fold takes the worse of the two verdicts
// — a claim check that could turn degraded into healthy would be a
// second, weaker opinion about the same workload.
func (p K8sClusterProvider) withClaims(ctx context.Context, runner commandRunner, kctx, namespace string, claims []string, item ObservedItem) ObservedItem {
	for _, claim := range claims {
		phase, err := p.claimPhase(ctx, runner, kctx, namespace, claim)
		switch {
		case err != nil:
			// The workload's own numbers were measured and stay
			// reported, but health drops to unknown: forge emitted this
			// claim, so "I could not read a thing I created" is not a
			// state it may report as healthy.
			item.Health = worseHealth(item.Health, HealthUnknown)
			item.Detail = appendDetail(item.Detail, fmt.Sprintf(
				"could not read PersistentVolumeClaim %q (forge emits it for this service): %v", claim, err))
		case phase == "Bound":
			// Nothing to say. A bound claim is the expected state and
			// adding a line for it would bury the ones that matter.
		default:
			item.Health = worseHealth(item.Health, HealthDegraded)
			item.Detail = appendDetail(item.Detail, fmt.Sprintf(
				"PersistentVolumeClaim %q is %s, not Bound — the workload cannot start until it binds "+
					"(commonly: no default StorageClass on this cluster, or a storage size the class refused)",
				claim, phase))
		}
	}
	return item
}

// claimPhase reads one PVC's status phase. A claim that is not there at
// all reports "Absent" rather than an error: forge emitted it, so its
// absence is a MEASUREMENT (someone deleted it, or the apply never
// landed) and not a failure to look.
func (p K8sClusterProvider) claimPhase(ctx context.Context, runner commandRunner, kctx, namespace, claim string) (string, error) {
	args := cluster.KubectlArgs(kctx, "get", "pvc", claim, "-n", namespace, "-o", "json")
	raw, err := runner.Output(ctx, "kubectl", args...)
	if err != nil {
		if isKubectlNotFound(raw) {
			return "Absent", nil
		}
		return "", err
	}
	var pvc k8sPVC
	if jerr := json.Unmarshal(raw, &pvc); jerr != nil {
		return "", fmt.Errorf("could not parse kubectl output: %w", jerr)
	}
	if pvc.Status.Phase == "" {
		// A claim the API server has accepted but not yet given a phase.
		// Reporting "" would render as an empty word in the detail line.
		return "Pending", nil
	}
	return pvc.Status.Phase, nil
}

// k8sPVC is the slice of a PersistentVolumeClaim this observation needs,
// hand-declared for the same reason k8sDeployment is.
type k8sPVC struct {
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// worseHealth returns the more alarming of two verdicts, so a fold can
// only ever downgrade.
//
// The order is NOT the iota order, because the iota order is chosen to
// make unknown the zero value and that is a different question from
// which verdict is worse. Unknown is the worst thing to fold in — a
// measurement forge could not take must not be outranked by one it
// could — then absent, degraded, and healthy last.
func worseHealth(a, b Health) Health {
	if healthSeverity(a) >= healthSeverity(b) {
		return a
	}
	return b
}

func healthSeverity(h Health) int {
	switch h {
	case HealthUnknown:
		return 3
	case HealthAbsent:
		return 2
	case HealthDegraded:
		return 1
	default: // HealthHealthy
		return 0
	}
}

// appendDetail joins detail lines with "; " so a workload with several
// findings reports all of them rather than the last one to be written.
func appendDetail(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// k8sHealth turns replica counts into a verdict.
//
// Healthy requires ALL THREE of ready, updated and available to have
// reached desired. Requiring only `ready` would call a rollout healthy
// while the old ReplicaSet still serves most of the traffic; requiring
// only `available` would ignore whether the pods are the current
// template. A desired of zero is a deliberately scaled-to-zero workload,
// which is absent rather than degraded — there is nothing wrong with it.
func k8sHealth(c ReplicaCounts) Health {
	if c.Desired == 0 {
		return HealthAbsent
	}
	if c.Ready >= c.Desired && c.Updated >= c.Desired && c.Available >= c.Desired {
		return HealthHealthy
	}
	return HealthDegraded
}

// k8sDeployment is the SLICE of a Deployment this observation needs.
// Deliberately hand-declared rather than pulled from k8s.io/api: forge
// does not otherwise depend on the Kubernetes Go types, and taking that
// dependency (plus apimachinery, plus its transitive tree) to read six
// integers would be a large cost for a small read. Unknown JSON fields
// are ignored by encoding/json, so a newer Deployment shape does not
// break this.
type k8sDeployment struct {
	Spec struct {
		Replicas int `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas     int `json:"readyReplicas"`
		UpdatedReplicas   int `json:"updatedReplicas"`
		AvailableReplicas int `json:"availableReplicas"`
	} `json:"status"`
}

// firstImage returns the first container's image reference, or "" when
// the template declares none.
func (d k8sDeployment) firstImage() string {
	if len(d.Spec.Template.Spec.Containers) == 0 {
		return ""
	}
	return d.Spec.Template.Spec.Containers[0].Image
}

// digestFromImageRef extracts the `sha256:…` half of a digest-pinned
// image reference, and returns "" for anything else.
//
// Returning "" for a tag-pinned image is the point. A tag is a MUTABLE
// POINTER: `repo:v1.4.0` names whatever was last pushed under that tag,
// so it cannot answer "which bytes are running" — and a caller handed a
// tag in a field called Digest would compare it against a release
// ledger's digests and conclude, wrongly, that the running image is
// unknown to the ledger. Empty says "no content-addressed identity
// available", which is true and unambiguous.
func digestFromImageRef(ref string) string {
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return ""
	}
	d := ref[at+1:]
	if !strings.HasPrefix(d, "sha256:") {
		return ""
	}
	return d
}

// isKubectlNotFound reports whether kubectl's output is its
// object-does-not-exist message. Matching on text is unpleasant, but
// kubectl exits 1 for every failure and does not distinguish them in the
// exit code, so the alternative is to report every failure — including a
// cluster forge cannot reach — as "absent", which is the single most
// dangerous thing this verb could say: absent invites a caller to CREATE.
func isKubectlNotFound(out []byte) bool {
	return strings.Contains(string(out), "NotFound") ||
		strings.Contains(string(out), "not found")
}
