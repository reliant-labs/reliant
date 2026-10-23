package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The Cluster Workloads check exists because forge deployed pods and then
// held no opinion about them: `forge env status dev` was all-green for the
// hour daemon-gateway spent OOMKilled + CrashLoopBackOff, and for the ten
// hours admin-api (CreateContainerConfigError) and four litellm /
// reliant-api-server pods (~120 and ~130 restarts) crashlooped in the
// deployed namespace. Every case below is one of those, or one of the two
// ways a check like this becomes worthless: passing when it could not look,
// and reporting on pods that are not this env's.

// --- fixtures -------------------------------------------------------------

// pod builds the JSON `kubectl get pods -o json` actually emits. The tests
// decode the SAME bytes production does rather than hand-building structs,
// because the fact this check turns on — `lastState.terminated.reason` —
// exists only in that wire shape, and a test that skips the decode cannot
// prove forge reads it.
type cwPod struct {
	name     string
	app      string
	ownerRS  string        // owning ReplicaSet; "" for a bare pod
	ownerJob string        // owning Job
	phase    string        // default "Running"
	ready    bool          // the Ready condition AND the container's readiness
	restarts int           // restartCount on the single container
	waiting  string        // state.waiting.reason
	lastTerm string        // lastState.terminated.reason (this is where OOMKilled lives)
	exitCode int           // that termination's exit code
	age      time.Duration // default 1h; drives the startup grace
	envTag   string        // forge.dev/env ownership stamp
	initBad  string        // an init container stuck on this waiting reason
}

func (p cwPod) json() string {
	phase := p.phase
	if phase == "" {
		phase = "Running"
	}
	age := p.age
	if age == 0 {
		age = time.Hour
	}
	labels := map[string]string{}
	if p.app != "" {
		labels["app.kubernetes.io/name"] = p.app
	}
	if p.envTag != "" {
		labels["forge.dev/env"] = p.envTag
	}
	labelJSON, _ := json.Marshal(labels)

	owners := "[]"
	switch {
	case p.ownerRS != "":
		owners = `[{"kind":"ReplicaSet","name":"` + p.ownerRS + `"}]`
	case p.ownerJob != "":
		owners = `[{"kind":"Job","name":"` + p.ownerJob + `"}]`
	}

	state := `{"running":{"startedAt":"2026-08-24T00:00:00Z"}}`
	if p.waiting != "" {
		state = `{"waiting":{"reason":"` + p.waiting + `","message":"back-off restarting failed container"}}`
	}
	last := "{}"
	if p.lastTerm != "" {
		last = fmt.Sprintf(`{"terminated":{"reason":%q,"exitCode":%d}}`, p.lastTerm, p.exitCode)
	}
	readyCond := "False"
	if p.ready {
		readyCond = "True"
	}
	initStatuses := ""
	if p.initBad != "" {
		initStatuses = fmt.Sprintf(`"initContainerStatuses":[{"name":"migrate","ready":false,"restartCount":0,`+
			`"state":{"waiting":{"reason":%q}},"lastState":{}}],`, p.initBad)
	}

	return fmt.Sprintf(`{
	  "metadata":{"name":%q,"labels":%s,"creationTimestamp":%q,"ownerReferences":%s},
	  "status":{"phase":%q,
	    "conditions":[{"type":"PodScheduled","status":"True"},{"type":"Ready","status":%q}],
	    %s"containerStatuses":[{"name":"app","ready":%t,"restartCount":%d,"state":%s,"lastState":%s}]}}`,
		p.name, labelJSON, time.Now().Add(-age).UTC().Format(time.RFC3339), owners,
		phase, readyCond, initStatuses, p.ready, p.restarts, state, last)
}

