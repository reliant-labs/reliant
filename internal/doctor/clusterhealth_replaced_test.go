package doctor

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// A pod in a terminal phase is history, not health. After a Docker Desktop
// restart plus kubelet DiskPressure evictions, `forge env up dev` on
// control-plane failed with
//
//	barman-cloud (pod barman-cloud-74b76d548f-c4wm7): 0/1 Ready  Succeeded
//	barman-cloud OOMKilled (exit 137) — raise its memory limit  restarts=12 |
//	claimable-dns (pod claimable-dns-7565b5dbc7-q74nd): … | +19 more (-v)
//	[21 failing …]
//
// while every one of those Deployments had a replacement pod 1/1 Running for
// 21 minutes. The flagged pods were leftovers of the SAME ReplicaSet in phase
// Failed or Succeeded (ContainerStatusUnknown, Completed, Error, Evicted) —
// pods the controller had already stopped counting and replaced. Terminated
// pods are kept until the terminated-pod GC threshold (12500 by default), so
// on a dev cluster they never go away by themselves, and every routine
// restart made the next `env up` fail.
//
// The judgement mirrors the controllers' own: a replica controller counts
// only ACTIVE pods (not Succeeded, not Failed, not being deleted), and a Job
// retries a failed pod with a newer one. What was replaced is not judged;
// what is current still is, with the full per-pod diagnosis.

func cwStatefulSet(name, ns string) string {
	return `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"` + name +
		`","namespace":"` + ns + `","labels":{"app.kubernetes.io/name":"` + name +
		`"}},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"app","image":"x:1"}]}}}}`
}

func cwCronJob(name, ns string) string {
	return `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"` + name +
		`","namespace":"` + ns + `","labels":{"app.kubernetes.io/name":"` + name +
		`"}},"spec":{"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"name":"app","image":"x:1"}]}}}}}}`
}

