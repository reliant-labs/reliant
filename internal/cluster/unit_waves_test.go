package cluster

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// Tests for delivery units under direct apply (unit_waves.go): a unit's
// workloads are applied only once every unit it is `after` has rolled out, a
// unit that does not roll out stops the deploy with nothing later applied and
// nothing pruned, and an env that declares no units — or whose workloads sit in
// one — is applied exactly as before.
//
// They drive the apply against the recording fake kubectl of
// prerollout_test.go: the ORDER of its invocations is the property.

// natsBrokerUnits is control-plane's declaration: the broker and its
// permissions roll before everything that connects to it.
var natsBrokerUnits = []release.DeliveryUnit{
	{Name: "nats-broker", After: []string{}},
	{Name: "base", After: []string{"nats-broker"}},
}

// brokerStream is a release whose broker StatefulSet (and its config) sit in
// nats-broker and whose consumer Deployment sits in base.
var brokerStream = strings.Join([]string{
	`apiVersion: v1
kind: ConfigMap
metadata:
  name: nats-cluster-config
  namespace: app-prod
  labels:
    app.kubernetes.io/name: nats-cluster
    forge.dev/delivery-unit: nats-broker`,
	`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: nats-cluster
  namespace: app-prod
  labels:
    app.kubernetes.io/name: nats-cluster
    forge.dev/delivery-unit: nats-broker`,
	`apiVersion: v1
kind: Service
metadata:
  name: daemon-gateway
  namespace: app-prod
  labels:
    app.kubernetes.io/name: daemon-gateway`,
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: daemon-gateway
  namespace: app-prod
  labels:
    app.kubernetes.io/name: daemon-gateway`,
}, docDelimiter)

func unitOpts(units []release.DeliveryUnit) ApplyOpts {
	o := gateOpts(RolloutWait)
	o.DeliveryUnits = units
	o.Prune = true
	return o
}

func appliesNamed(kind, name string) func(kubectlCall) bool {
	return func(c kubectlCall) bool {
		return c.IsApply() && strings.Contains(c.Stdin, "\nkind: "+kind+"\nmetadata:\n  name: "+name+"\n")
	}
}

func readsStatefulSet(name string) func(kubectlCall) bool {
	return func(c kubectlCall) bool {
		return strings.Contains(c.Args, " get statefulset") && strings.Contains(c.Args, name)
	}
}

func prunes(c kubectlCall) bool {
	return strings.Contains(c.Args, "poddisruptionbudget") || strings.Contains(c.Args, " delete ")
}

// TestApply_DeliveryUnitRollsOutBeforeTheUnitsAfterIt is the v1.14.0-rc.1
// incident: the consumer (base) must not be applied until the broker
// (nats-broker) it depends on has rolled out.
//
// Mutation that fails it: drop the `unitOrder != nil` branch in
// applyRenderedNoWait — the Deployment rides the StatefulSet's apply.
func TestApply_DeliveryUnitRollsOutBeforeTheUnitsAfterIt(t *testing.T) {
	testkit.Slow(t, "drives a fake kubectl through many shell subprocesses")
	calls := fakeGateKubectl(t, jobCompletes, "daemon-gateway")

	if err := applyRendered(context.Background(), unitOpts(natsBrokerUnits), brokerStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	got := calls()
	broker := firstIndex(got, appliesNamed("StatefulSet", "nats-cluster"))
	brokerReady := firstIndex(got, readsStatefulSet("nats-cluster"))
	consumer := firstIndex(got, appliesNamed("Deployment", "daemon-gateway"))
	if broker < 0 || brokerReady < 0 || consumer < 0 {
		t.Fatalf("broker apply %d, broker readiness %d, consumer apply %d: all must happen\ncalls:\n%s", broker, brokerReady, consumer, describeCalls(got))
	}
	if !(broker < brokerReady && brokerReady < consumer) {
		t.Errorf("want broker applied → broker ready → consumer applied, got %d → %d → %d\ncalls:\n%s", broker, brokerReady, consumer, describeCalls(got))
	}
	if strings.Contains(got[consumer].Stdin, "\nkind: StatefulSet\n") || strings.Contains(got[broker].Stdin, "\nkind: Deployment\n") {
		t.Errorf("the two units shared an apply\ncalls:\n%s", describeCalls(got))
	}
	// Everything that is not a workload went ahead of every unit: the broker's
	// config (the permissions) and the consumer's Service.
	for _, name := range []string{"nats-cluster-config", "daemon-gateway"} {
		kind := map[string]string{"nats-cluster-config": "ConfigMap", "daemon-gateway": "Service"}[name]
		if at := firstIndex(got, appliesNamed(kind, name)); at < 0 || at > broker {
			t.Errorf("%s/%s must be applied before the first unit (at %d, broker at %d)", kind, name, at, broker)
		}
	}
	// The apply-time prunes wait for every unit.
	if at := firstIndex(got, prunes); at < 0 || at < consumer {
		t.Errorf("the prune must run after the last unit was applied (prune at %d, consumer at %d)\ncalls:\n%s", at, consumer, describeCalls(got))
	}
}

// TestApply_DeliveryUnitThatFailsHoldsTheRest: a unit that does not roll out
// stops the deploy — the units after it are never applied and nothing is
// pruned — and says so with a *UnitRolloutError.
//
// Mutation that fails it: in convergeUnitWaves, keep going after a failed wave
// (or run the deferred prune before the waves).
func TestApply_DeliveryUnitThatFailsHoldsTheRest(t *testing.T) {
	testkit.Slow(t, "drives a fake kubectl through many shell subprocesses")
	calls := fakeGateKubectl(t, jobCompletes, "broker", "daemon-gateway")
	t.Setenv("FAKE_ROLLOUT_FAIL", "broker")
	stream := strings.Replace(brokerStream, "kind: StatefulSet\nmetadata:\n  name: nats-cluster", "kind: Deployment\nmetadata:\n  name: broker", 1)

	err := applyRendered(context.Background(), unitOpts(natsBrokerUnits), stream)
	var held *UnitRolloutError
	if !errors.As(err, &held) {
		t.Fatalf("want *UnitRolloutError, got %T: %v", err, err)
	}
	if !reflect.DeepEqual(held.Units, []string{"nats-broker"}) || !reflect.DeepEqual(held.Held, []string{"base"}) {
		t.Errorf("units %v held %v, want [nats-broker] holding [base]", held.Units, held.Held)
	}
	for _, want := range []string{"deployment/broker", "NOT applied (base)", "nothing was pruned"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must contain %q, got:\n%v", want, err)
		}
	}
	got := calls()
	if at := firstIndex(got, appliesNamed("Deployment", "daemon-gateway")); at >= 0 {
		t.Errorf("the base unit was applied (call %d) although nats-broker did not roll out\ncalls:\n%s", at, describeCalls(got))
	}
	if at := firstIndex(got, prunes); at >= 0 {
		t.Errorf("something was pruned (call %d) although the deploy stopped between units\ncalls:\n%s", at, describeCalls(got))
	}
}

// TestApply_NoDeliveryUnitsIsUnchanged: an env that declares no units, and one
// whose workloads all sit in one unit, send exactly the calls they always did.
//
// Mutation that fails it: stage whenever units are declared (drop the
// "carrying < 2" rule in planUnitWaves).
func TestApply_NoDeliveryUnitsIsUnchanged(t *testing.T) {
	testkit.Slow(t, "drives a fake kubectl through many shell subprocesses")
	oneUnit := strings.ReplaceAll(brokerStream, "forge.dev/delivery-unit: nats-broker", "forge.dev/delivery-unit: base")
	run := func(units []release.DeliveryUnit, stream string) []string {
		calls := fakeGateKubectl(t, jobCompletes, "daemon-gateway")
		if err := applyRendered(context.Background(), unitOpts(units), stream); err != nil {
			t.Fatalf("applyRendered: %v", err)
		}
		var out []string
		for _, c := range calls() {
			out = append(out, c.Args+"\n"+c.Stdin)
		}
		return out
	}
	baseline := run(nil, brokerStream)
	if got := applies(func() []kubectlCall {
		var cs []kubectlCall
		for _, s := range baseline {
			args, stdin, _ := strings.Cut(s, "\n")
			cs = append(cs, kubectlCall{Args: args, Stdin: stdin})
		}
		return cs
	}()); len(got) != 2 {
		t.Fatalf("no units: want the config pass plus ONE apply, got %d applies", len(got))
	}
	if single := run(natsBrokerUnits, oneUnit); !reflect.DeepEqual(single, run(nil, oneUnit)) {
		t.Errorf("declared units with every workload in one unit must apply exactly as with none")
	}
}

// TestWaitRollouts_DeliveryUnitsOrderAcrossClusters: `after` holds across
// clusters — the consumer on one cluster waits for the broker on another, even
// though its own cluster's stream carries only one unit.
//
// Mutation that fails it: make planUnitWaves read the per-cluster scoped stream
// (the consumer's cluster then stages nothing and applies at once).
func TestWaitRollouts_DeliveryUnitsOrderAcrossClusters(t *testing.T) {
	testkit.Slow(t, "drives a fake kubectl through many shell subprocesses")
	calls := fakeGateKubectl(t, jobCompletes, "daemon-gateway")
	scoped := func(kctx, own, other string) ApplyOpts {
		o := unitOpts(natsBrokerUnits)
		o.Context = kctx
		o.ClusterScope = &GroupScope{Cluster: kctx, OwnApps: map[string]struct{}{own: {}}, OtherApps: map[string]struct{}{other: {}}}
		return o
	}
	ctx := context.Background()
	hub, err := applyRenderedNoWait(ctx, scoped("k3d-hub", "daemon-gateway", "nats-cluster"), brokerStream)
	if err != nil {
		t.Fatalf("hub apply: %v", err)
	}
	broker, err := applyRenderedNoWait(ctx, scoped("k3d-broker", "nats-cluster", "daemon-gateway"), brokerStream)
	if err != nil {
		t.Fatalf("broker apply: %v", err)
	}
	if at := firstIndex(calls(), appliesNamed("Deployment", "daemon-gateway")); at >= 0 {
		t.Fatalf("the consumer was applied (call %d) before any wave ran\ncalls:\n%s", at, describeCalls(calls()))
	}
	if err := WaitRollouts(ctx, hub, broker); err != nil {
		t.Fatalf("WaitRollouts: %v", err)
	}
	got := calls()
	ready := firstIndex(got, func(c kubectlCall) bool {
		return readsStatefulSet("nats-cluster")(c) && strings.Contains(c.Args, "--context k3d-broker")
	})
	consumer := firstIndex(got, func(c kubectlCall) bool {
		return appliesNamed("Deployment", "daemon-gateway")(c) && strings.Contains(c.Args, "--context k3d-hub")
	})
	if ready < 0 || consumer < 0 || consumer < ready {
		t.Errorf("want the broker ready on k3d-broker (call %d) before the consumer is applied on k3d-hub (call %d)\ncalls:\n%s", ready, consumer, describeCalls(got))
	}
}

// TestApply_DeliveryUnitThatDoesNotFullyApplyHoldsTheRestUnderWarn: warn mode
// tolerates an incomplete apply of the release, never of a unit wave — an
// earlier unit that did not land is not one that rolled out.
//
// Mutation that fails it: run the wave apply through classifyApplyResult.
func TestApply_DeliveryUnitThatDoesNotFullyApplyHoldsTheRestUnderWarn(t *testing.T) {
	testkit.Slow(t, "drives a fake kubectl through many shell subprocesses")
	calls := fakeGateKubectl(t, jobCompletes, "daemon-gateway")
	t.Setenv("FAKE_APPLY_OMIT", "nats-cluster")
	opts := unitOpts(natsBrokerUnits)
	opts.Rollout.Mode = RolloutWarn

	err := applyRendered(context.Background(), opts, brokerStream)
	if err == nil || !strings.Contains(err.Error(), "delivery unit(s) nats-broker") {
		t.Fatalf("an incomplete wave apply must stop the deploy naming the unit, got %v", err)
	}
	got := calls()
	if at := firstIndex(got, appliesNamed("Deployment", "daemon-gateway")); at >= 0 {
		t.Errorf("the base unit was applied (call %d) although nats-broker did not fully apply\ncalls:\n%s", at, describeCalls(got))
	}
	if at := firstIndex(got, prunes); at >= 0 {
		t.Errorf("something was pruned (call %d) although the deploy stopped between units", at)
	}
}

func TestPlanUnitWaves(t *testing.T) {
	if waves, err := planUnitWaves(nil, brokerStream); err != nil || waves != nil {
		t.Errorf("no declared units: want nil, got %v %v", waves, err)
	}
	waves, err := planUnitWaves(natsBrokerUnits, brokerStream)
	if err != nil || !reflect.DeepEqual(waves, [][]string{{"nats-broker"}, {"base"}}) {
		t.Errorf("want [[nats-broker] [base]], got %v %v", waves, err)
	}
	cyclic := []release.DeliveryUnit{{Name: "nats-broker", After: []string{"base"}}, {Name: "base", After: []string{"nats-broker"}}}
	if _, err := planUnitWaves(cyclic, brokerStream); err == nil || !strings.Contains(err.Error(), "nothing was applied") {
		t.Errorf("a cycle must be refused before anything is applied, got %v", err)
	}
}

func TestSplitUnitStages_EveryDocumentLandsOnce(t *testing.T) {
	stream := brokerStream + docDelimiter + `apiVersion: batch/v1
kind: Job
metadata:
  name: smoke
  annotations:
    forge.dev/deploy-phase: post-rollout`
	head, st := splitUnitStages(stream, [][]string{{"nats-broker"}, {"base"}}, "app-prod")
	count := func(s string) int { return len(splitDocs(s)) }
	total := count(head) + count(st.final)
	for _, w := range st.waves {
		total += count(w.stream)
	}
	if total != count(stream) {
		t.Errorf("split holds %d documents, the stream had %d", total, count(stream))
	}
	if !strings.Contains(st.waves[0].stream, "kind: StatefulSet") || !strings.Contains(st.waves[1].stream, "kind: Deployment") {
		t.Errorf("each workload must ride its unit's wave: %+v", st.waves)
	}
	if !strings.Contains(st.final, "kind: Job") || !strings.Contains(head, "kind: Service") {
		t.Errorf("the Job goes last and the Service goes now: head=%q final=%q", head, st.final)
	}
	want := []workloadRef{{Kind: "StatefulSet", Namespace: "app-prod", Name: "nats-cluster"}}
	if !reflect.DeepEqual(st.waves[0].workloads, want) {
		t.Errorf("wave 0 awaits %v, want %v", st.waves[0].workloads, want)
	}
}
