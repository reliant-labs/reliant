package cluster

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// unitStream is a two-unit render the way a split env produces one: reliant
// is the unit the hub converges, infra the one forge still applies, and a few
// objects carry no unit (the default, `base`).
var unitStream = strings.Join([]string{
	`apiVersion: v1
kind: Namespace
metadata:
  name: app-prod`,
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: reliant-api
  namespace: app-prod
  labels:
    app.kubernetes.io/name: reliant-api
    app.kubernetes.io/managed-by: forge
    forge.dev/delivery-unit: reliant`,
	`apiVersion: v1
kind: Service
metadata:
  name: reliant-api
  namespace: app-prod
  labels:
    forge.dev/delivery-unit: reliant`,
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: nats
  namespace: app-prod
  labels:
    app.kubernetes.io/name: nats
    app.kubernetes.io/managed-by: forge
    forge.dev/delivery-unit: infra`,
	`apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: app-prod`,
}, docDelimiter)

func kindNames(stream string) []string {
	var out []string
	for _, doc := range splitDocs(stream) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		out = append(out, m.Kind+"/"+m.Metadata.Name)
	}
	return out
}

func TestDropDeliveryUnits(t *testing.T) {
	cases := []struct {
		name  string
		units []string
		want  []string
	}{
		{"none skipped", nil, []string{"Namespace/app-prod", "Deployment/reliant-api", "Service/reliant-api", "Deployment/nats", "ConfigMap/shared"}},
		{"one unit", []string{"reliant"}, []string{"Namespace/app-prod", "Deployment/nats", "ConfigMap/shared"}},
		{"the default unit", []string{"base"}, []string{"Deployment/reliant-api", "Service/reliant-api", "Deployment/nats"}},
		{"every unit", []string{"reliant", "infra", "base"}, nil},
		{"a unit nothing carries", []string{"nope"}, []string{"Namespace/app-prod", "Deployment/reliant-api", "Service/reliant-api", "Deployment/nats", "ConfigMap/shared"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kindNames(DropDeliveryUnits(unitStream, tc.units))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

// A document that does not parse can prove nothing about its unit, so it is
// kept: dropping what cannot be read would silently ship less than the render.
func TestDropDeliveryUnits_KeepsWhatItCannotRead(t *testing.T) {
	stream := "{not: [valid yaml" + docDelimiter + unitStream
	got := DropDeliveryUnits(stream, []string{"reliant"})
	if !strings.HasPrefix(got, "{not: [valid yaml") {
		t.Errorf("an unreadable document was dropped: %q", got[:40])
	}
}

func TestDeploymentsInUnits(t *testing.T) {
	if got := DeploymentsInUnits(unitStream, []string{"reliant"}); !reflect.DeepEqual(got, []string{"reliant-api"}) {
		t.Errorf("got %v", got)
	}
	if got := DeploymentsInUnits(unitStream, nil); got != nil {
		t.Errorf("no units must name no Deployment, got %v", got)
	}
}

// THE LOAD-BEARING ONE. forge applies the unit it keeps and sends NOTHING of the
// unit the hub converges: a second applier over the hub's unit is the one
// outcome the unit-by-unit move must never produce.
func TestApply_SkipsTheDeliveryUnitsTheHubConverges(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobCompletes, "nats")
	opts := gateOpts(RolloutWait)
	opts.SkipUnits = []string{"reliant"}
	if err := applyRendered(context.Background(), opts, unitStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	sent := ""
	for _, c := range applies(calls()) {
		sent += c.Stdin + "\n---\n"
	}
	if strings.Contains(sent, "reliant-api") {
		t.Errorf("a unit the hub converges was applied from here:\n%s", sent)
	}
	for _, want := range []string{"name: nats", "name: shared", "name: app-prod"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the unit forge keeps lost %q:\n%s", want, sent)
		}
	}
}

// Pruning reads the FULL render. Computed from what was sent, it would read the
// hub's Deployment as an orphan and delete it.
func TestApply_PruneNeverDeletesTheHubsDeployments(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobCompletes, "nats", "reliant-api")
	opts := gateOpts(RolloutWait)
	opts.Prune = true
	opts.SkipUnits = []string{"reliant"}
	if err := applyRendered(context.Background(), opts, unitStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	for _, c := range calls() {
		if strings.Contains(c.Args, "delete") && strings.Contains(c.Args, "reliant-api") {
			t.Fatalf("prune deleted a Deployment the hub converges: %s", c.Args)
		}
	}
}

// The rollout wait is not forge's for a unit it did not roll: a deploy that
// blocked on (or failed because of) the hub's rollout would report the hub's
// trouble as its own.
func TestApply_DoesNotAwaitTheHubsRollout(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobCompletes, "nats", "reliant-api")
	opts := gateOpts(RolloutWait)
	opts.SkipUnits = []string{"reliant"}
	if err := applyRendered(context.Background(), opts, unitStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	for _, c := range calls() {
		if strings.Contains(c.Args, "rollout status") && strings.Contains(c.Args, "reliant-api") {
			t.Errorf("forge awaited a rollout the hub owns: %s", c.Args)
		}
	}
	var awaitedNATS bool
	for _, c := range calls() {
		if strings.Contains(c.Args, "nats") && strings.Contains(c.Args, "rollout status") {
			awaitedNATS = true
		}
	}
	if !awaitedNATS {
		t.Errorf("forge did not await the unit it applied:\n%s", describeCalls(calls()))
	}
}

// Every object is the hub's: nothing is sent, and kubectl is not handed an
// empty stream (which it refuses, failing a deploy that was right to do nothing).
func TestApply_AllUnitsTheHubsSendsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobCompletes)
	opts := gateOpts(RolloutWait)
	opts.SkipUnits = []string{"reliant", "infra", "base"}
	if err := applyRendered(context.Background(), opts, unitStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	if got := applies(calls()); len(got) != 0 {
		t.Errorf("an env the hub converges whole was applied from here:\n%s", describeCalls(calls()))
	}
}

// The superseded-Job prune reads what forge SENT, not the full render: a Job
// base in a unit the hub converges is "not defined" here, so forge leaves its
// backlog to Flux instead of deleting Jobs it does not own.
func TestApply_NeverPrunesTheHubsJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	seedBacklog(cl)

	hubJob := strings.Replace(jobRender("control-plane-migrate-h7"),
		"    forge.dev/env: prod\n", "    forge.dev/env: prod\n    forge.dev/delivery-unit: reliant\n", 1)
	stream := strings.Join([]string{hubJob, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: shared\n  namespace: " + jobNS}, docDelimiter)

	opts := jobApplyOpts()
	opts.SkipUnits = []string{"reliant"}
	if err := applyRendered(context.Background(), opts, stream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	if got := cl.deletes(); len(got) != 0 {
		t.Errorf("forge pruned Jobs of a unit the hub converges: %v", got)
	}

	// Control: the same render with nothing skipped does prune that backlog.
	cl2 := newFakeCluster(t)
	seedBacklog(cl2)
	if err := applyRendered(context.Background(), jobApplyOpts(), stream); err != nil {
		t.Fatalf("applyRendered (control): %v", err)
	}
	if len(cl2.deletes()) == 0 {
		t.Error("control: a forge-applied unit's Job backlog should have been pruned")
	}
}
