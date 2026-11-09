package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Tests for the secret ordering (secret_order.go, secret_pins.go):
//
//	CRDs -> ESO -> OpenBao -> stores -> ExternalSecrets -> workloads
//
// The ordering is a statement about the SEQUENCE of kubectl calls, which no
// return value shows, so the apply tests drive applyRendered against the fake
// kubectl that records every call in order (fakeGateKubectl) and assert on that
// sequence. The ExternalSecret wait is driven against the same fake, which
// answers `get externalsecret` from FAKE_ES_JSON.

// esJSON is an ExternalSecret as the API server returns it.
func esJSON(generation int, syncedGeneration int, ready string, reason string) string {
	synced := ""
	if syncedGeneration > 0 {
		synced = fmt.Sprintf("%d-abc123", syncedGeneration)
	}
	return fmt.Sprintf(`{"metadata":{"generation":%d},"status":{"syncedResourceVersion":%q,"conditions":[{"type":"Ready","status":%q,"reason":%q,"message":""}]}}`,
		generation, synced, ready, reason)
}

func TestExternalSecretReady(t *testing.T) {
	cases := []struct {
		name string
		json string
		want bool
		why  string
	}{
		{"synced this spec", esJSON(3, 3, "True", "SecretSynced"), true, ""},
		{"ready for an OLDER spec: a pin bump must not pass on the previous sync", esJSON(3, 2, "True", "SecretSynced"), false, "older spec"},
		{"never synced", esJSON(1, 0, "True", "SecretSynced"), false, "older spec"},
		{"store refused", esJSON(1, 0, "False", "SecretSyncedError"), false, "SecretSyncedError"},
		{"no condition yet", `{"metadata":{"generation":1},"status":{}}`, false, "no Ready condition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v externalSecretView
			if err := jsonUnmarshal(tc.json, &v); err != nil {
				t.Fatal(err)
			}
			got, why := v.ready()
			if got != tc.want || !strings.Contains(why, tc.why) {
				t.Fatalf("ready() = %v, %q; want %v containing %q", got, why, tc.want, tc.why)
			}
		})
	}
}

// storeStream is a release that has a store component, an env SecretStore and
// ExternalSecret, a migrate Job and a Deployment that reads the synced Secret.
func storeStream() string {
	return strings.Join([]string{
		`apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: app-prod`,
		// The store component's own bootstrap delivery.
		`apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: openbao-unseal
  namespace: openbao
  labels:
    forge.dev/component: openbao`,
		// The component's widen (pre-rollout) Job.
		`apiVersion: batch/v1
kind: Job
metadata:
  name: openbao-widen-aaa
  namespace: openbao
  labels:
    forge.dev/component: openbao
  annotations:
    forge.dev/deploy-phase: pre-rollout`,
		`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: openbao
  namespace: openbao
  labels:
    forge.dev/component: openbao`,
		// The component's converge (post-rollout) Job.
		`apiVersion: batch/v1
kind: Job
metadata:
  name: openbao-converge-bbb
  namespace: openbao
  labels:
    forge.dev/component: openbao
  annotations:
    forge.dev/deploy-phase: post-rollout`,
		`apiVersion: v1
kind: ServiceAccount
metadata:
  name: reliant-secrets
  namespace: app-prod
  labels:
    forge.dev/secret-stage: secrets`,
		`apiVersion: external-secrets.io/v1
kind: SecretStore
metadata:
  name: reliant-openbao
  namespace: app-prod
  labels:
    forge.dev/secret-stage: secrets`,
		`apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: db-role
  namespace: app-prod
  labels:
    forge.dev/secret-stage: secrets`,
		`apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: renewed-token
  namespace: app-prod
  labels:
    forge.dev/secret-stage: secrets
  annotations:
    forge.dev/secret-sync: deferred`,
		migrateJob,
		`apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: app-prod
  labels:
    app.kubernetes.io/name: admin-server`,
	}, docDelimiter)
}

