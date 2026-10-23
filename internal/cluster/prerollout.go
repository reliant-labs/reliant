package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The pre-rollout gate: one-shot Jobs that must COMPLETE before any workload
// in the same apply is touched.
//
// WHY THIS EXISTS
// ===============
// Apply used to send the migrate Job and every Deployment in ONE server-side
// apply, then wait for the rollouts, and only then for the Job. So the new
// code rolled out first and the schema moved second. A control-plane prod
// deploy log shows it plainly — `deployment.apps/admin-server
// serverside-applied` … `job.batch/control-plane-migrate-…
// serverside-applied` → `Waiting for rollouts...` → `…migrate…: complete`,
// about two minutes later — and for those two minutes every new pod served
// requests against a schema it did not have. A migration that FAILED was
// worse: the workloads had already moved, and the verdict arrived after the
// damage.
//
// Helm (`helm.sh/hook: pre-upgrade`) and Argo CD (`argocd.argoproj.io/hook:
// PreSync`) both answer this the same way: an explicit phase that runs, and
// must succeed, before the release's workloads change. This is forge's.
//
// THE MARKER
// ==========
// A one-shot Job declares its phase with the DeployPhaseAnnotation on its
// metadata. forge's KCL emits it from the declarative
// `forge.CronJob.deploy_phase` (and `forge.workloads.Workload.deploy_phase`),
// so a project states intent in KCL, and Go reads it from the stream it is
// about to apply — the one source that also sees a raw `kind: Job` a project
// placed in `additional_manifests`.
//
// It lives on the Job's metadata, NOT its pod template: the Job's name
// carries a hash of the pod spec, and declaring a phase must not rename (and
// therefore re-run) a Job that already completed.
//
// THE DEFAULT IS PRE-ROLLOUT, and an absent annotation means the same thing.
// The two ways to get it wrong are not symmetric:
//
//   - A Job wrongly held as pre-rollout fails LOUD and SAFE. The deploy
//     aborts before any workload changes, the previous release keeps
//     serving, and the error names the Job and the command that shows why.
//   - A Job wrongly let through as post-rollout fails SILENT and UNSAFE. New
//     code serves against the old schema, which is the incident above.
//
// The dominant deploy-time one-shot is a migration or backfill, whose whole
// purpose is to precede the code that needs it. The exception — a Job that
// needs THIS release's workloads to be serving (a provisioner that configures
// an in-stream identity provider) — declares `deploy_phase = "post-rollout"`
// in one line.
//
// SCOPE
// =====
// The gate orders ONE Apply, which is one cluster group's stream. Platform
// dependencies (helm-rendered charts) are applied before it, deliberately:
// they are the substrate a migration may itself need. A workload the Job
// depends on must not ride the same stream as a pre-rollout Job — the gate
// holds it back, the Job cannot reach it, and the deploy aborts naming the
// Job. That abort is the correct outcome; the fix is to declare the Job
// post-rollout or to move the dependency out of the release's own stream.
//
// The gate holds in every rollout mode. `--rollout=skip` means "do not wait
// for the cluster to converge"; it never meant "ship code ahead of its
// migration", and `--rollout=warn` tolerates a half-rolled cluster only
// because the previous Deployment is still serving — which is exactly what
// aborting at the gate preserves.

// The phase vocabulary and the classifier live in pkg/release
// ([release.ClassifyPhase]), because a Flux-applied bundle expresses the same
// phases as trees and the two appliers must not classify differently.
const (
	// DeployPhaseAnnotation is the Job metadata annotation that places a
	// one-shot Job relative to the workloads in the same apply.
	DeployPhaseAnnotation = release.AnnotationDeployPhase
	// DeployPhasePreRollout is applied, and required to COMPLETE, before any
	// workload in the same apply is applied. The default.
	DeployPhasePreRollout = release.DeployPhasePreRollout
	// DeployPhasePostRollout is applied with the workloads and awaited after
	// their rollouts, for a Job that needs this release's workloads running.
	DeployPhasePostRollout = release.DeployPhasePostRollout
)