func TestClusterWorkloadsJudgesCurrentPodsNotReplacedOnes(t *testing.T) {
	const ns = "control-plane-dev"
	const rs = "barman-cloud-74b76d548f"

	// The incident's leftovers, one per terminal shape seen on the cluster.
	// Each was created before the replacement, as the controller does it.
	oomSucceeded := cwPod{name: rs + "-c4wm7", app: "barman-cloud", ownerRS: rs, phase: "Succeeded",
		term: "OOMKilled", termExit: 137, restarts: 12, age: 3 * time.Hour}.json()
	statusUnknown := cwPod{name: rs + "-h2x9q", app: "barman-cloud", ownerRS: rs, phase: "Failed",
		term: "ContainerStatusUnknown", termExit: 137, age: 3 * time.Hour}.json()
	evicted := cwPod{name: rs + "-p8vzl", app: "barman-cloud", ownerRS: rs, phase: "Failed",
		reason: "Evicted", term: "Error", termExit: 137, age: 2 * time.Hour}.json()
	completed := cwPod{name: rs + "-r5mfd", app: "barman-cloud", ownerRS: rs, phase: "Succeeded",
		term: "Completed", age: 2 * time.Hour}.json()
	terminating := cwPod{name: rs + "-t7kcw", app: "barman-cloud", ownerRS: rs, deleting: true,
		age: 2 * time.Hour}.json()
	replacement := cwPod{name: rs + "-gk4dr", app: "barman-cloud", ownerRS: rs, ready: true,
		age: 21 * time.Minute}.json()

	for _, tc := range []struct {
		name     string
		objs     []string
		pods     []string
		want     Status
		mention  []string // must appear in the message or evidence
		absent   []string // must appear nowhere in the report or inventory
		workload string   // inventory row to inspect
		invPods  []string // the pods that row must list
	}{
		{
			name:     "replaced terminal leftovers do not fail a Ready Deployment",
			objs:     []string{cwDeploy("barman-cloud", ns)},
			pods:     []string{oomSucceeded, statusUnknown, evicted, completed, replacement},
			want:     StatusPass,
			mention:  []string{"1 workload(s), 1 pod(s) Ready", rs + "-gk4dr", "4 replaced pod(s) not judged"},
			absent:   []string{rs + "-c4wm7", rs + "-h2x9q", rs + "-p8vzl", rs + "-r5mfd"},
			workload: "barman-cloud", invPods: []string{rs + "-gk4dr"},
		},
		{
			name:     "a Terminating pod the controller already replaced is not judged",
			objs:     []string{cwDeploy("barman-cloud", ns)},
			pods:     []string{terminating, replacement},
			want:     StatusPass,
			mention:  []string{"1 replaced pod(s) not judged"},
			absent:   []string{rs + "-t7kcw"},
			workload: "barman-cloud", invPods: []string{rs + "-gk4dr"},
		},
		{
			name: "a crashlooping current pod still fails, with its OOMKilled diagnosis",
			objs: []string{cwDeploy("barman-cloud", ns)},
			pods: []string{oomSucceeded, evicted, cwPod{name: rs + "-zq2lm", app: "barman-cloud", ownerRS: rs,
				waiting: "CrashLoopBackOff", lastTerm: "OOMKilled", exitCode: 137, restarts: 9, age: 20 * time.Minute}.json()},
			want:     StatusFail,
			mention:  []string{rs + "-zq2lm", "CrashLoopBackOff", "OOMKilled (exit 137) — raise its memory limit", "restarts=9"},
			absent:   []string{rs + "-c4wm7", rs + "-p8vzl"},
			workload: "barman-cloud", invPods: []string{rs + "-zq2lm"},
		},
		{
			name: "a current pod OOM-killed right now still fails",
			objs: []string{cwDeploy("barman-cloud", ns)},
			pods: []string{completed, cwPod{name: rs + "-v6cbn", app: "barman-cloud", ownerRS: rs,
				term: "OOMKilled", termExit: 137, restarts: 3}.json()},
			want:    StatusFail,
			mention: []string{rs + "-v6cbn", "OOMKilled (exit 137)"},
			absent:  []string{rs + "-r5mfd"},
		},
		{
			name:    "a Deployment whose only pods are terminal is down, and says why",
			objs:    []string{cwDeploy("barman-cloud", ns)},
			pods:    []string{oomSucceeded, evicted},
			want:    StatusFail,
			mention: []string{rs + "-c4wm7", "OOMKilled (exit 137) — raise its memory limit", rs + "-p8vzl", "Evicted"},
		},
		{
			name:    "a Deployment whose only pod exited cleanly is down too",
			objs:    []string{cwDeploy("barman-cloud", ns)},
			pods:    []string{completed},
			want:    StatusFail,
			mention: []string{"NO LIVE PODS", rs + "-r5mfd"},
		},
		{
			name: "a StatefulSet's failed leftover is replaced by its Ready pod",
			objs: []string{cwStatefulSet("nats", ns)},
			pods: []string{
				cwPod{name: "nats-0-old", app: "nats", ownerSTS: "nats", phase: "Failed",
					reason: "Evicted", term: "Error", termExit: 137, age: 3 * time.Hour}.json(),
				cwPod{name: "nats-0", app: "nats", ownerSTS: "nats", ready: true, age: time.Hour}.json(),
			},
			want:     StatusPass,
			absent:   []string{"nats-0-old"},
			workload: "nats", invPods: []string{"nats-0"},
		},
		{
			name: "a Job attempt that failed and was retried to success is not a failure",
			objs: []string{cwJob("migrate", ns)},
			pods: []string{
				cwPod{name: "migrate-a1", app: "migrate", ownerJob: "migrate", phase: "Failed",
					term: "Error", termExit: 1, age: 10 * time.Minute}.json(),
				cwPod{name: "migrate-b2", app: "migrate", ownerJob: "migrate", phase: "Succeeded",
					term: "Completed", age: 8 * time.Minute}.json(),
			},
			want:     StatusPass,
			absent:   []string{"migrate-a1"},
			workload: "migrate", invPods: []string{"migrate-b2"},
		},
		{
			// A retry still in progress has not recovered anything: it may
			// fail too, so the failed attempt stays reported until a newer
			// attempt Succeeds.
			name: "a Job attempt followed by a running retry still fails",
			objs: []string{cwJob("migrate", ns)},
			pods: []string{
				cwPod{name: "migrate-a1", app: "migrate", ownerJob: "migrate", phase: "Failed",
					term: "Error", termExit: 1, age: 10 * time.Minute}.json(),
				cwPod{name: "migrate-b2", app: "migrate", ownerJob: "migrate", ready: true, age: 8 * time.Minute}.json(),
			},
			want:     StatusFail,
			mention:  []string{"migrate-a1", "terminated Error (exit 1)"},
			workload: "migrate", invPods: []string{"migrate-a1", "migrate-b2"},
		},
		{
			name: "a CronJob's failed run followed by a pending one still fails",
			objs: []string{cwCronJob("backup", ns)},
			pods: []string{
				cwPod{name: "backup-29846520-bad", app: "backup", ownerJob: "backup-29846520", phase: "Failed",
					term: "Error", termExit: 2, age: 25 * time.Hour}.json(),
				cwPod{name: "backup-29847960-new", app: "backup", ownerJob: "backup-29847960", phase: "Pending",
					age: time.Minute}.json(),
			},
			want:    StatusFail,
			mention: []string{"backup-29846520-bad", "terminated Error (exit 2)"},
		},
		{
			name: "a Job that exhausted its retries fails on every attempt",
			objs: []string{cwJob("migrate", ns)},
			pods: []string{
				cwPod{name: "migrate-a1", app: "migrate", ownerJob: "migrate", phase: "Failed",
					term: "Error", termExit: 1, age: 10 * time.Minute}.json(),
				cwPod{name: "migrate-b2", app: "migrate", ownerJob: "migrate", phase: "Failed",
					term: "Error", termExit: 1, age: 8 * time.Minute}.json(),
			},
			want:    StatusFail,
			mention: []string{"migrate-a1", "migrate-b2", "pod phase Failed"},
		},
		{
			// An older successful run must not hide a newer failure: only a
			// NEWER pod supersedes a failed one.
			name: "a CronJob's newest run failing is not hidden by an older success",
			objs: []string{cwCronJob("backup", ns)},
			pods: []string{
				cwPod{name: "backup-29846520-ok", app: "backup", ownerJob: "backup-29846520", phase: "Succeeded",
					term: "Completed", age: 25 * time.Hour}.json(),
				cwPod{name: "backup-29847960-bad", app: "backup", ownerJob: "backup-29847960", phase: "Failed",
					term: "Error", termExit: 2, age: time.Hour}.json(),
			},
			want:    StatusFail,
			mention: []string{"backup-29847960-bad", "terminated Error (exit 2)"},
		},
		{
			name: "a CronJob's failed run followed by a successful one passes",
			objs: []string{cwCronJob("backup", ns)},
			pods: []string{
				cwPod{name: "backup-29846520-bad", app: "backup", ownerJob: "backup-29846520", phase: "Failed",
					term: "Error", termExit: 2, age: 25 * time.Hour}.json(),
				cwPod{name: "backup-29847960-ok", app: "backup", ownerJob: "backup-29847960", phase: "Succeeded",
					term: "Completed", age: time.Hour}.json(),
			},
			want:     StatusPass,
			absent:   []string{"backup-29846520-bad"},
			workload: "backup", invPods: []string{"backup-29847960-ok"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cwRender(t, "dev", "k3d-control-plane", nil, tc.objs...)
			got := cwRun(t, r, cwPods(t, map[string][]string{"k3d-control-plane/" + ns: tc.pods}))
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q\nmessage: %s\nevidence:\n%s", got.Status, tc.want, got.Message, got.Evidence)
			}
			cwMustContain(t, got, tc.mention...)
			data, err := json.Marshal(got.Cluster)
			if err != nil {
				t.Fatal(err)
			}
			inv := string(data)
			for _, a := range tc.absent {
				if strings.Contains(got.Message+got.Evidence+inv, a) {
					t.Errorf("replaced pod %q leaked into the verdict or inventory\nmessage: %s\nevidence:\n%s", a, got.Message, got.Evidence)
				}
			}
			if tc.workload == "" {
				return
			}
			var names []string
			for _, p := range cwFindWorkload(t, got.Cluster, tc.workload).Pods {
				names = append(names, p.Name)
			}
			if !slices.Equal(names, tc.invPods) {
				t.Errorf("inventory pods for %s = %v, want %v", tc.workload, names, tc.invPods)
			}
		})
	}
}
