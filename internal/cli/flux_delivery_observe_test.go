package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// jsonValue is v as encoding/json decodes it, the shape kubectl's objects
// are read in.
func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

// fakeFluxCluster answers fluxDeliveryObserve's kubectl reads the way kubectl
// does: one list per resource, and metadata.managedFields only when the read
// asks for them (--show-managed-fields).
type fakeFluxCluster struct {
	pointer        map[string]any
	pointerWritten time.Time
	renderer       map[string]any
	source         map[string]any
	kustomizations []map[string]any
	reads          [][]string
}

func (c *fakeFluxCluster) install(t *testing.T) {
	t.Helper()
	prev := fluxDeliveryKubectl
	t.Cleanup(func() { fluxDeliveryKubectl = prev })
	fluxDeliveryKubectl = func(_ context.Context, _ FluxDeliveryEntity, args ...string) ([]byte, error) {
		c.reads = append(c.reads, args)
		if len(args) < 2 || args[0] != "get" {
			return nil, fmt.Errorf("unexpected kubectl %v", args)
		}
		var items []map[string]any
		switch args[1] {
		case "resourcesetinputproviders.fluxcd.controlplane.io":
			if c.pointer != nil {
				p := jsonValue(c.pointer).(map[string]any)
				md := p["metadata"].(map[string]any)
				delete(md, "managedFields")
				if slices.Contains(args, "--show-managed-fields") {
					md["managedFields"] = []any{map[string]any{
						"manager": fluxDeliveryFieldManager, "operation": "Update", "time": c.pointerWritten.Format(time.RFC3339),
					}}
				}
				items = append(items, p)
			}
		case "resourcesets.fluxcd.controlplane.io":
			items = append(items, c.renderer)
		case "ocirepositories.source.toolkit.fluxcd.io":
			items = append(items, c.source)
		case "kustomizations.kustomize.toolkit.fluxcd.io":
			items = c.kustomizations
		default:
			return nil, fmt.Errorf("unexpected resource %s", args[1])
		}
		return json.Marshal(map[string]any{"items": items})
	}
}

// fluxCondition is one status condition.
func fluxCondition(typ, status, reason, message string, at time.Time) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "message": message, "lastTransitionTime": at.Format(time.RFC3339)}
}

// fluxApplied is obj (as the renderer rendered it) at generation 1, with a
// status of that generation: Ready (or not) on digest. Each condition is dated
// with the generation; a ResourceSet, like flux-operator's, reports no
// status.observedGeneration beside them.
func fluxApplied(obj map[string]any, digest string, conds ...map[string]any) map[string]any {
	o := jsonValue(obj).(map[string]any)
	o["metadata"].(map[string]any)["generation"] = 1
	var dated []any
	for _, c := range conds {
		c = jsonValue(c).(map[string]any)
		c["observedGeneration"] = 1
		dated = append(dated, c)
	}
	status := map[string]any{"observedGeneration": 1, "conditions": dated}
	switch o["kind"] {
	case fluxdelivery.KindResourceSet:
		delete(status, "observedGeneration")
	case fluxdelivery.KindKustomization:
		status["lastAppliedRevision"] = "latest@" + digest
		status["lastAttemptedRevision"] = "latest@" + digest
	case fluxdelivery.KindOCIRepository:
		status["artifact"] = map[string]any{"revision": "latest@" + digest}
	}
	o["status"] = status
	return jsonValue(o).(map[string]any)
}