// cwPending is the shape a pod takes when the scheduler cannot place it —
// no containerStatuses at all, and the only explanation on the PodScheduled
// condition. It reads as a blank "0/0 not Ready" to anything that does not
// look there.
func cwPending(name, app, message string) string {
	return fmt.Sprintf(`{
	  "metadata":{"name":%q,"labels":{"app.kubernetes.io/name":%q},"creationTimestamp":%q,"ownerReferences":[]},
	  "status":{"phase":"Pending",
	    "conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":%q}],
	    "containerStatuses":[]}}`,
		name, app, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339), message)
}

// cwDeploy renders a Deployment carrying the `app.kubernetes.io/name` label
// forge stamps on everything, so the pod-matching path under test is the
// real one.
func cwDeploy(name, ns string) string {
	return `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"` + name +
		`","namespace":"` + ns + `","labels":{"app.kubernetes.io/name":"` + name +
		`"}},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"app","image":"x:1"}]}}}}`
}

func cwJob(name, ns string) string {
	return `{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"` + name +
		`","namespace":"` + ns + `","labels":{"app.kubernetes.io/name":"` + name +
		`"}},"spec":{"template":{"spec":{"containers":[{"name":"app","image":"x:1"}]}}}}`
}

// cwRender builds one env's render: the objects it applies plus the
// slice of the `output` contract that decides which cluster each lands on.
// perApp pins a workload to a cluster of its own — control-plane's `dev`
// puts most workloads on k3d-control-plane and workspace-proxy on
// k3d-cp-daemon, and an env spanning clusters is the normal case, not a
// corner one.
func cwRender(t *testing.T, env, defaultCluster string, perApp map[string]string, objs ...string) envRender {
	t.Helper()
	var workloads []string
	for app, c := range perApp {
		workloads = append(workloads, fmt.Sprintf(`{"name":%q,"runtime":{"type":"cluster","cluster":%q}}`, app, c))
	}
	target := "null"
	if defaultCluster != "" {
		target = fmt.Sprintf(`{"cluster":%q,"namespace":"ignored"}`, defaultCluster)
	}
	body := fmt.Sprintf(`{"output":{"cluster_target":%s,"workloads":[%s],"manifests":[%s]}}`,
		target, strings.Join(workloads, ","), strings.Join(objs, ","))
	return renderFromJSON(t, env, body)
}

// cwPods answers a probe from a fixed table keyed "<context>/<namespace>".
// A key with no entry answers "no pods", which is a real cluster's answer
// for a namespace that does not exist.
func cwPods(t *testing.T, table map[string][]string) podLister {
	t.Helper()
	return func(_ context.Context, kctx, ns string) ([]podView, error) {
		var out []podView
		for _, body := range table[kctx+"/"+ns] {
			var p podView
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				t.Fatalf("fixture pod is not valid kubectl JSON: %v\n%s", err, body)
			}
			out = append(out, p)
		}
		return out, nil
	}
}

// cwUnreachable is every way a cluster refuses to answer: no kubeconfig
// context, an API server that is not listening, RBAC denying the list, a
// timeout. They differ only in the text, and the check must treat all of
// them the same way — as a hole.
func cwUnreachable(msg string) podLister {
	return func(context.Context, string, string) ([]podView, error) { return nil, errors.New(msg) }
}

func cwRun(t *testing.T, r envRender, list podLister) CheckResult {
	t.Helper()
	return clusterWorkloadReport(context.Background(), r, list)
}

func cwMustContain(t *testing.T, got CheckResult, want ...string) {
	t.Helper()
	hay := got.Message + "\n" + got.Evidence
	for _, w := range want {
		if !strings.Contains(hay, w) {
			t.Errorf("report never mentions %q — it is not actionable without it\nmessage: %s\nevidence:\n%s",
				w, got.Message, got.Evidence)
		}
	}
}

// --- the healthy baseline -------------------------------------------------

