package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// The rollout wait for the workload kinds `kubectl get deployments -n <ns>`
// cannot see: every StatefulSet and DaemonSet an apply sent, and every
// Deployment it sent into a namespace other than the apply's own.
//
// WHY THESE ARE AWAITED NOW. The wait used to cover the apply namespace's
// forge-managed Deployments and the one-shot Jobs, and nothing else. A
// StatefulSet stuck on a bad image, a DaemonSet that never scheduled, or a
// Deployment in a second namespace (an observability collector beside the
// app) shipped a green deploy. That was survivable while nothing read the
// deploy's verdict as a fact about the cluster. It is not now: the deploy
// records it on the promotion as the `cluster-rollout` gate, and a control
// plane holds a hosted part's activation until that gate passed. The verdict
// has to cover every kind the hosted part can depend on, or the gate passes
// over a predecessor that never rolled out.
//
// JUDGED BY STATUS, NOT BY PODS. Each object is graded the way kstatus
// (sigs.k8s.io/cli-utils) and Flux's health checks grade it: the controller's
// own status counts against its spec, at the generation it last observed.
// Nothing reads a Pod, so an admission webhook that rewrites a Pod spec, an
// evicted Pod waiting for garbage collection, or a terminated one-off Pod
// cannot hold a rollout that its controller reports complete.

// AwaitedKinds are the workload kinds forge's rollout wait judges, in the
// order a reader would list them. The deploy names them on the gate it records,
// so a reader of the gate knows what "rolled out" covered.
var AwaitedKinds = []string{"Deployment", "StatefulSet", "DaemonSet", "Job"}

// workloadRef is one workload the stream-derived wait covers.
type workloadRef struct {
	Kind      string
	Namespace string
	Name      string
}

// streamWorkloads is the stream-derived half of the wait set: every
// StatefulSet and DaemonSet in manifests, and every Deployment whose namespace
// is not namespace (the apply namespace's Deployments are listed from the
// cluster by ListManagedDeployments, as they always were). A document with no
// namespace lands in namespace. De-duplicated, in render order.
func streamWorkloads(manifests, namespace string) []workloadRef {
	seen := map[workloadRef]bool{}
	var out []workloadRef
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok || m.Metadata.Name == "" {
			continue
		}
		ns := m.Metadata.Namespace
		if ns == "" {
			ns = namespace
		}
		switch m.Kind {
		case "StatefulSet", "DaemonSet":
		case "Deployment":
			if ns == namespace {
				continue
			}
		default:
			continue
		}
		ref := workloadRef{Kind: m.Kind, Namespace: ns, Name: m.Metadata.Name}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// workloadPollInterval is how often a StatefulSet or DaemonSet's status is
// re-read while it converges. A var so a test can shrink it.
var workloadPollInterval = 2 * time.Second

// workloadStatus is the part of a StatefulSet or DaemonSet the readiness
// verdict reads.
type workloadStatus struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Generation int64 `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas       *int64 `json:"replicas"`
		UpdateStrategy struct {
			Type          string `json:"type"`
			RollingUpdate *struct {
				Partition *int64 `json:"partition"`
			} `json:"rollingUpdate"`
		} `json:"updateStrategy"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration int64  `json:"observedGeneration"`
		Replicas           int64  `json:"replicas"`
		ReadyReplicas      int64  `json:"readyReplicas"`
		CurrentReplicas    int64  `json:"currentReplicas"`
		UpdatedReplicas    int64  `json:"updatedReplicas"`
		CurrentRevision    string `json:"currentRevision"`
		UpdateRevision     string `json:"updateRevision"`
		// DaemonSet. Desired is a pointer: absent means the controller has
		// not computed it yet, which is not "zero nodes".
		DesiredNumberScheduled *int64 `json:"desiredNumberScheduled"`
		CurrentNumberScheduled int64  `json:"currentNumberScheduled"`
		UpdatedNumberScheduled int64  `json:"updatedNumberScheduled"`
		NumberAvailable        int64  `json:"numberAvailable"`
		NumberReady            int64  `json:"numberReady"`
	} `json:"status"`
}

// workloadReady grades one StatefulSet or DaemonSet: "" when it has rolled
// out, else what it is still waiting on. kstatus's rules, with one difference
// for a DaemonSet on the OnDelete strategy (kstatus would wait on updated Pods
// that only a person deleting the old ones ever creates; forge waits on
// scheduled, available and ready, as kstatus already does for a StatefulSet on
// OnDelete by not waiting at all).
func workloadReady(s workloadStatus) string {
	if s.Status.ObservedGeneration < s.Metadata.Generation {
		return fmt.Sprintf("controller has observed generation %d of %d", s.Status.ObservedGeneration, s.Metadata.Generation)
	}
	switch s.Kind {
	case "StatefulSet":
		return statefulSetReady(s)
	case "DaemonSet":
		return daemonSetReady(s)
	default:
		return fmt.Sprintf("unsupported kind %q", s.Kind)
	}
}