// liveFluxCluster is a cluster whose renderer has applied rendered (every
// object Ready on digest) and whose pointer projects pointed.
func liveFluxCluster(t *testing.T, fd FluxDeliveryEntity, pointed, rendered fluxdelivery.BindingInput) *fakeFluxCluster {
	t.Helper()
	pointer, err := fluxdelivery.InputProvider(pointed)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := renderFluxDeliveryInput(rendered, fd)
	if err != nil {
		t.Fatal(err)
	}
	at := pointerWrittenAt.Add(-time.Hour)
	ready := fluxCondition("Ready", "True", "ReconciliationSucceeded", "Applied", at)
	c := &fakeFluxCluster{
		pointer: pointer, pointerWritten: pointerWrittenAt,
		renderer: fluxApplied(map[string]any{"apiVersion": fluxdelivery.APIVersion, "kind": fluxdelivery.KindResourceSet,
			"metadata": map[string]any{"name": fd.ResourceSet, "namespace": fd.Namespace}}, rendered.Source.Digest, ready),
	}
	for _, o := range objs {
		switch o.GetKind() {
		case fluxdelivery.KindOCIRepository:
			c.source = fluxApplied(o.Object, rendered.Source.Digest, ready)
		case fluxdelivery.KindKustomization:
			c.kustomizations = append(c.kustomizations, fluxApplied(o.Object, rendered.Source.Digest, ready))
		}
	}
	if c.source == nil || len(c.kustomizations) != len(rendered.Appliers) {
		t.Fatalf("fixture: rendered %d objects without a source and one Kustomization per applier", len(objs))
	}
	return c
}

// TestFluxStatusOfAResourceSet: flux-operator's ResourceSet has no
// status.observedGeneration — it dates its conditions — so its Ready
// condition's generation says whether the status is current. Reading only
// status.observedGeneration left every real renderer "not current", and no
// flux wait could ever converge (found by TestE2EFluxDeliverySelfHosted).
func TestFluxStatusOfAResourceSet(t *testing.T) {
	t.Parallel()
	rs := func(gen, readyGen int) map[string]any {
		return jsonValue(map[string]any{
			"metadata": map[string]any{"name": "delivery.prod", "generation": gen},
			"status": map[string]any{"conditions": []any{map[string]any{
				"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded", "observedGeneration": readyGen,
			}}},
		}).(map[string]any)
	}
	if s := fluxStatusOf("delivery.prod", rs(3, 3)); !s.Current || s.Ready != "True" {
		t.Errorf("a ResourceSet whose Ready is of its generation: %+v, want current and Ready", s)
	}
	if s := fluxStatusOf("delivery.prod", rs(3, 2)); s.Current {
		t.Errorf("a ResourceSet whose Ready is of an older generation is current: %+v", s)
	}
	// status.observedGeneration, where a controller reports it, still decides.
	k := rs(3, 3)
	k["status"].(map[string]any)["observedGeneration"] = float64(2)
	if s := fluxStatusOf("k", k); s.Current {
		t.Errorf("a status.observedGeneration behind the generation is current: %+v", s)
	}
}

// TestFluxDeliveryObserveReadsThePointerWriteTime: the reader asks kubectl for
// the pointer's managedFields — which kubectl omits by default — so a renderer
// that stalled after the write is this promotion's failure (exit 1), not a
// wait that runs out its budget. Not parallel: it swaps the kubectl seam.
func TestFluxDeliveryObserveReadsThePointerWriteTime(t *testing.T) {
	rel := fluxRelease(true)
	in, _ := rel.input("2", "promo-2")
	c := liveFluxCluster(t, rel.fd, in, in)
	c.renderer["status"].(map[string]any)["conditions"] = []any{
		fluxCondition("Ready", "False", "BuildFailed", "template: bad input", pointerWrittenAt.Add(time.Second)),
		fluxCondition("Stalled", "True", "BuildFailed", "template: bad input", pointerWrittenAt.Add(time.Second)),
	}
	c.install(t)

	obs, err := fluxDeliveryObserve(context.Background(), rel.fd, in)
	if err != nil {
		t.Fatal(err)
	}
	var pointerRead []string
	for _, r := range c.reads {
		if r[1] == "resourcesetinputproviders.fluxcd.controlplane.io" {
			pointerRead = r
		}
	}
	if !slices.Contains(pointerRead, "--show-managed-fields") {
		t.Errorf("the pointer read %v does not ask for managedFields, so its write time is never known", pointerRead)
	}
	if !obs.PointerWrittenAt.Equal(pointerWrittenAt) {
		t.Errorf("PointerWrittenAt = %s, want %s", obs.PointerWrittenAt, pointerWrittenAt)
	}
	err = obs.failure("prod", "v2", in)
	if exitcode.Of(err) != exitWrong || err == nil || !strings.Contains(err.Error(), "renderer has not applied the promoted inputs") {
		t.Errorf("a renderer stalled after the pointer's write: %v (exit %d), want its failure, exit 1", err, exitcode.Of(err))
	}
	if _, err := waitFluxDeliveryObserved(context.Background(), "prod", "v2", rel.fd, in, time.Minute,
		fluxWaitPace{interval: time.Millisecond}, func(string, ...any) {}); exitcode.Of(err) != exitWrong {
		t.Errorf("the wait on a renderer stalled after the write: %v (exit %d), want exit 1 at once", err, exitcode.Of(err))
	}
}