func TestClusterWorkloadsPassesWhenEveryRenderedWorkloadIsReady(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil,
		cwDeploy("daemon-gateway", "control-plane-dev"),
		cwDeploy("workspace-controller", "control-plane-dev"))

	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-8598f99486-frs5m", app: "daemon-gateway", ownerRS: "daemon-gateway-8598f99486", ready: true}.json(),
			cwPod{name: "workspace-controller-69c88b6fd-ldhsx", app: "workspace-controller", ownerRS: "workspace-controller-69c88b6fd", ready: true}.json(),
		},
	}))

	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q\nmessage: %s\nevidence:\n%s", got.Status, StatusPass, got.Message, got.Evidence)
	}
	cwMustContain(t, got, "2 workload(s), 2 pod(s) Ready", "k3d-control-plane/control-plane-dev")
}

// --- the daemon-gateway incident ------------------------------------------

// The hour-long outage, verbatim. The container is backing off; the reason
// it died is in lastState, and NOTHING in forge read that field. A report
// that says "CrashLoopBackOff" without "OOMKilled" names the symptom and
// hides the fix, so both must reach the one-line message — Evidence prints
// only under `-v`, and the person who needs this does not know yet that
// there is anything to look at.
func TestClusterWorkloadsSurfacesOOMKilledInTheHeadline(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil,
		cwDeploy("daemon-gateway", "control-plane-dev"))

	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{
				name: "daemon-gateway-8598f99486-frs5m", app: "daemon-gateway",
				ownerRS: "daemon-gateway-8598f99486", ready: false,
				waiting: "CrashLoopBackOff", lastTerm: "OOMKilled", exitCode: 137, restarts: 37,
			}.json(),
		},
	}))

	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q — a crashlooping OOMKilled pod is not a pass\nmessage: %s",
			got.Status, StatusFail, got.Message)
	}
	// Every one of these was missing from the report that existed at the
	// time, and each was needed to close the investigation.
	for _, want := range []string{"daemon-gateway", "daemon-gateway-8598f99486-frs5m", "CrashLoopBackOff", "OOMKilled", "137", "restarts=37"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("one-line message omits %q (Evidence only shows under -v)\nmessage: %s", want, got.Message)
		}
	}
	if !strings.Contains(got.Message, "memory limit") {
		t.Errorf("message does not say what to DO about an OOMKill:\n%s", got.Message)
	}
}

// An OOMKill on a pod that came back up is still a wrong memory limit, and
// it will recur — that is how daemon-gateway got from "restarted once" to
// CrashLoopBackOff. Warn rather than Fail (nothing is down right now), but
// never silence.
func TestClusterWorkloadsWarnsOnAnOOMKillThatRecovered(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))

	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-1-a", app: "daemon-gateway", ownerRS: "daemon-gateway-1",
				ready: true, restarts: 1, lastTerm: "OOMKilled", exitCode: 137}.json(),
		},
	}))

	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — Ready now, but it was OOMKilled and the limit is still wrong\nmessage: %s",
			got.Status, StatusWarn, got.Message)
	}
	cwMustContain(t, got, "OOMKilled", "daemon-gateway-1-a")
}

// The first snapshot after a memory limit goes wrong, measured live against
// k3d-control-plane: the pod is seconds old and 0/1, its lastState says
// OOMKilled, and the kubelet has not yet relabelled it CrashLoopBackOff.
// The startup grace must NOT soften this — an OOMKill is not a rollout in
// progress, and the whole point is to name it the first time it happens
// rather than an hour later.
func TestClusterWorkloadsFailsOnAFreshOOMKillDespiteTheStartupGrace(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))

	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-67c74dc9dd-hlmsh", app: "daemon-gateway",
				ownerRS: "daemon-gateway-67c74dc9dd", ready: false, restarts: 1,
				lastTerm: "OOMKilled", exitCode: 137, age: 12 * time.Second}.json(),
		},
	}))

	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q — a pod that is DOWN after an OOM kill is not a rollout in progress\nmessage: %s",
			got.Status, StatusFail, got.Message)
	}
	cwMustContain(t, got, "daemon-gateway-67c74dc9dd-hlmsh", "OOMKilled", "137")
}