func statefulSetReady(s workloadStatus) string {
	if s.Spec.UpdateStrategy.Type == "OnDelete" {
		// kstatus: a StatefulSet whose Pods a person replaces is current as
		// soon as its controller has observed the spec.
		return ""
	}
	want := int64(1)
	if s.Spec.Replicas != nil {
		want = *s.Spec.Replicas
	}
	st := s.Status
	switch {
	case st.Replicas < want:
		return fmt.Sprintf("replicas %d/%d", st.Replicas, want)
	case st.ReadyReplicas < want:
		return fmt.Sprintf("ready %d/%d", st.ReadyReplicas, want)
	case st.Replicas > want:
		return fmt.Sprintf("%d replicas pending termination", st.Replicas-want)
	}
	if ru := s.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil && *ru.Partition > 0 {
		// A partitioned rollout updates only the ordinals at or above the
		// partition, and is complete when those are updated.
		// A partition at or above the replica count updates no ordinal.
		need := max(want-*ru.Partition, 0)
		if st.UpdatedReplicas < need {
			return fmt.Sprintf("updated %d/%d (partition %d)", st.UpdatedReplicas, need, *ru.Partition)
		}
		return ""
	}
	if st.CurrentReplicas < want {
		return fmt.Sprintf("current %d/%d", st.CurrentReplicas, want)
	}
	if st.CurrentRevision != st.UpdateRevision {
		return fmt.Sprintf("revision %s is still rolling out", st.UpdateRevision)
	}
	return ""
}

func daemonSetReady(s workloadStatus) string {
	st := s.Status
	if st.DesiredNumberScheduled == nil {
		return "the controller has not computed the desired number of Pods"
	}
	want := *st.DesiredNumberScheduled
	switch {
	case st.CurrentNumberScheduled < want:
		return fmt.Sprintf("scheduled %d/%d", st.CurrentNumberScheduled, want)
	case s.Spec.UpdateStrategy.Type != "OnDelete" && st.UpdatedNumberScheduled < want:
		return fmt.Sprintf("updated %d/%d", st.UpdatedNumberScheduled, want)
	case st.NumberAvailable < want:
		return fmt.Sprintf("available %d/%d", st.NumberAvailable, want)
	case st.NumberReady < want:
		return fmt.Sprintf("ready %d/%d", st.NumberReady, want)
	}
	return ""
}

// WaitWorkloadObserved waits until a StatefulSet or DaemonSet has rolled out
// (workloadReady), polling its status, and grades the outcome the way
// WaitRolloutObserved grades a Deployment's: ready, timed out (the budget
// expired with the object still converging, or never readable), or failed
// (the object could not be judged at all).
func WaitWorkloadObserved(ctx context.Context, kctx, kind, name, namespace string, timeout time.Duration) (RolloutState, error) {
	if timeout <= 0 {
		timeout = DefaultRolloutTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := "not read yet"
	for {
		out, err := kubectlCmd(wctx, kctx, "get", strings.ToLower(kind), name, "-n", namespace, "-o", "json").Output()
		switch {
		case err != nil && wctx.Err() != nil:
			// The budget expired mid-read and killed kubectl: the last thing
			// actually observed is the more useful report.
		case err != nil:
			last = "read failed: " + oneLineErr(err)
		default:
			var s workloadStatus
			if jerr := json.Unmarshal(out, &s); jerr != nil {
				last = "unreadable status: " + jerr.Error()
				break
			}
			if s.Kind != kind {
				return RolloutStateFailed, fmt.Errorf("%s/%s: the cluster returned a %q", kind, name, s.Kind)
			}
			msg := workloadReady(s)
			if msg == "" {
				return RolloutStateReady, nil
			}
			last = msg
		}
		select {
		case <-wctx.Done():
			err := fmt.Errorf("%s/%s: timed out waiting for the condition after %s: %s", kind, name, timeout, last)
			if errors.Is(ctx.Err(), context.Canceled) {
				err = fmt.Errorf("%s/%s: %w: %s", kind, name, ctx.Err(), last)
			}
			return RolloutStateTimedOut, err
		case <-time.After(workloadPollInterval):
		}
	}
}

// oneLineErr is err's text with an *exec.ExitError's stderr folded in, on one
// line, for a progress message.
func oneLineErr(err error) string {
	msg := err.Error()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		msg += ": " + string(exit.Stderr)
	}
	return strings.Join(strings.Fields(msg), " ")
}