func TestPartitionSecretStages(t *testing.T) {
	st, err := partitionSecretStages(storeStream())
	if err != nil {
		t.Fatal(err)
	}
	if !st.active() {
		t.Fatal("a stream with a store-backed ExternalSecret must have stages")
	}
	docs := func(s string) string { return strings.Join(kindsOf(s), ",") }
	for name, tc := range map[string]struct{ got, want string }{
		"bootstrap": {docs(st.bootstrap), "ExternalSecret"},
		"storePre":  {docs(st.storePre), "Job"},
		"store":     {docs(st.store), "StatefulSet,Job"},
		"secrets":   {docs(st.secrets), "ServiceAccount,SecretStore,ExternalSecret,ExternalSecret"},
		"rest":      {docs(st.rest), "ConfigMap,Job,Deployment"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s stage holds [%s], want [%s]", name, tc.got, tc.want)
		}
	}
	if got := refNames(st.secretSyncs); got != "db-role" {
		t.Errorf("awaited ExternalSecrets = %q: a `deferred` one is written after the deploy and must not be waited for", got)
	}
	if got := refNames(st.bootstrapSync); got != "openbao-unseal" {
		t.Errorf("awaited bootstrap ExternalSecrets = %q", got)
	}
	count := func(s string) int { return len(splitDocs(s)) }
	if got, want := count(st.bootstrap)+count(st.storePre)+count(st.store)+count(st.secrets)+count(st.rest), count(storeStream()); got != want {
		t.Errorf("the stages hold %d documents, the stream had %d: a dropped document is an object never applied", got, want)
	}
}

// TestPartitionSecretStages_NoStoreIsInert pins that a stream with no
// store-backed secret keeps its historical passes: a stage that fired for it
// would reorder every project's deploy.
func TestPartitionSecretStages_NoStoreIsInert(t *testing.T) {
	st, err := partitionSecretStages(gateStream)
	if err != nil {
		t.Fatal(err)
	}
	if st.active() {
		t.Fatal("a stream with no store-backed secret must not activate the ordering")
	}
	if got, want := len(splitDocs(st.rest)), len(splitDocs(gateStream)); got != want {
		t.Fatalf("an inert partition must return every document in rest: %d of %d", got, want)
	}
}

// TestPartitionSecretStages_UnknownPhaseOnStoreJobIsRefused: the store
// component's Jobs are phase-classified by the same rule as any Job.
func TestPartitionSecretStages_UnknownPhaseOnStoreJobIsRefused(t *testing.T) {
	stream := `apiVersion: batch/v1
kind: Job
metadata:
  name: openbao-widen-aaa
  labels:
    forge.dev/component: openbao
  annotations:
    forge.dev/deploy-phase: pre-rolout`
	if _, err := partitionSecretStages(stream); err == nil || !strings.Contains(err.Error(), "pre-rolout") {
		t.Fatalf("an unknown phase on a store Job must be refused naming the value, got %v", err)
	}
}

// fakeES makes the fake kubectl answer `get externalsecret` with doc.
func fakeES(t *testing.T, doc string) { t.Setenv("FAKE_ES_JSON", doc) }

// fakeESFor answers doc for the named ExternalSecret and a synced one for every
// other, so a test can fail exactly the one it means to.
func fakeESFor(t *testing.T, name, doc string) {
	t.Setenv("FAKE_ES_JSON", esJSON(1, 1, "True", "SecretSynced"))
	t.Setenv("FAKE_ES_NAME", name)
	t.Setenv("FAKE_ES_NAME_JSON", doc)
}

func fastSecretPoll(t *testing.T) {
	t.Helper()
	prev := secretSyncPoll
	secretSyncPoll = 20 * time.Millisecond
	t.Cleanup(func() { secretSyncPoll = prev })
}

// callIndex finds the first call matching, for ordering assertions.
func callIndex(calls []kubectlCall, pred func(kubectlCall) bool) int { return firstIndex(calls, pred) }

func getsES(c kubectlCall) bool { return strings.Contains(c.Args, "get externalsecret") }

func rolloutStatus(what string) func(kubectlCall) bool {
	return func(c kubectlCall) bool {
		return strings.Contains(c.Args, "rollout status") && strings.Contains(c.Args, what)
	}
}

