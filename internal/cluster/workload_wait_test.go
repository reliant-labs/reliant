package cluster

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func ptr(i int64) *int64 { return &i }

// sts is a StatefulSet status at generation 2, observed, with every count at
// want: rolled out. Each case below breaks one thing.
func sts(want int64, mutate func(*workloadStatus)) workloadStatus {
	var s workloadStatus
	s.Kind = "StatefulSet"
	s.Metadata.Generation = 2
	s.Spec.Replicas = ptr(want)
	s.Spec.UpdateStrategy.Type = "RollingUpdate"
	s.Status.ObservedGeneration = 2
	s.Status.Replicas, s.Status.ReadyReplicas = want, want
	s.Status.CurrentReplicas, s.Status.UpdatedReplicas = want, want
	s.Status.CurrentRevision, s.Status.UpdateRevision = "db-2", "db-2"
	if mutate != nil {
		mutate(&s)
	}
	return s
}

func ds(want int64, mutate func(*workloadStatus)) workloadStatus {
	var s workloadStatus
	s.Kind = "DaemonSet"
	s.Metadata.Generation = 4
	s.Spec.UpdateStrategy.Type = "RollingUpdate"
	s.Status.ObservedGeneration = 4
	s.Status.DesiredNumberScheduled = ptr(want)
	s.Status.CurrentNumberScheduled, s.Status.UpdatedNumberScheduled = want, want
	s.Status.NumberAvailable, s.Status.NumberReady = want, want
	if mutate != nil {
		mutate(&s)
	}
	return s
}

// The verdict is kstatus's, read from the controller's status alone.
func TestWorkloadReady(t *testing.T) {
	for _, tc := range []struct {
		name  string
		s     workloadStatus
		ready bool
	}{
		{"statefulset rolled out", sts(3, nil), true},
		{"statefulset generation not observed", sts(3, func(s *workloadStatus) { s.Status.ObservedGeneration = 1 }), false},
		{"statefulset short of replicas", sts(3, func(s *workloadStatus) { s.Status.Replicas = 2 }), false},
		{"statefulset short of ready", sts(3, func(s *workloadStatus) { s.Status.ReadyReplicas = 2 }), false},
		{"statefulset scaling down", sts(3, func(s *workloadStatus) { s.Status.Replicas = 4 }), false},
		{"statefulset revision still rolling", sts(3, func(s *workloadStatus) { s.Status.UpdateRevision = "db-3" }), false},
		{"statefulset current short", sts(3, func(s *workloadStatus) { s.Status.CurrentReplicas = 1 }), false},
		{"statefulset replicas unset means one", sts(1, func(s *workloadStatus) { s.Spec.Replicas = nil }), true},
		{"statefulset partition updated", sts(3, func(s *workloadStatus) {
			s.Spec.UpdateStrategy.RollingUpdate = &struct {
				Partition *int64 `json:"partition"`
			}{Partition: ptr(2)}
			s.Status.UpdatedReplicas, s.Status.UpdateRevision = 1, "db-3"
		}), true},
		{"statefulset partition not yet updated", sts(3, func(s *workloadStatus) {
			s.Spec.UpdateStrategy.RollingUpdate = &struct {
				Partition *int64 `json:"partition"`
			}{Partition: ptr(2)}
			s.Status.UpdatedReplicas, s.Status.UpdateRevision = 0, "db-3"
		}), false},
		{"statefulset partition at or above replicas needs no update", sts(0, func(s *workloadStatus) {
			s.Spec.UpdateStrategy.RollingUpdate = &struct {
				Partition *int64 `json:"partition"`
			}{Partition: ptr(2)}
			s.Status.UpdatedReplicas, s.Status.UpdateRevision = 0, "db-3"
		}), true},
		{"statefulset on OnDelete is current once observed", sts(3, func(s *workloadStatus) {
			s.Spec.UpdateStrategy.Type = "OnDelete"
			s.Status.UpdateRevision, s.Status.ReadyReplicas = "db-3", 0
		}), true},
		{"daemonset rolled out", ds(5, nil), true},
		{"daemonset desired not computed", ds(5, func(s *workloadStatus) { s.Status.DesiredNumberScheduled = nil }), false},
		{"daemonset zero nodes is rolled out", ds(0, nil), true},
		{"daemonset short of scheduled", ds(5, func(s *workloadStatus) { s.Status.CurrentNumberScheduled = 4 }), false},
		{"daemonset short of updated", ds(5, func(s *workloadStatus) { s.Status.UpdatedNumberScheduled = 4 }), false},
		{"daemonset on OnDelete does not wait for updated", ds(5, func(s *workloadStatus) {
			s.Spec.UpdateStrategy.Type = "OnDelete"
			s.Status.UpdatedNumberScheduled = 0
		}), true},
		{"daemonset short of available", ds(5, func(s *workloadStatus) { s.Status.NumberAvailable = 4 }), false},
		{"daemonset short of ready", ds(5, func(s *workloadStatus) { s.Status.NumberReady = 4 }), false},
		{"daemonset generation not observed", ds(5, func(s *workloadStatus) { s.Status.ObservedGeneration = 3 }), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := workloadReady(tc.s)
			if (msg == "") != tc.ready {
				t.Fatalf("workloadReady = %q, want ready=%v", msg, tc.ready)
			}
		})
	}
}