// --- the ten-hour incident ------------------------------------------------

func TestClusterWorkloadsFlagsConfigAndImageFailures(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"admin-api's missing Secret key", "CreateContainerConfigError"},
		{"an image that does not resolve", "ImagePullBackOff"},
		{"a registry that refused the pull", "ErrImagePull"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cwRender(t, "prod", "gke-prod", nil, cwDeploy("admin-api", "control-plane-prod"))
			got := cwRun(t, r, cwPods(t, map[string][]string{
				"gke-prod/control-plane-prod": {
					cwPod{name: "admin-api-6b4f-2xz9m", app: "admin-api", ownerRS: "admin-api-6b4f",
						waiting: tc.reason, phase: "Pending"}.json(),
				},
			}))
			if got.Status != StatusFail {
				t.Fatalf("status = %q, want %q for %s\nmessage: %s", got.Status, StatusFail, tc.reason, got.Message)
			}
			// No startup grace applies: these reasons never resolve on their
			// own, so waiting longer only lengthens the outage.
			cwMustContain(t, got, "admin-api-6b4f-2xz9m", tc.reason)
		})
	}
}

// litellm and reliant-api-server reached ~120 and ~130 restarts. A pod that
// happens to be Ready at the instant of the snapshot is still a container
// dying repeatedly.
func TestClusterWorkloadsWarnsOnARestartCountThatKeepsClimbing(t *testing.T) {
	r := cwRender(t, "prod", "gke-prod", nil, cwDeploy("litellm", "control-plane-prod"))
	got := cwRun(t, r, cwPods(t, map[string][]string{
		"gke-prod/control-plane-prod": {
			cwPod{name: "litellm-77d-9k4", app: "litellm", ownerRS: "litellm-77d", ready: true, restarts: 129}.json(),
		},
	}))
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — 129 restarts is not healthy\nmessage: %s", got.Status, StatusWarn, got.Message)
	}
	cwMustContain(t, got, "litellm-77d-9k4", "restarts=129")
}

// --- absence is as invisible as a crash -----------------------------------

func TestClusterWorkloadsFailsWhenARenderedDeploymentHasNoPods(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))
	got := cwRun(t, r, cwPods(t, nil)) // namespace empty: never deployed, pruned, or scaled to 0

	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q — the render declares a replica and the cluster has none\nmessage: %s",
			got.Status, StatusFail, got.Message)
	}
	cwMustContain(t, got, "daemon-gateway", "NO PODS")
}

// A migrate Job whose pods were reaped by ttlSecondsAfterFinished, and a
// CronJob between schedules, have no pods and are not broken. Reporting
// them would be a permanent false alarm on every project that migrates —
// which is how a check gets ignored.
func TestClusterWorkloadsStaysQuietAboutFinishedJobs(t *testing.T) {
	r := cwRender(t, "prod", "gke-prod", nil, cwJob("control-plane-migrate", "control-plane-prod"))
	got := cwRun(t, r, cwPods(t, nil))
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — a completed Job's pods are garbage-collected\nmessage: %s",
			got.Status, StatusPass, got.Message)
	}
}