// TestApply_WorkloadsWaitForTheExternalSecretsToSync is the ordering the
// feature exists for: the store is applied and ready, the SecretStore and
// ExternalSecret are applied, the ExternalSecret is awaited, and ONLY THEN is
// the migrate Job or a Deployment sent.
//
// Mutation that fails it: drop applySecretStages from applyRenderedNoWait (the
// Deployment rides the first apply) or reorder it after the pre-rollout gate.
func TestApply_WorkloadsWaitForTheExternalSecretsToSync(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fastSecretPoll(t)
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")
	fakeES(t, esJSON(1, 1, "True", "SecretSynced"))

	if err := applyRendered(context.Background(), gateOpts(RolloutWait), storeStream()); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	got := calls()
	at := func(what string, pred func(kubectlCall) bool) int {
		i := callIndex(got, pred)
		if i < 0 {
			t.Fatalf("%s never happened\ncalls:\n%s", what, describeCalls(got))
		}
		return i
	}
	bootstrapES := at("the bootstrap ExternalSecret apply", func(c kubectlCall) bool {
		return c.IsApply() && strings.Contains(c.Stdin, "name: openbao-unseal")
	})
	bootstrapWait := at("the wait for the bootstrap ExternalSecret", getsES)
	widen := at("the widen Job apply", func(c kubectlCall) bool { return c.IsApply() && strings.Contains(c.Stdin, "name: openbao-widen-aaa") })
	storeApply := at("the store apply", func(c kubectlCall) bool { return c.IsApply() && strings.Contains(c.Stdin, "kind: StatefulSet") })
	storeReady := at("the store's rollout wait", rolloutStatus("statefulset/openbao"))
	converge := at("the converge Job wait", waitsForJobComplete("openbao-converge-bbb"))
	secretsApply := at("the SecretStore apply", func(c kubectlCall) bool { return c.IsApply() && strings.Contains(c.Stdin, "kind: SecretStore") })
	esWaits := 0
	lastESWait := -1
	for i, c := range got {
		if getsES(c) {
			esWaits++
			lastESWait = i
		}
	}
	migrate := at("the migrate Job apply", func(c kubectlCall) bool {
		return c.IsApply() && strings.Contains(c.Stdin, "name: app-migrate-abc123")
	})
	deployment := at("the Deployment apply", appliesKind("Deployment"))

	order := []struct {
		name string
		idx  int
	}{
		{"bootstrap ExternalSecret applied", bootstrapES},
		{"bootstrap ExternalSecret awaited", bootstrapWait},
		{"widen Job applied", widen},
		{"store applied", storeApply},
		{"store ready", storeReady},
		{"converge Job complete", converge},
		{"SecretStore + ExternalSecret applied", secretsApply},
		{"ExternalSecret awaited", lastESWait},
	}
	for i := 1; i < len(order); i++ {
		if order[i].idx < order[i-1].idx {
			t.Errorf("%q (call %d) came before %q (call %d)\ncalls:\n%s", order[i].name, order[i].idx, order[i-1].name, order[i-1].idx, describeCalls(got))
		}
	}
	// The widen Job must be awaited before the store's workloads roll: it
	// grants a name the new release adds, before anything reads it.
	if w := callIndex(got, waitsForJobComplete("openbao-widen-aaa")); w < 0 || w > storeApply {
		t.Errorf("the widen Job must complete (call %d) before the store is applied (call %d)\ncalls:\n%s", w, storeApply, describeCalls(got))
	}
	if lastESWait > migrate || lastESWait > deployment {
		t.Errorf("a workload or the migrate Job was sent (calls %d, %d) before the ExternalSecret was proven synced (call %d)\ncalls:\n%s",
			migrate, deployment, lastESWait, describeCalls(got))
	}
	// The release's own pre-rollout gate is intact: the Job completes, then the Deployment.
	if jw := callIndex(got, waitsForJobComplete("app-migrate-abc123")); jw < 0 || jw > deployment {
		t.Errorf("the migrate Job must still complete (call %d) before the Deployment (call %d)", jw, deployment)
	}
	// A `deferred` ExternalSecret is applied but never awaited.
	for _, c := range got {
		if getsES(c) && strings.Contains(c.Args, "renewed-token") {
			t.Errorf("a deferred ExternalSecret must not be awaited: %s", c.Args)
		}
	}
	// No Job is awaited twice: the store's Jobs were awaited by their stages.
	for _, job := range []string{"openbao-widen-aaa", "openbao-converge-bbb"} {
		n := 0
		for _, c := range got {
			if waitsForJobComplete(job)(c) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("Job %s was awaited %d times, want exactly 1", job, n)
		}
	}
	if esWaits < 2 {
		t.Errorf("expected a wait for the bootstrap and the env ExternalSecrets, saw %d reads", esWaits)
	}
}