// The stream-derived wait set: StatefulSets and DaemonSets anywhere, and the
// Deployments the apply namespace's list cannot see.
func TestStreamWorkloads(t *testing.T) {
	doc := func(kind, ns, name string) string {
		out := "apiVersion: apps/v1\nkind: " + kind + "\nmetadata:\n  name: " + name + "\n"
		if ns != "" {
			out += "  namespace: " + ns + "\n"
		}
		return out
	}
	stream := strings.Join([]string{
		doc("Deployment", "app", "api"),            // listed from the cluster
		doc("Deployment", "", "worker"),            // defaults to app: listed
		doc("Deployment", "observability", "otel"), // another namespace
		doc("StatefulSet", "", "db"),               // defaults to app
		doc("DaemonSet", "kube-system", "prepull"), // cluster agent
		doc("StatefulSet", "app", "db"),            // duplicate
		doc("ConfigMap", "app", "settings"),        // not a workload
		doc("Job", "app", "migrate-abc"),           // awaited as a Job
	}, docDelimiter)
	got := streamWorkloads(stream, "app")
	want := []workloadRef{
		{Kind: "Deployment", Namespace: "observability", Name: "otel"},
		{Kind: "StatefulSet", Namespace: "app", Name: "db"},
		{Kind: "DaemonSet", Namespace: "kube-system", Name: "prepull"},
	}
	if len(got) != len(want) {
		t.Fatalf("streamWorkloads = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("streamWorkloads = %+v, want %+v", got, want)
		}
	}
}