// A Succeeded pod is not "0/1 not Ready" — that is a Job's resting state.
func TestClusterWorkloadsTreatsSucceededJobPodsAsHealthy(t *testing.T) {
	r := cwRender(t, "prod", "gke-prod", nil, cwJob("control-plane-migrate", "control-plane-prod"))
	got := cwRun(t, r, cwPods(t, map[string][]string{
		"gke-prod/control-plane-prod": {
			cwPod{name: "control-plane-migrate-9v7f2", app: "control-plane-migrate",
				ownerJob: "control-plane-migrate", phase: "Succeeded", ready: false}.json(),
		},
	}))
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q\nmessage: %s\nevidence:\n%s", got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// --- mid-rollout is not an outage -----------------------------------------

func TestClusterWorkloadsWarnsRatherThanFailsInsideTheStartupGrace(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))
	fresh := cwPod{name: "daemon-gateway-new-abc", app: "daemon-gateway", ownerRS: "daemon-gateway-new",
		ready: false, phase: "Pending", waiting: "ContainerCreating", age: 5 * time.Second}.json()

	got := cwRun(t, r, cwPods(t, map[string][]string{"k3d-control-plane/control-plane-dev": {fresh}}))
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — a 5s-old pod is a deploy in progress\nmessage: %s",
			got.Status, StatusWarn, got.Message)
	}

	stuck := cwPod{name: "daemon-gateway-new-abc", app: "daemon-gateway", ownerRS: "daemon-gateway-new",
		ready: false, phase: "Pending", waiting: "ContainerCreating", age: 20 * time.Minute}.json()
	got = cwRun(t, r, cwPods(t, map[string][]string{"k3d-control-plane/control-plane-dev": {stuck}}))
	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q — 20 minutes in ContainerCreating is not a rollout\nmessage: %s",
			got.Status, StatusFail, got.Message)
	}
}

