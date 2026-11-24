package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The superseded-Job prune (job_prune.go).
//
// Two layers. The decision (supersededJobs) is pure, so each guard in its file
// comment is one table row. The claim a user cares about is a statement about two
// applies and what the first left behind, so the rest drive the real Apply against
// the stateful fake cluster (fake_cluster_test.go): seed a backlog, apply a render,
// read what is live.

const jobNS = "control-plane-prod"

var jobEpoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// jobDoc is a live batch/v1 Job as `kubectl get -o json` returns it: n days
// after jobEpoch, in state "complete" | "failed" | "running" | "pending".
func jobDoc(ns, name, base, state string, day int, extraLabels map[string]string) map[string]any {
	labels := map[string]any{
		AppManagedByLabel: "forge",
		"forge.dev/env":   "prod",
	}
	if base != "" {
		labels["forge.dev/job-name"] = base
		labels["forge.dev/spec-hash"] = strings.TrimPrefix(name, base+"-")
	}
	for k, v := range extraLabels {
		labels[k] = v
	}
	status := map[string]any{}
	switch state {
	case "complete":
		status["succeeded"] = 1
		status["conditions"] = []any{map[string]any{"type": "Complete", "status": "True"}}
	case "failed":
		status["failed"] = 4
		status["conditions"] = []any{map[string]any{"type": "Failed", "status": "True"}}
	case "running":
		status["active"] = 1
	}
	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name": name, "namespace": ns, "labels": labels,
			"creationTimestamp": jobEpoch.AddDate(0, 0, day).Format(time.RFC3339),
		},
		"status": status,
	}
}