// fakeWorkloadKubectl is a fake `kubectl` that applies anything, lists the
// Deployments applied into the apply namespace, answers `rollout status`
// ready, and answers `get <kind> <name> -n <ns> -o json` from
// <dir>/<kind>-<ns>-<name>.json (absent: a NotFound error).
func fakeWorkloadKubectl(t *testing.T) (dir string, calls func() []string) {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir = t.TempDir()
	script := `#!/bin/sh
state='` + dir + `'
if [ "$1" = "--context" ]; then shift 2; fi
printf '%s\n' "$*" >> "$state/calls.log"
case "$1" in
  apply)
    awk '
      function emit() { if (k != "" && n != "") print k "/" n " serverside-applied"; k = ""; n = ""; m = 0 }
      /^---$/ { emit(); next }
      /^kind:/ { k = tolower($2) }
      /^metadata:/ { m = 1; next }
      /^[^ ]/ { m = 0 }
      m && $1 == "name:" && n == "" { n = $2 }
      END { emit() }'
    exit 0 ;;
  rollout)
    exit 0 ;;
  get)
    if [ "$2" = "deployments" ]; then echo api; exit 0; fi
    f="$state/$2-$5-$3.json"
    if [ -f "$f" ]; then cat "$f"; exit 0; fi
    echo "Error from server (NotFound): $2 \"$3\" not found" >&2
    exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, func() []string {
		data, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func writeWorkloadStatus(t *testing.T, dir, ns, name string, s workloadStatus) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.ToLower(s.Kind)+"-"+ns+"-"+name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

const workloadStream = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
  namespace: app
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: prepull
  namespace: kube-system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: otel
  namespace: observability`

// The rollout wait judges every StatefulSet, DaemonSet and other-namespace
// Deployment the apply sent, beside the namespace's listed Deployments.
func TestWaitRollouts_AwaitsStatefulSetsDaemonSetsAndOtherNamespaces(t *testing.T) {
	testkit.Slow(t, "drives a fake kubectl through shell subprocesses")
	dir, calls := fakeWorkloadKubectl(t)
	writeWorkloadStatus(t, dir, "app", "db", sts(3, nil))
	writeWorkloadStatus(t, dir, "kube-system", "prepull", ds(5, nil))

	var observed []string
	p, err := applyRenderedNoWait(context.Background(), ApplyOpts{
		Namespace: "app", Context: "k3d-alpha", Rollout: RolloutPolicy{Timeout: 5 * time.Second},
		OnRollout: func(o RolloutObservation) { observed = append(observed, o.Kind+"/"+o.Name+"="+o.State.String()) },
	}, workloadStream)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := WaitRollouts(context.Background(), p); err != nil {
		t.Fatalf("WaitRollouts: %v", err)
	}
	joined := strings.Join(calls(), "\n")
	for _, want := range []string{
		"rollout status deployment/api -n app",
		"rollout status deployment/otel -n observability",
		"get statefulset db -n app -o json",
		"get daemonset prepull -n kube-system -o json",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the wait never ran %q; calls:\n%s", want, joined)
		}
	}
	got := strings.Join(observed, ",")
	for _, want := range []string{"Deployment/api=ready", "Deployment/otel=ready", "StatefulSet/db=ready", "DaemonSet/prepull=ready"} {
		if !strings.Contains(got, want) {
			t.Errorf("observations %s lack %s", got, want)
		}
	}
}

// A StatefulSet that never rolls out fails the deploy under the default
// policy, and names itself; under warn it is reported and tolerated.
func TestWaitRollouts_UnreadyStatefulSetFailsTheWait(t *testing.T) {
	testkit.Slow(t, "drives a fake kubectl through shell subprocesses")
	prev := workloadPollInterval
	workloadPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { workloadPollInterval = prev })

	dir, _ := fakeWorkloadKubectl(t)
	writeWorkloadStatus(t, dir, "app", "db", sts(3, func(s *workloadStatus) { s.Status.ReadyReplicas = 1 }))
	writeWorkloadStatus(t, dir, "kube-system", "prepull", ds(5, nil))

	for _, tc := range []struct {
		mode    RolloutMode
		wantErr bool
	}{{RolloutWait, true}, {RolloutWarn, false}} {
		var states []string
		p, err := applyRenderedNoWait(context.Background(), ApplyOpts{
			Namespace: "app", Context: "k3d-alpha", Rollout: RolloutPolicy{Mode: tc.mode, Timeout: 300 * time.Millisecond},
			OnRollout: func(o RolloutObservation) {
				if o.Kind == "StatefulSet" {
					states = append(states, o.State.String())
				}
			},
		}, workloadStream)
		if err != nil {
			t.Fatalf("%s apply: %v", tc.mode, err)
		}
		err = WaitRollouts(context.Background(), p)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: WaitRollouts err = %v, want error=%v", tc.mode, err, tc.wantErr)
		}
		if tc.wantErr && !strings.Contains(err.Error(), "statefulset/db") {
			t.Errorf("%s: the failure does not name the StatefulSet: %v", tc.mode, err)
		}
		if strings.Join(states, ",") != "timed_out" {
			t.Errorf("%s: StatefulSet observations %v, want [timed_out]", tc.mode, states)
		}
	}
}