// TestApply_UnsyncedExternalSecretAppliesNoWorkload: an ExternalSecret that
// never syncs must stop the deploy BEFORE any workload or migration, name the
// ExternalSecret and the commands that show why, and say what was withheld.
//
// Mutation that fails it: ignore waitExternalSecrets' error.
func TestApply_UnsyncedExternalSecretAppliesNoWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fastSecretPoll(t)
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"store refused the login":        {esJSON(1, 0, "False", "SecretSyncedError"), "SecretSyncedError"},
		"stale: ready for an older spec": {esJSON(2, 1, "True", "SecretSynced"), "older spec"},
	} {
		t.Run(name, func(t *testing.T) {
			calls := fakeGateKubectl(t, jobCompletes, "admin-server")
			fakeESFor(t, "db-role", tc.doc)
			opts := gateOpts(RolloutWait)
			opts.Rollout.Timeout = 150 * time.Millisecond

			err := applyRendered(context.Background(), opts, storeStream())
			var gate *ExternalSecretSyncError
			if !errors.As(err, &gate) {
				t.Fatalf("an ExternalSecret that did not sync must stop the deploy with *ExternalSecretSyncError, got %T: %v", err, err)
			}
			if !errors.Is(err, ErrExternalSecretNotSynced) {
				t.Errorf("the error must wrap ErrExternalSecretNotSynced")
			}
			msg := err.Error()
			for _, want := range []string{"app-prod/db-role", tc.want, "kubectl --context " + gateContext + " -n app-prod describe externalsecret db-role", "NO workload was applied", "previous release is still serving"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the error must contain %q, got:\n%s", want, msg)
				}
			}
			got := calls()
			for _, kind := range append([]string{"Job"}, gatedKinds...) {
				for i, c := range got {
					if !c.IsApply() || !strings.Contains(c.Stdin, "\nkind: "+kind+"\n") {
						continue
					}
					if kind == "Job" && strings.Contains(c.Stdin, "openbao-") {
						continue // the store's own Jobs legitimately precede the env ExternalSecrets
					}
					if kind == "StatefulSet" && strings.Contains(c.Stdin, "name: openbao\n") {
						continue
					}
					t.Errorf("%s was applied (call %d) although an ExternalSecret did not sync\ncalls:\n%s", kind, i, describeCalls(got))
				}
			}
		})
	}
}

// TestApply_UnsyncedBootstrapSecretNeverStartsTheStore: the seal key reaches the
// store through an ExternalSecret, so a store started ahead of it cannot unseal.
// The bootstrap ExternalSecret is awaited before the store's workloads or Jobs
// are sent, and nothing downstream of it is.
//
// Mutation that fails it: skip waitExternalSecrets(st.bootstrapSync).
func TestApply_UnsyncedBootstrapSecretNeverStartsTheStore(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fastSecretPoll(t)
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")
	fakeESFor(t, "openbao-unseal", esJSON(1, 0, "False", "SecretSyncedError"))
	opts := gateOpts(RolloutWait)
	opts.Rollout.Timeout = 150 * time.Millisecond

	err := applyRendered(context.Background(), opts, storeStream())
	var gate *ExternalSecretSyncError
	if !errors.As(err, &gate) || !strings.Contains(err.Error(), "openbao/openbao-unseal") {
		t.Fatalf("want *ExternalSecretSyncError naming openbao/openbao-unseal, got %T: %v", err, err)
	}
	got := calls()
	for _, kind := range []string{"Job", "StatefulSet", "SecretStore", "Deployment"} {
		if at := callIndex(got, appliesKind(kind)); at >= 0 {
			t.Errorf("%s was applied (call %d) although the seal key never arrived\ncalls:\n%s", kind, at, describeCalls(got))
		}
	}
}