// Readiness duration comes from the condition transition, not pod creation.
// Missing timestamps must not invent an outage lasting the pod's whole life.
func TestClusterWorkloadsUsesReadinessTransitionForOutageDuration(t *testing.T) {
	now := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, transition, want string
	}{
		{"recent outage", `,"lastTransitionTime":"2026-09-30T13:59:30Z"`, "not Ready for 30s"},
		{"missing timestamp", "", " — not Ready"},
		{"future timestamp", `,"lastTransitionTime":"2026-09-30T14:01:00Z"`, " — not Ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"metadata":{"name":"bridge-old","creationTimestamp":"2026-09-27T05:00:00Z"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"` + tc.transition + `}],"containerStatuses":[{"name":"relay","ready":false}]}}`
			var pod podView
			if err := json.Unmarshal([]byte(raw), &pod); err != nil {
				t.Fatal(err)
			}
			finding, bad := judgePod(probeTarget{}, &clusterWorkload{name: "bridge"}, pod, now)
			if !bad || finding.severity != StatusFail || !strings.HasSuffix(finding.detail, tc.want) {
				t.Fatalf("finding = %+v, bad = %v; want failure ending %q", finding, bad, tc.want)
			}
		})
	}
}

// An unschedulable pod has NO container statuses at all: the reason lives on
// the PodScheduled condition, and anything that does not read it reports a
// blank "0/0 not Ready" that explains nothing.
func TestClusterWorkloadsExplainsAnUnschedulablePod(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))
	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPending("daemon-gateway-7d9-zzz", "daemon-gateway", "0/1 nodes are available: 1 Insufficient memory."),
		},
	}))
	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusFail, got.Message)
	}
	cwMustContain(t, got, "daemon-gateway-7d9-zzz", "Insufficient memory")
}

// A pod held up by an init container is down just as hard, and the failing
// container is the init one — naming the app container would send the reader
// to the wrong logs.
func TestClusterWorkloadsNamesAFailingInitContainer(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))
	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-1-b", app: "daemon-gateway", ownerRS: "daemon-gateway-1",
				ready: false, phase: "Pending", initBad: "CrashLoopBackOff"}.json(),
		},
	}))
	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusFail, got.Message)
	}
	cwMustContain(t, got, "init container migrate", "CrashLoopBackOff")
}

// --- scoping --------------------------------------------------------------

// A namespace is not an environment. `dev` and `dev-k8s` both render into
// control-plane-dev, and control-plane-dev also holds pods no forge env
// owns. Reporting on all of them would make one env's status a function of
// another env's leftovers — a separate defect with its own check
// (CheckObjectCollision), and a fast route to a report nobody trusts.
func TestClusterWorkloadsIgnoresPodsThisEnvDoesNotRender(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))

	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-8598-frs5m", app: "daemon-gateway", ownerRS: "daemon-gateway-8598", ready: true}.json(),
			// Another env's, and thoroughly broken. Not this report's business.
			cwPod{name: "internal-console-devtest-798-94qtg", app: "internal-console-devtest",
				ownerRS: "internal-console-devtest-798", ready: false, waiting: "CrashLoopBackOff",
				lastTerm: "OOMKilled", exitCode: 137, restarts: 88, envTag: "dev-k8s"}.json(),
		},
	}))

	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — the broken pod belongs to no workload `dev` renders\nmessage: %s\nevidence:\n%s",
			got.Status, StatusPass, got.Message, got.Evidence)
	}
	if strings.Contains(got.Message+got.Evidence, "internal-console-devtest") {
		t.Errorf("report speaks for a pod this env does not deploy:\n%s\n%s", got.Message, got.Evidence)
	}
	cwMustContain(t, got, "1 workload(s), 1 pod(s) Ready")
}

// Pod matching goes through the ownerReference chain, not a name prefix.
// `reliant-api` and `reliant-api-server` are both real control-plane names,
// and prefix matching would attribute one's pods to the other — reporting
// the wrong workload as broken is worse than reporting nothing.
func TestClusterWorkloadsDoesNotConfuseWorkloadsWithASharedPrefix(t *testing.T) {
	r := cwRender(t, "prod", "gke-prod", nil,
		cwDeploy("reliant-api", "control-plane-prod"),
		cwDeploy("reliant-api-server", "control-plane-prod"))

	got := cwRun(t, r, cwPods(t, map[string][]string{
		"gke-prod/control-plane-prod": {
			cwPod{name: "reliant-api-5f4-aaa", app: "reliant-api", ownerRS: "reliant-api-5f4", ready: true}.json(),
			cwPod{name: "reliant-api-server-6c1-bbb", app: "reliant-api-server", ownerRS: "reliant-api-server-6c1",
				ready: false, waiting: "CrashLoopBackOff", restarts: 131}.json(),
		},
	}))

	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusFail, got.Message)
	}
	if !strings.Contains(got.Message, "reliant-api-server (pod reliant-api-server-6c1-bbb)") {
		t.Errorf("the failing workload is misattributed:\n%s", got.Message)
	}
	if strings.Contains(got.Evidence, "x reliant-api  ") {
		t.Errorf("the healthy workload was reported broken:\n%s", got.Evidence)
	}
}

// --- multi-cluster --------------------------------------------------------

// An env spanning clusters is the normal case, not a corner one:
// control-plane's `dev` puts most workloads on k3d-control-plane and
// workspace-proxy on k3d-cp-daemon. Each cluster is a separate probe, each
// workload is judged against ITS cluster, and a workload broken on the
// second cluster must not be hidden by a healthy first.
func TestClusterWorkloadsSpansEveryClusterTheEnvDeploysTo(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane",
		map[string]string{"daemon-gateway": "k3d-control-plane", "workspace-proxy": "k3d-cp-daemon"},
		cwDeploy("daemon-gateway", "control-plane-dev"),
		cwDeploy("workspace-proxy", "control-plane-dev"))

	got := cwRun(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-8598-frs5m", app: "daemon-gateway", ownerRS: "daemon-gateway-8598", ready: true}.json(),
		},
		"k3d-cp-daemon/control-plane-dev": {
			cwPod{name: "workspace-proxy-5cc-7bl24", app: "workspace-proxy", ownerRS: "workspace-proxy-5cc",
				ready: false, waiting: "CrashLoopBackOff", lastTerm: "Error", exitCode: 2, restarts: 9}.json(),
		},
	}))

	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q — the second cluster's workload is crashlooping\nmessage: %s",
			got.Status, StatusFail, got.Message)
	}
	cwMustContain(t, got, "k3d-cp-daemon/control-plane-dev", "workspace-proxy-5cc-7bl24", "last=Error(exit 2)")
	// The healthy cluster is still reported, so the reader can see the whole
	// env rather than only its worst part.
	cwMustContain(t, got, "k3d-control-plane/control-plane-dev", "daemon-gateway")
}

// One cluster answering and one refusing is the case that must not become a
// pass: the reachable half is genuinely fine, and saying so without saying
// the other half is unknown is exactly the report that hid the outage.
func TestClusterWorkloadsIsUndeterminedWhenOnlySomeClustersAnswer(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane",
		map[string]string{"daemon-gateway": "k3d-control-plane", "workspace-proxy": "k3d-cp-daemon"},
		cwDeploy("daemon-gateway", "control-plane-dev"),
		cwDeploy("workspace-proxy", "control-plane-dev"))

	healthy := cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-8598-frs5m", app: "daemon-gateway", ownerRS: "daemon-gateway-8598", ready: true}.json(),
		},
	})
	half := func(ctx context.Context, kctx, ns string) ([]podView, error) {
		if kctx == "k3d-cp-daemon" {
			return nil, errors.New(`context "k3d-cp-daemon" does not exist`)
		}
		return healthy(ctx, kctx, ns)
	}

	got := cwRun(t, r, half)
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q — half the env could not be looked at\nmessage: %s",
			got.Status, StatusUnknown, got.Message)
	}
	cwMustContain(t, got, "k3d-cp-daemon", "NOT a pass")
}

// A failure must not absolve the holes. Reporting "1 failing" while silently
// dropping "and one cluster never answered" is a smaller version of the very
// defect this check closes, so both reach the one line.
func TestClusterWorkloadsStillNamesTheHolesWhenSomethingIsAlreadyFailing(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane",
		map[string]string{"daemon-gateway": "k3d-control-plane", "workspace-proxy": "k3d-cp-daemon"},
		cwDeploy("daemon-gateway", "control-plane-dev"),
		cwDeploy("workspace-proxy", "control-plane-dev"))

	broken := cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{name: "daemon-gateway-8598-frs5m", app: "daemon-gateway", ownerRS: "daemon-gateway-8598",
				ready: false, waiting: "CrashLoopBackOff", lastTerm: "OOMKilled", exitCode: 137, restarts: 37}.json(),
		},
	})
	mixed := func(ctx context.Context, kctx, ns string) ([]podView, error) {
		if kctx == "k3d-cp-daemon" {
			return nil, errors.New("Unable to connect to the server")
		}
		return broken(ctx, kctx, ns)
	}

	got := cwRun(t, r, mixed)
	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusFail, got.Message)
	}
	if !strings.Contains(got.Message, "NOT LOOKED AT") {
		t.Errorf("the unreachable cluster vanished from the one-line report:\n%s", got.Message)
	}
	cwMustContain(t, got, "OOMKilled", "k3d-cp-daemon")
}

// --- the three-state contract ---------------------------------------------

// This is the property the whole check turns on. Cluster cwUnreachable,
// kubectl missing, context absent, RBAC denying the list: forge did not
// obtain the facts, so it has no answer. Never Pass (that is the defect
// being closed) and never Fail (nothing is known to be broken).
func TestClusterWorkloadsIsUndeterminedWhenItCannotLook(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))

	for _, why := range []string{
		`Unable to connect to the server: dial tcp 127.0.0.1:6443: connect: connection refused`,
		`error: context "k3d-control-plane" does not exist`,
		`kubectl is not on PATH`,
		`pods is forbidden: User "dev" cannot list resource "pods" in the namespace "control-plane-dev"`,
		`context "k3d-control-plane" did not answer within 6s`,
	} {
		got := cwRun(t, r, cwUnreachable(why))
		if got.Status != StatusUnknown {
			t.Errorf("status = %q, want %q for %q — a check that passes when it could not look is what created this gap\nmessage: %s",
				got.Status, StatusUnknown, why, got.Message)
		}
		cwMustContain(t, got, "k3d-control-plane/control-plane-dev")
	}
}

// A workload the render cannot route has no cluster to ask. Falling back to
// kubectl's CURRENT context would ask an unrelated cluster and believe the
// answer — the footgun internal/cluster.KubectlApply refuses for writes.
func TestClusterWorkloadsIsUndeterminedWhenTheRenderNamesNoCluster(t *testing.T) {
	r := cwRender(t, "dev", "", nil, cwDeploy("daemon-gateway", "control-plane-dev"))
	got := cwRun(t, r, cwPods(t, nil))

	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q — there is no cluster to ask\nmessage: %s", got.Status, StatusUnknown, got.Message)
	}
	cwMustContain(t, got, "daemon-gateway", "which cluster")
}

// SKIP is reserved for "not applicable": this env genuinely deploys nothing
// to Kubernetes. It must not render the same as UNDETERMINED — one is a
// finished answer, the other is a hole.
func TestClusterWorkloadsSkipsAnEnvThatDeploysNoWorkloads(t *testing.T) {
	// A host-mode env still renders a Namespace, a ConfigMap and RBAC. None
	// of them owns a pod, so there is nothing for this check to be about.
	r := cwRender(t, "dev", "k3d-control-plane", nil,
		`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"control-plane-dev"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cfg","namespace":"control-plane-dev"}}`)

	got := cwRun(t, r, cwPods(t, nil))
	if got.Status != StatusSkip {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusSkip, got.Message)
	}
}