// jobRef names one Job the gate waits on.
type jobRef struct {
	Name      string
	Namespace string
}

// rolloutPhases is the non-config half of an apply stream, split by the gate.
//
// Every document lands in exactly one field, and each field keeps the
// stream's relative order.
type rolloutPhases struct {
	// support is everything that is neither a workload nor a Job: the
	// ServiceAccount a Job's pod runs as, its RBAC, the NetworkPolicy that
	// lets it egress to the database, Services, CRDs and custom resources.
	// It is applied BEFORE the pre-rollout Jobs because a Job's pod needs it.
	support string
	// preJobs is every pre-rollout Job, applied together and awaited before
	// any workload.
	preJobs string
	// preJobRefs names the Jobs in preJobs, in stream order.
	preJobRefs []jobRef
	// workloads is every release.RolloutWorkloadKind document plus the post-rollout
	// Jobs — everything the gate holds back.
	workloads string
}

// gated reports whether the stream carries a pre-rollout Job at all. When it
// does not, Apply sends the stream in the single apply it always has.
func (p rolloutPhases) gated() bool {
	return len(p.preJobRefs) > 0
}

// awaited reports whether a Job was already waited on at the gate, so the
// post-rollout wait does not wait on it twice.
func (p rolloutPhases) awaited(name string) bool {
	for _, j := range p.preJobRefs {
		if j.Name == name {
			return true
		}
	}
	return false
}

// partitionRolloutPhases splits a stream into the gate's three passes.
//
// An unknown DeployPhaseAnnotation value is an ERROR, returned before
// anything is applied. A typo'd "pre-rolout" must not quietly pick a phase:
// read as post-rollout it ships code ahead of its migration, read as
// pre-rollout it silently becomes a gate its author meant to opt out of. The
// author is told which Job and which values are accepted.
//
// A document that does not parse cannot be confirmed as anything, so it goes
// to the held-back workloads pass. That is the one that always runs last, and
// kubectl is the right thing to report on a malformed manifest.
func partitionRolloutPhases(stream string) (rolloutPhases, error) {
	var support, preJobs, workloads []string
	var refs []jobRef
	for _, doc := range splitDocs(stream) {
		m, ok := parseDoc(doc)
		if !ok {
			workloads = append(workloads, doc)
			continue
		}
		phase, err := release.ClassifyPhase(m.Kind, m.Metadata.Name, m.Metadata.Annotations)
		if err != nil {
			return rolloutPhases{}, err
		}
		switch {
		case phase == release.PhasePre && m.Kind == "Job":
			preJobs = append(preJobs, doc)
			refs = append(refs, jobRef{Name: m.Metadata.Name, Namespace: m.Metadata.Namespace})
		case phase == release.PhasePre:
			support = append(support, doc)
		default:
			workloads = append(workloads, doc)
		}
	}
	return rolloutPhases{
		support:    strings.Join(support, docDelimiter),
		preJobs:    strings.Join(preJobs, docDelimiter),
		preJobRefs: refs,
		workloads:  strings.Join(workloads, docDelimiter),
	}, nil
}

// applyPreRolloutGate applies the support objects and the pre-rollout Jobs,
// then waits for every one of those Jobs to complete.
//
// A non-nil return means the caller MUST NOT apply the workloads. Every Job is
// awaited, not just the first to fail, so the operator sees the whole gate in
// one run. They already run concurrently, so waiting on the rest costs the
// slowest Job's time, not the sum.
func applyPreRolloutGate(ctx context.Context, opts ApplyOpts, policy RolloutPolicy, phases rolloutPhases) error {
	if strings.TrimSpace(phases.support) != "" {
		if err := policy.classifyApplyResult(KubectlApply(ctx, opts.Context, phases.support)); err != nil {
			return fmt.Errorf("kubectl apply failed (pre-rollout support objects; no workload was applied): %w", err)
		}
	}
	if !opts.Quiet {
		fmt.Printf("Applying %d pre-rollout Job(s); workloads are held until they complete...\n", len(phases.preJobRefs))
	}
	if err := policy.classifyApplyResult(KubectlApply(ctx, opts.Context, phases.preJobs)); err != nil {
		return fmt.Errorf("kubectl apply failed (pre-rollout Jobs; no workload was applied): %w", err)
	}

	return awaitJobs(ctx, opts, policy, phases.preJobRefs, "pre-rollout")
}