func asLiveJobs(t *testing.T, docs ...map[string]any) []liveJob {
	t.Helper()
	b, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	var out []liveJob
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// jobRender renders `names` (base -> hash-suffixed name) as the manifest stream an
// apply sends, in jobNS.
func jobRender(names ...string) string {
	var docs []string
	for _, n := range names {
		base := n[:strings.LastIndex(n, "-")]
		docs = append(docs, fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: forge
    forge.dev/env: prod
    forge.dev/job-name: %s
    forge.dev/spec-hash: %s
  annotations:
    forge.dev/deploy-phase: post-rollout
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: main
          image: example/app:1`, n, jobNS, base, strings.TrimPrefix(n, base+"-")))
	}
	return strings.Join(docs, "\n---\n")
}

func TestSupersededJobs_DecisionTable(t *testing.T) {
	// Six finished migrations, oldest first, and the render that is applying h6.
	var mig []map[string]any
	for i := 1; i <= 6; i++ {
		mig = append(mig, jobDoc(jobNS, fmt.Sprintf("migrate-h%d", i), "migrate", "complete", i, nil))
	}
	cases := []struct {
		name     string
		live     []map[string]any
		rendered []string
		env      string
		want     []string
	}{
		{
			name:     "keeps the newest three finished, deletes the older ones",
			live:     mig,
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name:     "the current release's Job is never deleted, even when older than the cut",
			live:     mig,
			rendered: []string{"migrate-h1"}, // a rollback re-renders h1
			env:      "prod",
			want:     []string{"migrate-h2", "migrate-h3"},
		},
		{
			name:     "a running Job is never touched, however old, and does not count toward keep",
			live:     append([]map[string]any{jobDoc(jobNS, "migrate-h0", "migrate", "running", 0, nil)}, mig...),
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name:     "a Job with no terminal condition yet (just created) is left alone",
			live:     append([]map[string]any{jobDoc(jobNS, "migrate-h0", "migrate", "pending", 0, nil)}, mig...),
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name: "failed Jobs are finished: they are kept for debugging, then pruned like any other",
			live: []map[string]any{
				jobDoc(jobNS, "migrate-h1", "migrate", "failed", 1, nil),
				jobDoc(jobNS, "migrate-h2", "migrate", "complete", 2, nil),
				jobDoc(jobNS, "migrate-h3", "migrate", "complete", 3, nil),
				jobDoc(jobNS, "migrate-h4", "migrate", "complete", 4, nil),
			},
			rendered: []string{"migrate-h4"},
			env:      "prod",
			want:     []string{"migrate-h1"},
		},
		{
			name:     "fewer than keep: nothing to prune",
			live:     mig[:2],
			rendered: []string{"migrate-h2"},
			env:      "prod",
			want:     nil,
		},
		{
			name: "each base name is counted on its own",
			live: append(append([]map[string]any{}, mig[:4]...),
				jobDoc(jobNS, "idp-a", "idp", "complete", 1, nil),
				jobDoc(jobNS, "idp-b", "idp", "complete", 2, nil),
			),
			rendered: []string{"migrate-h4", "idp-b"},
			env:      "prod",
			want:     []string{"migrate-h1"},
		},
		{
			name: "a base the render no longer defines is left alone (a --target subset or a conditional that went false proves nothing)",
			live: append(append([]map[string]any{}, mig...),
				jobDoc(jobNS, "gone-a", "gone", "complete", 1, nil),
				jobDoc(jobNS, "gone-b", "gone", "complete", 2, nil),
				jobDoc(jobNS, "gone-c", "gone", "complete", 3, nil),
				jobDoc(jobNS, "gone-d", "gone", "complete", 4, nil),
			),
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name: "not forge's: no managed-by label, or no forge.dev/job-name handle (a CronJob's child, a chart's Job)",
			live: append(append([]map[string]any{}, mig...),
				jobDoc(jobNS, "cloudsql-backup-prune-1", "", "complete", 1, nil),
				jobDoc(jobNS, "cloudsql-backup-prune-2", "", "complete", 2, nil),
				jobDoc(jobNS, "cloudsql-backup-prune-3", "", "complete", 3, nil),
				jobDoc(jobNS, "cloudsql-backup-prune-4", "", "complete", 4, nil),
				func() map[string]any {
					d := jobDoc(jobNS, "migrate-hx", "migrate", "complete", 0, nil)
					delete(d["metadata"].(map[string]any)["labels"].(map[string]any), AppManagedByLabel)
					return d
				}(),
			),
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name: "another namespace is never pruned by this apply",
			live: append(append([]map[string]any{}, mig...),
				jobDoc("other", "migrate-o1", "migrate", "complete", 1, nil),
				jobDoc("other", "migrate-o2", "migrate", "complete", 2, nil),
				jobDoc("other", "migrate-o3", "migrate", "complete", 3, nil),
				jobDoc("other", "migrate-o4", "migrate", "complete", 4, nil),
			),
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name: "another env's Jobs (forge.dev/env differs) are never pruned",
			live: append(append([]map[string]any{}, mig...),
				jobDoc(jobNS, "migrate-s1", "migrate", "complete", 0, map[string]string{"forge.dev/env": "staging"}),
			),
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name:     "a Job already being deleted is not deleted again",
			live:     append([]map[string]any{withDeletion(jobDoc(jobNS, "migrate-h0", "migrate", "complete", 0, nil))}, mig...),
			rendered: []string{"migrate-h6"},
			env:      "prod",
			want:     []string{"migrate-h1", "migrate-h2", "migrate-h3"},
		},
		{
			name:     "exactly three finished: all are kept",
			live:     mig[:3],
			rendered: []string{"migrate-h3"},
			env:      "prod",
			want:     nil,
		},
		{
			name:     "a fourth finished Job is the first one pruned",
			live:     mig[:4],
			rendered: []string{"migrate-h4"},
			env:      "prod",
			want:     []string{"migrate-h1"},
		},
		{
			name:     "equal timestamps are broken by name, so the choice is stable",
			live:     []map[string]any{jobDoc(jobNS, "migrate-a", "migrate", "complete", 1, nil), jobDoc(jobNS, "migrate-b", "migrate", "complete", 1, nil), jobDoc(jobNS, "migrate-c", "migrate", "complete", 1, nil), jobDoc(jobNS, "migrate-d", "migrate", "complete", 1, nil)},
			rendered: []string{"migrate-d"},
			env:      "prod",
			want:     []string{"migrate-a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := supersededJobs(asLiveJobs(t, tc.live...), renderedOneShotJobs(jobRender(tc.rendered...), jobNS), jobNS, tc.env)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("supersededJobs = %v, want %v", got, tc.want)
			}
		})
	}
}

func withDeletion(d map[string]any) map[string]any {
	d["metadata"].(map[string]any)["deletionTimestamp"] = jobEpoch.Format(time.RFC3339)
	return d
}

func TestRenderedOneShotJobs_OnlyForgeHashNamedJobs(t *testing.T) {
	stream := jobRender("migrate-h1") + "\n---\n" + `apiVersion: batch/v1
kind: Job
metadata:
  name: raw-job
  namespace: control-plane-prod
---
apiVersion: batch/v1
kind: Job
metadata:
  name: nons-h2
  labels:
    forge.dev/job-name: nons
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels:
    forge.dev/job-name: not-a-job`
	got := renderedOneShotJobs(stream, "fallback-ns")
	if _, ok := got.names[jobBaseKey(jobNS, "migrate-h1")]; !ok {
		t.Errorf("a forge Job is missing from the rendered names: %v", got.names)
	}
	if _, ok := got.names[jobBaseKey(jobNS, "raw-job")]; ok {
		t.Error("a Job with no forge.dev/job-name handle is not a hash-named forge Job and must not take part")
	}
	if _, ok := got.bases[jobBaseKey("fallback-ns", "nons")]; !ok {
		t.Errorf("a Job naming no namespace is in the apply's namespace: %v", got.bases)
	}
	if len(got.names) != 2 || len(got.bases) != 2 {
		t.Errorf("rendered = %+v, want exactly migrate-h1 and nons-h2", got)
	}
}

// ---- the real Apply, against the stateful fake cluster ----------------------

func seedBacklog(cl *fakeCluster) {
	for i := 1; i <= 6; i++ {
		cl.seed(jobDoc(jobNS, fmt.Sprintf("control-plane-migrate-h%d", i), "control-plane-migrate", "complete", i, nil))
	}
	// A Job still running, older than all of them.
	cl.seed(jobDoc(jobNS, "control-plane-migrate-hrun", "control-plane-migrate", "running", 0, nil))
	// Not forge's: a CronJob's child (no handle) and a Job of an unrelated base.
	for i := 1; i <= 5; i++ {
		cl.seed(jobDoc(jobNS, fmt.Sprintf("cloudsql-backup-prune-%d", i), "", "complete", i, nil))
	}
	for i := 1; i <= 5; i++ {
		cl.seed(jobDoc(jobNS, fmt.Sprintf("retired-job-r%d", i), "retired-job", "complete", i, nil))
	}
	// Another namespace's migrations.
	for i := 1; i <= 5; i++ {
		cl.seed(jobDoc("other-ns", fmt.Sprintf("control-plane-migrate-o%d", i), "control-plane-migrate", "complete", i, nil))
	}
}

func liveJobNames(cl *fakeCluster, ns string) []string {
	var out []string
	for _, o := range cl.state().Objects {
		if k, _ := o["kind"].(string); k != "Job" {
			continue
		}
		md := strMap(o["metadata"])
		if n, _ := md["namespace"].(string); n == ns {
			out = append(out, md["name"].(string))
		}
	}
	return out
}

func jobApplyOpts(targets ...string) ApplyOpts {
	return ApplyOpts{
		Namespace: jobNS,
		Env:       "prod",
		Context:   "k3d-job-prune",
		Targets:   targets,
		Rollout:   RolloutPolicy{Mode: RolloutSkip},
	}
}

// The headline claim, through the real Apply (the wiring, not just the decision): release N+1's apply leaves the newest three finished
// migrations (its own included) and removes the older finished ones, and touches
// nothing that is not a forge one-shot of a base this render defines.
func TestApply_PrunesSupersededJobs_KeepsLastThree(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	seedBacklog(cl)

	// The release being applied carries migration h7 (new) — and, because the
	// fake does not run Jobs, h7 itself is not finished yet, as in a post-rollout Job.
	if err := applyRendered(context.Background(), jobApplyOpts(), jobRender("control-plane-migrate-h7")); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}

	for _, gone := range []string{"control-plane-migrate-h1", "control-plane-migrate-h2", "control-plane-migrate-h3"} {
		if cl.has("Job", jobNS, gone) {
			t.Errorf("%s is superseded and finished, but is still live", gone)
		}
	}
	for _, kept := range []string{
		"control-plane-migrate-h4", "control-plane-migrate-h5", "control-plane-migrate-h6", // the newest three finished
		"control-plane-migrate-h7",   // the release being applied
		"control-plane-migrate-hrun", // running: never touched
		"retired-job-r1",             // base not in this render
		"retired-job-r5",             //
		"cloudsql-backup-prune-1",    // no forge handle
		"cloudsql-backup-prune-5",    //
		"control-plane-migrate-o1",   // another namespace
		"control-plane-migrate-o5",   //
	} {
		if !cl.has("Job", jobNS, kept) && !cl.has("Job", "other-ns", kept) {
			t.Errorf("%s was deleted, and must not be", kept)
		}
	}
	wantDeletes := []string{
		"job control-plane-migrate-h1 " + jobNS,
		"job control-plane-migrate-h2 " + jobNS,
		"job control-plane-migrate-h3 " + jobNS,
	}
	if got := cl.deletes(); !reflect.DeepEqual(got, wantDeletes) {
		t.Errorf("deletes = %v, want exactly %v", got, wantDeletes)
	}

	// Idempotent: the next apply of the same render has nothing left to prune.
	if err := applyRendered(context.Background(), jobApplyOpts(), jobRender("control-plane-migrate-h7")); err != nil {
		t.Fatalf("second applyRendered: %v", err)
	}
	if got := cl.deletes(); len(got) != len(wantDeletes) {
		t.Errorf("a second apply deleted more: %v", got)
	}
}

// A rollback re-renders an older release. Its Job is referenced by the render
// being applied, so it survives even though it is outside the newest three.
func TestPruneSupersededJobs_NeverDeletesAJobTheRenderStillReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	seedBacklog(cl)

	deleted, err := PruneSupersededJobs(context.Background(), "k3d-job-prune", jobRender("control-plane-migrate-h1"), jobNS, "prod")
	if err != nil {
		t.Fatalf("PruneSupersededJobs: %v", err)
	}
	if !cl.has("Job", jobNS, "control-plane-migrate-h1") {
		t.Error("control-plane-migrate-h1 is in the render being applied and must survive")
	}
	if want := []string{"control-plane-migrate-h2", "control-plane-migrate-h3"}; !reflect.DeepEqual(deleted, want) {
		t.Errorf("deleted = %v, want %v (live: %v)", deleted, want, liveJobNames(cl, jobNS))
	}
}

// A --target apply renders a subset of the env, so what it does not mention
// proves nothing: it prunes no Job.
func TestApply_TargetedApplyPrunesNoJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	seedBacklog(cl)
	stream := jobRender("control-plane-migrate-h7")
	stream = strings.Replace(stream, "forge.dev/spec-hash: h7", "forge.dev/spec-hash: h7\n    app.kubernetes.io/name: control-plane-migrate", 1)

	if err := applyRendered(context.Background(), jobApplyOpts("control-plane-migrate"), stream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	for _, d := range cl.deletes() {
		if strings.HasPrefix(d, "job ") {
			t.Errorf("a --target apply deleted %q; it must prune no Job", d)
		}
	}
	if !cl.has("Job", jobNS, "control-plane-migrate-h7") {
		t.Fatal("the targeted apply did not apply the Job at all; the test proves nothing")
	}
}

// A render with no hash-named Job at all (an env with no migrations) never even
// lists Jobs, so a cluster whose Jobs this apply knows nothing about is untouched.
func TestPruneSupersededJobs_RenderWithoutJobsPrunesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	seedBacklog(cl)
	stream := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\n  namespace: " + jobNS + "\ndata:\n  a: b"
	deleted, err := PruneSupersededJobs(context.Background(), "k3d-job-prune", stream, jobNS, "prod")
	if err != nil || len(deleted) != 0 {
		t.Errorf("deleted %v (err %v) from a render that defines no Job", deleted, err)
	}
	if got := cl.deletes(); len(got) != 0 {
		t.Errorf("kubectl deleted %v", got)
	}
}