// TestFluxDeliveryObserveJudgesTheWholeApplierSpec: the source can carry the
// new fence while a Kustomization is still the previous render's — a retired
// applier still suspended, or one with other health checks. Its Ready status
// (or failure) at the same digest decides nothing until the renderer updates
// it. Read end to end through the kubectl seam. Not parallel: it swaps the
// seam.
func TestFluxDeliveryObserveJudgesTheWholeApplierSpec(t *testing.T) {
	rel := fluxRelease(true)
	in, _ := rel.input("3", "promo-3")
	d := in.Source.Digest
	post := in.Appliers[2].Name
	rollout := in.Appliers[1].Name

	prevHealth, _ := rel.input("2", "promo-2")
	prevHealth.Appliers = slices.Clone(prevHealth.Appliers)
	prevHealth.Appliers[1].Health = []fluxdelivery.HealthCheck{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "acme-prod"}}

	for _, tc := range []struct {
		name    string
		prev    fluxdelivery.BindingInput
		applier string
	}{
		{"a retired applier still suspended", retireFluxDeliveryAppliers(in, []string{post}, "2"), post},
		{"an applier with the previous health checks", prevHealth, rollout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The renderer has stamped the new fence on the source and
			// updated every Kustomization but tc.applier.
			c := liveFluxCluster(t, rel.fd, in, in)
			stale := liveFluxCluster(t, rel.fd, tc.prev, tc.prev)
			for i, k := range c.kustomizations {
				if nested(k, "metadata", "name") == tc.applier {
					for _, s := range stale.kustomizations {
						if nested(s, "metadata", "name") == tc.applier {
							c.kustomizations[i] = s
						}
					}
				}
			}
			c.install(t)

			obs, err := fluxDeliveryObserve(context.Background(), rel.fd, in)
			if err != nil {
				t.Fatal(err)
			}
			if !obs.consumed(in) {
				t.Fatalf("fixture: the source does not carry the new fence: %+v", obs.Source)
			}
			if obs.converged(in) {
				t.Errorf("converged while %s is the previous render's", tc.applier)
			}

			// Its stale failure at the same digest is not this input's either.
			for _, k := range c.kustomizations {
				if nested(k, "metadata", "name") == tc.applier {
					k["status"].(map[string]any)["conditions"] = []any{
						fluxCondition("Ready", "False", "HealthCheckFailed", "Job failed", pointerWrittenAt.Add(-time.Minute))}
				}
			}
			obs, err = fluxDeliveryObserve(context.Background(), rel.fd, in)
			if err != nil {
				t.Fatal(err)
			}
			if err := obs.failure("prod", "v3", in); err != nil {
				t.Errorf("the previous render's failure decided this input: %v", err)
			}

			// Once the renderer updates it, the same status is this input's.
			fresh := liveFluxCluster(t, rel.fd, in, in)
			c.kustomizations = fresh.kustomizations
			obs, err = fluxDeliveryObserve(context.Background(), rel.fd, in)
			if err != nil {
				t.Fatal(err)
			}
			if !obs.converged(in) {
				t.Errorf("not converged once every Kustomization carries the input's spec: %+v", obs.Appliers)
			}
			if revisionDigestOf(obs.Appliers[0].LastApplied) != d {
				t.Errorf("fixture: applied %s, want %s", obs.Appliers[0].LastApplied, d)
			}
		})
	}
}