// awaitJobs waits for every Job in refs to complete and reports ALL that did
// not, as one *PreRolloutJobError. kind labels them in the progress lines
// ("pre-rollout", "secret store").
func awaitJobs(ctx context.Context, opts ApplyOpts, policy RolloutPolicy, refs []jobRef, kind string) error {
	gate := &PreRolloutJobError{Context: opts.Context, Kind: kind}
	for _, job := range refs {
		namespace := job.Namespace
		if namespace == "" {
			namespace = opts.Namespace
		}
		fmt.Printf("Waiting for %s Job %q to complete before applying workloads...\n", kind, job.Name)
		state, err := WaitJobCompleteObserved(ctx, opts.Context, job.Name, namespace, policy.Timeout)
		observeRollout(opts, "Job", job.Name, state, err)
		if err != nil {
			fmt.Printf("  FAILED: %s job %s: %v\n", kind, job.Name, err)
			gate.Failures = append(gate.Failures, PreRolloutJobFailure{
				Job: job.Name, Namespace: namespace, State: state, Timeout: policy.Timeout, Err: err,
			})
			continue
		}
		fmt.Printf("  %s: complete\n", job.Name)
	}
	if len(gate.Failures) > 0 {
		return gate
	}
	return nil
}

// PreRolloutJobError is a deploy stopped at the pre-rollout gate: at least one
// pre-rollout Job did not complete, so no workload in the group was applied.
//
// It is a type, not a string, so a caller (and a test) can tell "the gate
// held" apart from every other apply failure without parsing prose.
type PreRolloutJobError struct {
	// Context is the kubectl context the Jobs ran in, threaded into the
	// commands the message prints so they can be pasted as-is.
	Context  string
	Failures []PreRolloutJobFailure
	// Kind names the gate in the message: "pre-rollout" when empty, "secret
	// store" for the store component's own Jobs.
	Kind string
}

// PreRolloutJobFailure is one Job that did not complete.
type PreRolloutJobFailure struct {
	Job       string
	Namespace string
	// State distinguishes a Job that positively failed from one whose
	// budget expired while it was still running.
	State   RolloutState
	Timeout time.Duration
	Err     error
}

// Error names every Job, says what did NOT happen, and gives the exact
// commands to see why and to retry. A migration that failed at 3am is
// diagnosed from this message, so it carries the commands rather than a hint
// to go and find them.
func (e *PreRolloutJobError) Error() string {
	var b strings.Builder
	kind := e.Kind
	if kind == "" {
		kind = "pre-rollout"
	}
	for i, f := range e.Failures {
		if i > 0 {
			b.WriteString("\n")
		}
		verdict := fmt.Sprintf("failed: %v", f.Err)
		if f.State == RolloutStateTimedOut {
			verdict = fmt.Sprintf("did not complete within %s (timed out; raise it with --rollout-timeout)", f.Timeout)
		}
		fmt.Fprintf(&b, "%s Job %q %s\n", kind, f.Job, verdict)
		fmt.Fprintf(&b, "  logs:  kubectl %s\n",
			strings.Join(KubectlArgs(e.Context, "-n", f.Namespace, "logs", "job/"+f.Job, "--all-containers"), " "))
		fmt.Fprintf(&b, "  retry: fix the cause, then `kubectl %s` and re-run the deploy (a Job whose spec is unchanged keeps its name, and a failed one stays failed)",
			strings.Join(KubectlArgs(e.Context, "-n", f.Namespace, "delete", "job", f.Job), " "))
	}
	b.WriteString("\nNO workload in this group was applied; the previous release is still serving.")
	return b.String()
}
