package doctor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClusterWorkloadsIgnoreSupersededJobPodsWithTheSameApp(t *testing.T) {
	const current = "bootstrap-18cab959c9"
	job := strings.Replace(cwJob(current, "dev"), `"app.kubernetes.io/name":"`+current+`"`, `"app.kubernetes.io/name":"bootstrap"`, 1)
	r := cwRender(t, "dev", "k3d-dev", nil, job)
	old := cwPod{name: "bootstrap-old-failed", app: "bootstrap", ownerJob: "bootstrap-5a22734d73", phase: "Failed"}.json()
	for _, phase := range []string{"Succeeded", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			got := cwRun(t, r, cwPods(t, map[string][]string{"k3d-dev/dev": {
				old, cwPod{name: "bootstrap-current", app: "bootstrap", ownerJob: current, phase: phase}.json(),
			}}))
			want := StatusPass
			if phase == "Failed" {
				want = StatusFail
			}
			if got.Status != want {
				t.Fatalf("status = %s, want %s: %s", got.Status, want, got.Message)
			}
			data, err := json.Marshal(got.Cluster)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got.Message+got.Evidence+string(data), "bootstrap-old-failed") {
				t.Fatal("superseded Job leaked into current health or JSON inventory")
			}
			w := cwFindWorkload(t, got.Cluster, current)
			if len(w.Pods) != 1 || w.Pods[0].Name != "bootstrap-current" {
				t.Fatalf("wrong pods attributed: %+v", w.Pods)
			}
		})
	}
}

func TestMatchWorkloadHonorsControllerIdentity(t *testing.T) {
	deploy := &clusterWorkload{kind: "Deployment", name: "api", app: "shared"}
	job := &clusterWorkload{kind: "Job", name: "api", app: "shared"}
	cron := &clusterWorkload{kind: "CronJob", name: "backup", app: "shared"}
	byName := map[workloadKey]*clusterWorkload{{"Deployment", "api"}: deploy, {"Job", "api"}: job, {"CronJob", "backup"}: cron}
	for _, tc := range []struct {
		name, owner string
		want        *clusterWorkload
	}{
		{"deployment via ReplicaSet", `[{"kind":"ReplicaSet","name":"api-abc","controller":true}]`, deploy},
		{"Job with same name", `[{"kind":"Job","name":"api","controller":true}]`, job},
		{"scheduled CronJob", `[{"kind":"Job","name":"backup-29846520","controller":true}]`, cron},
		{"content hash is not a CronJob schedule", `[{"kind":"Job","name":"backup-abcd","controller":true}]`, nil},
		{"unrendered Job cannot fall back to app", `[{"kind":"Job","name":"api-old","controller":true}]`, nil},
		{"unrendered ReplicaSet cannot fall back to app", `[{"kind":"ReplicaSet","name":"other-abc","controller":true}]`, nil},
		{"wrong kind with same name", `[{"kind":"StatefulSet","name":"api","controller":true}]`, nil},
		{"non-controller does not override controller", `[{"kind":"Job","name":"api","controller":false},{"kind":"ReplicaSet","name":"other-abc","controller":true}]`, nil},
		{"omitted controller flag does not override controller", `[{"kind":"Job","name":"api"},{"kind":"ReplicaSet","name":"other-abc","controller":true}]`, nil},
		{"bare pod retains label fallback", `[]`, deploy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p podView
			if err := json.Unmarshal([]byte(`{"metadata":{"name":"pod","labels":{"app.kubernetes.io/name":"shared"},"ownerReferences":`+tc.owner+`}}`), &p); err != nil {
				t.Fatal(err)
			}
			if got := matchWorkload(p, byName, map[string][]*clusterWorkload{"shared": {deploy}}); got != tc.want {
				t.Fatalf("matched %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClusterInventorySeparatesKindsWithTheSameName(t *testing.T) {
	r := cwRender(t, "dev", "k3d-dev", nil, cwDeploy("api", "dev"), cwJob("api", "dev"))
	got := cwRun(t, r, cwPods(t, map[string][]string{"k3d-dev/dev": {
		cwPod{name: "api-server", app: "api", ownerRS: "api-abc", ready: true}.json(),
		cwPod{name: "api-job", app: "api", ownerJob: "api", phase: "Failed"}.json(),
	}}))
	if got.Status != StatusFail || len(got.Cluster.Workloads) != 2 {
		t.Fatalf("expected one healthy Deployment and one failed Job: %+v", got)
	}
	for _, w := range got.Cluster.Workloads {
		want := StatusPass
		pod := "api-server"
		if w.Kind == "job" {
			want, pod = StatusFail, "api-job"
		}
		if w.Status != want || len(w.Pods) != 1 || w.Pods[0].Name != pod {
			t.Errorf("kind/name ownership or finding attribution crossed workloads: %+v", w)
		}
	}
}