// TestApply_SecretOrderingHoldsUnderRolloutSkip: `--rollout=skip` is about not
// WAITING for convergence. It never meant starting a workload ahead of its
// Secret, so the gate holds in every mode (like the pre-rollout gate).
//
// Mutation that fails it: enforce the gate only in RolloutWait mode.
func TestApply_SecretOrderingHoldsUnderRolloutSkip(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fastSecretPoll(t)
	for _, mode := range []RolloutMode{RolloutSkip, RolloutWarn} {
		t.Run(string(mode), func(t *testing.T) {
			calls := fakeGateKubectl(t, jobCompletes, "admin-server")
			fakeES(t, esJSON(1, 0, "False", "SecretSyncedError"))
			opts := gateOpts(mode)
			opts.Rollout.Timeout = 100 * time.Millisecond
			if err := applyRendered(context.Background(), opts, storeStream()); err == nil {
				t.Fatalf("an unsynced ExternalSecret must fail the deploy under --rollout=%s", mode)
			}
			if at := callIndex(calls(), appliesKind("Deployment")); at >= 0 {
				t.Errorf("the Deployment was applied (call %d) under --rollout=%s although its Secret never synced", at, mode)
			}
		})
	}
}

// TestApply_FailedStoreJobStopsBeforeAnyExternalSecret: if the store's converge
// fails, nothing downstream of it is applied, and the error says it is the
// secret store, not a pre-rollout migration.
func TestApply_FailedStoreJobStopsBeforeAnyExternalSecret(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fastSecretPoll(t)
	calls := fakeGateKubectl(t, jobFails, "admin-server")
	fakeES(t, esJSON(1, 1, "True", "SecretSynced"))

	err := applyRendered(context.Background(), gateOpts(RolloutWait), storeStream())
	var gate *PreRolloutJobError
	if !errors.As(err, &gate) || gate.Kind != "secret store" {
		t.Fatalf("a failed store Job must surface as a secret store *PreRolloutJobError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), `secret store Job "openbao-widen-aaa"`) {
		t.Errorf("the message must name the store Job and its role, got:\n%s", err)
	}
	got := calls()
	for _, kind := range []string{"SecretStore", "Deployment"} {
		if at := callIndex(got, appliesKind(kind)); at >= 0 {
			t.Errorf("%s was applied (call %d) after the secret store's Job failed", kind, at)
		}
	}
}

func kindsOf(stream string) []string {
	var out []string
	for _, d := range splitDocs(stream) {
		if m, ok := parseDoc(d); ok {
			out = append(out, m.Kind)
		}
	}
	return out
}

func refNames(refs []stageRef) string {
	var n []string
	for _, r := range refs {
		n = append(n, r.Name)
	}
	return strings.Join(n, ",")
}

func TestSecretStageClassification(t *testing.T) {
	const eso = "external-secrets.io/v1"
	openbao := map[string]string{release.LabelComponent: release.ComponentOpenBao}
	for _, tc := range []struct {
		name        string
		apiVersion  string
		kind        string
		labels      map[string]string
		annotations map[string]string
		want        release.SecretStage
	}{
		{"an env ExternalSecret", eso, "ExternalSecret", nil, nil, release.SecretStageSecrets},
		{"an env SecretStore", eso, "SecretStore", nil, nil, release.SecretStageSecrets},
		{"the login identity, by label", "v1", "ServiceAccount", map[string]string{release.LabelSecretStage: "secrets"}, nil, release.SecretStageSecrets},
		{"the store's own ExternalSecret", eso, "ExternalSecret", openbao, nil, release.SecretStageBootstrap},
		{"the store's ConfigMap is bootstrap support", "v1", "ConfigMap", openbao, nil, release.SecretStageBootstrap},
		{"the store's widen Job", "batch/v1", "Job", openbao, map[string]string{release.AnnotationDeployPhase: "pre-rollout"}, release.SecretStageStorePre},
		{"the store's converge Job", "batch/v1", "Job", openbao, map[string]string{release.AnnotationDeployPhase: "post-rollout"}, release.SecretStageStore},
		{"the store's StatefulSet", "apps/v1", "StatefulSet", openbao, nil, release.SecretStageStore},
		{"an app Deployment is untouched", "apps/v1", "Deployment", nil, nil, release.SecretStageNone},
		{"an app Job is untouched", "batch/v1", "Job", nil, nil, release.SecretStageNone},
		{"an app ConfigMap is untouched", "v1", "ConfigMap", nil, nil, release.SecretStageNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := release.ClassifySecretStage(tc.apiVersion, tc.kind, "x", tc.labels, tc.annotations)
			if err != nil || got != tc.want {
				t.Fatalf("stage = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