// A workload the render asks for zero replicas of is deliberate, not missing.
func TestClusterWorkloadsRespectsARenderedReplicaCountOfZero(t *testing.T) {
	scaled := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"batch","namespace":"ns",` +
		`"labels":{"app.kubernetes.io/name":"batch"}},"spec":{"replicas":0}}`
	r := cwRender(t, "dev", "k3d-control-plane", nil, scaled)

	got := cwRun(t, r, cwPods(t, nil))
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — the render itself asks for zero\nmessage: %s", got.Status, StatusPass, got.Message)
	}
}

// --- the exported entry point ---------------------------------------------

// A project with no deploy/kcl/<env>/main.k (--kind cli / library) has
// answered by its own shape.
func TestCheckClusterWorkloadsSkipsAProjectWithNoKCLForTheEnv(t *testing.T) {
	env := &Environment{ProjectName: "test", ProjectDir: t.TempDir(), Env: "dev"}
	got := CheckClusterWorkloads(context.Background(), env)
	if got.Status != StatusSkip {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusSkip, got.Message)
	}
}

// No env named means no render, which means no idea what should be running.
func TestCheckClusterWorkloadsIsUndeterminedWithNoEnv(t *testing.T) {
	env := &Environment{ProjectName: "test", ProjectDir: t.TempDir()}
	got := CheckClusterWorkloads(context.Background(), env)
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusUnknown, got.Message)
	}
}

// --- registration ---------------------------------------------------------

// The check is worth nothing unregistered — the gap it closes existed for a
// year with the facts one kubectl away. Pinned so a refactor of
// runtimeSignals cannot quietly drop it back out of `forge env status`.
func TestClusterWorkloadsIsWiredIntoTheRuntimeCheckSet(t *testing.T) {
	for _, signal := range []string{"", "app"} {
		found := false
		for _, c := range runtimeSignals()[signal] {
			if c.name == clusterWorkloadsCheckName {
				found = true
			}
		}
		if !found {
			t.Errorf("signal %q does not run %q — `forge env status` would report on the developer's "+
				"machine and stay silent about every pod forge deployed", signal, clusterWorkloadsCheckName)
		}
	}
}
