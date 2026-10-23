package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The secret ordering: a store-backed secret's value must be in the cluster
// before the workload that reads it starts, and a pin bump must roll its
// consumers only after the new version has synced.
//
//	CRDs -> ESO -> OpenBao -> stores -> ExternalSecrets -> workloads
//
// WHY THE ORDER IS FORGE'S. Applying everything in one pass is a race with two
// losing outcomes. On a first install a Deployment starts before ESO has written
// the Secret it mounts and sticks on CreateContainerConfigError. On a rotation
// it is worse and silent: the pin moves from 1 to 2, the Deployment rolls at
// once, its new pods read the Secret ESO has not refreshed yet, and the
// "rotated" workload runs the OLD value while every object says it is current.
// ESO refreshes on an interval, not on a write, so nothing in the cluster closes
// the gap on its own.
//
// WHAT RUNS WHEN, in the order applyRenderedNoWait sends it:
//
//  1. ESO's CRDs and controller, from the chart path (applyRenderedCharts:
//     Established, then Available), before this stream is touched;
//  2. the store component's bootstrap stage: its support objects and its own
//     SecretStore and ExternalSecrets, then a wait for those to sync (they
//     deliver the seal key the store cannot start without);
//  3. the component's pre-rollout Jobs (a widen), then its workloads and
//     post-rollout Jobs (the StatefulSet, the converge), each awaited;
//  4. the env's login identity, SecretStores and ExternalSecrets, then a wait
//     for every ExternalSecret to report Ready for THIS apply's spec;
//  5. the release's pre-rollout Jobs and workloads, as before.
//
// A stream with no store-backed secret keeps its historical passes exactly:
// the stages are then empty and none of this runs. The ordering holds in every
// rollout mode, like the pre-rollout gate: `--rollout=skip` means "do not wait
// for the cluster to converge", never "start a workload ahead of its Secret".

// ErrExternalSecretNotSynced is the cause wrapped by a failed ExternalSecret
// gate, for callers that grade it with errors.Is.
var ErrExternalSecretNotSynced = errors.New("ExternalSecret did not sync")

// stageRef names one namespaced object an apply waits on.
type stageRef struct{ Name, Namespace string }

// secretStages is a stream partitioned into the secret ordering. Every document
// lands in exactly one field and keeps the stream's relative order.
type secretStages struct {
	// bootstrap is the store component's support objects and its own
	// SecretStore and ExternalSecrets; bootstrapSyncs are the ExternalSecrets in
	// it, awaited before the component's workloads roll.
	bootstrap     string
	bootstrapSync []stageRef
	// storePre is the component's pre-rollout Jobs.
	storePre     string
	storePreJobs []stageRef
	// store is the component's workloads and post-rollout Jobs, with the
	// objects each wait is made on.
	store            string
	storeDeployments []stageRef
	storeStatefuls   []stageRef
	storeJobs        []stageRef
	// secrets is the env's login identity, SecretStores and ExternalSecrets;
	// secretSyncs are the ExternalSecrets in it that gate the workloads.
	secrets     string
	secretSyncs []stageRef
	// rest is everything else, in order.
	rest string
}

// active reports whether the stream has any store-backed ordering to do.
func (s secretStages) active() bool {
	for _, part := range []string{s.bootstrap, s.storePre, s.store, s.secrets} {
		if strings.TrimSpace(part) != "" {
			return true
		}
	}
	return false
}

// awaitedJobs is every Job the stages complete, so the post-rollout wait does
// not wait on them twice.
func (s secretStages) awaitedJobs() map[string]bool {
	out := map[string]bool{}
	for _, refs := range [][]stageRef{s.storePreJobs, s.storeJobs} {
		for _, r := range refs {
			out[r.Name] = true
		}
	}
	return out
}

// partitionSecretStages splits a stream into the secret stages and the rest. An
// unknown deploy-phase on a store component Job is an error, returned before
// anything is applied.
func partitionSecretStages(stream string) (secretStages, error) {
	var out secretStages
	var bootstrap, storePre, store, secrets, rest []string
	for _, doc := range splitDocs(stream) {
		m, ok := parseDoc(doc)
		if !ok {
			rest = append(rest, doc)
			continue
		}
		stage, err := release.ClassifySecretStage(m.APIVersion, m.Kind, m.Metadata.Name, m.Metadata.Labels, m.Metadata.Annotations)
		if err != nil {
			return secretStages{}, err
		}
		ref := stageRef{Name: m.Metadata.Name, Namespace: m.Metadata.Namespace}
		awaited := m.Kind == "ExternalSecret" && release.SecretSyncAwaited(m.Metadata.Annotations)
		switch stage {
		case release.SecretStageBootstrap:
			bootstrap = append(bootstrap, doc)
			if awaited {
				out.bootstrapSync = append(out.bootstrapSync, ref)
			}
		case release.SecretStageStorePre:
			storePre = append(storePre, doc)
			out.storePreJobs = append(out.storePreJobs, ref)
		case release.SecretStageStore:
			store = append(store, doc)
			switch m.Kind {
			case "Deployment":
				out.storeDeployments = append(out.storeDeployments, ref)
			case "StatefulSet":
				out.storeStatefuls = append(out.storeStatefuls, ref)
			case "Job":
				out.storeJobs = append(out.storeJobs, ref)
			}
		case release.SecretStageSecrets:
			secrets = append(secrets, doc)
			if awaited {
				out.secretSyncs = append(out.secretSyncs, ref)
			}
		default:
			rest = append(rest, doc)
		}
	}
	out.bootstrap = strings.Join(bootstrap, docDelimiter)
	out.storePre = strings.Join(storePre, docDelimiter)
	out.store = strings.Join(store, docDelimiter)
	out.secrets = strings.Join(secrets, docDelimiter)
	out.rest = strings.Join(rest, docDelimiter)
	return out, nil
}

// applySecretStages sends the stages in order, each held until the one before
// it is proven. A non-nil return means the caller MUST NOT apply the workloads.
func applySecretStages(ctx context.Context, opts ApplyOpts, policy RolloutPolicy, st secretStages) error {
	apply := func(what, docs string) error {
		if strings.TrimSpace(docs) == "" {
			return nil
		}
		if err := policy.classifyApplyResult(KubectlApply(ctx, opts.Context, docs)); err != nil {
			return fmt.Errorf("kubectl apply failed (%s; no workload was applied): %w", what, err)
		}
		return nil
	}
	say := func(format string, a ...any) {
		if !opts.Quiet {
			fmt.Printf(format+"\n", a...)
		}
	}

	if strings.TrimSpace(st.bootstrap) != "" {
		say("Applying the secret store's bootstrap objects...")
		if err := apply("secret store bootstrap", st.bootstrap); err != nil {
			return err
		}
		if err := waitExternalSecrets(ctx, opts, policy, st.bootstrapSync, "the secret store starts"); err != nil {
			return err
		}
	}
	if strings.TrimSpace(st.storePre) != "" {
		say("Applying %d secret store pre-rollout Job(s)...", len(st.storePreJobs))
		if err := apply("secret store pre-rollout Jobs", st.storePre); err != nil {
			return err
		}
		if err := awaitJobs(ctx, opts, policy, toJobRefs(st.storePreJobs), "secret store"); err != nil {
			return err
		}
	}
	if strings.TrimSpace(st.store) != "" {
		say("Applying the secret store; secrets and workloads are held until it is ready...")
		if err := apply("secret store", st.store); err != nil {
			return err
		}
		if err := waitStoreWorkloads(ctx, opts, policy, st); err != nil {
			return err
		}
		if err := awaitJobs(ctx, opts, policy, toJobRefs(st.storeJobs), "secret store"); err != nil {
			return err
		}
	}
	if strings.TrimSpace(st.secrets) != "" {
		say("Applying SecretStores and ExternalSecrets; workloads are held until they sync...")
		if err := apply("SecretStores and ExternalSecrets", st.secrets); err != nil {
			return err
		}
		if err := waitExternalSecrets(ctx, opts, policy, st.secretSyncs, "workloads are applied"); err != nil {
			return err
		}
	}
	return nil
}

func toJobRefs(refs []stageRef) []jobRef {
	out := make([]jobRef, len(refs))
	for i, r := range refs {
		out[i] = jobRef(r)
	}
	return out
}

// waitStoreWorkloads waits for the store component's Deployments and
// StatefulSets, naming every one that did not become ready.
func waitStoreWorkloads(ctx context.Context, opts ApplyOpts, policy RolloutPolicy, st secretStages) error {
	timeout := policy.Timeout
	if timeout <= 0 {
		timeout = DefaultRolloutTimeout
	}
	var failed []string
	for _, kind := range []struct {
		resource, label string
		refs            []stageRef
	}{
		{"deployment", "Deployment", st.storeDeployments},
		{"statefulset", "StatefulSet", st.storeStatefuls},
	} {
		for _, ref := range kind.refs {
			ns := ref.Namespace
			if ns == "" {
				ns = opts.Namespace
			}
			fmt.Printf("Waiting for secret store %s %q to be ready...\n", kind.label, ref.Name)
			var captured bytes.Buffer
			cmd := kubectlCmd(ctx, opts.Context, "rollout", "status", kind.resource+"/"+ref.Name, "-n", ns, "--timeout="+timeout.String())
			cmd.Stdout = io.MultiWriter(os.Stdout, &captured)
			cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
			err := cmd.Run()
			state := classifyWaitError(err, captured.String())
			observeRollout(opts, kind.label, ref.Name, state, err)
			if err != nil {
				fmt.Printf("  FAILED: rollout for %s: %v\n", ref.Name, err)
				failed = append(failed, kind.label+"/"+ref.Name)
				continue
			}
			fmt.Printf("  %s: ready\n", ref.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("the secret store did not become ready (%s); no ExternalSecret or workload was applied", strings.Join(failed, ", "))
	}
	return nil
}

// secretSyncPoll is how often the ExternalSecret wait re-reads the object. A
// var so a test shrinks it.
var secretSyncPoll = 2 * time.Second

// externalSecretView is the part of an ExternalSecret the wait reads.
type externalSecretView struct {
	Metadata struct {
		Generation int64 `json:"generation"`
	} `json:"metadata"`
	Status struct {
		SyncedResourceVersion string `json:"syncedResourceVersion"`
		Conditions            []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

// ready reports whether the ExternalSecret has synced THIS spec, and otherwise
// why not, in the operator's terms.
//
// Ready=True alone is not enough: ESO keeps the condition from the previous
// sync while it reconciles a changed spec, so a pin bump would pass the wait on
// the old sync. ESO records which spec it synced in
// status.syncedResourceVersion ("<generation>-<hash of labels and
// annotations>"), written only when a sync completes, so the spec is current
// when that carries the object's generation. That field is ESO's own contract
// (ExternalSecretStatus.SyncedResourceVersion) and is set on every 2.x release.
func (v externalSecretView) ready() (bool, string) {
	for _, c := range v.Status.Conditions {
		if c.Type != "Ready" {
			continue
		}
		if c.Status != "True" {
			why := c.Reason
			if c.Message != "" {
				why += ": " + c.Message
			}
			return false, why
		}
		want := fmt.Sprintf("%d-", v.Metadata.Generation)
		if !strings.HasPrefix(v.Status.SyncedResourceVersion, want) {
			return false, fmt.Sprintf("Ready, but for an older spec (synced %q, spec generation %d): ESO has not yet synced this apply",
				v.Status.SyncedResourceVersion, v.Metadata.Generation)
		}
		return true, ""
	}
	return false, "no Ready condition yet (ESO has not reconciled it)"
}

// getExternalSecret reads one ExternalSecret.
func getExternalSecret(ctx context.Context, kctx string, ref stageRef) (externalSecretView, error) {
	var v externalSecretView
	cmd := kubectlCmd(ctx, kctx, "get", "externalsecret.external-secrets.io", ref.Name, "-n", ref.Namespace, "-o", "json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return v, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return v, fmt.Errorf("read ExternalSecret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	return v, nil
}

// secretSyncFailure is one ExternalSecret that did not sync.
type secretSyncFailure struct {
	Ref    stageRef
	Reason string
	// State separates an ExternalSecret ESO refused from a budget that expired
	// with ESO still trying.
	State RolloutState
}

// ExternalSecretSyncError is a deploy stopped at the ExternalSecret gate: at
// least one ExternalSecret did not sync, so what comes after it was not
// applied. It is a type so a caller (and a test) can tell "the gate held" from
// every other apply failure without parsing prose.
type ExternalSecretSyncError struct {
	Context  string
	Failures []secretSyncFailure
	// Held names what was withheld, for the closing line.
	Held string
}

func (e *ExternalSecretSyncError) Error() string {
	var b strings.Builder
	for i, f := range e.Failures {
		if i > 0 {
			b.WriteString("\n")
		}
		verdict := "has not synced"
		if f.State == RolloutStateTimedOut {
			verdict = "had not synced when the wait expired"
		}
		fmt.Fprintf(&b, "ExternalSecret %s/%s %s: %s\n", f.Ref.Namespace, f.Ref.Name, verdict, f.Reason)
		fmt.Fprintf(&b, "  why:    kubectl %s\n", strings.Join(KubectlArgs(e.Context, "-n", f.Ref.Namespace, "describe", "externalsecret", f.Ref.Name), " "))
		fmt.Fprintf(&b, "  stores: kubectl %s", strings.Join(KubectlArgs(e.Context, "-n", f.Ref.Namespace, "get", "secretstore"), " "))
	}
	fmt.Fprintf(&b, "\nNO %s; the previous release is still serving.", e.Held)
	return b.String()
}

func (e *ExternalSecretSyncError) Unwrap() error { return ErrExternalSecretNotSynced }

// waitExternalSecrets waits for every ref to sync its current spec, polling all
// of them against ONE deadline, so a batch of N failing ExternalSecrets costs
// one timeout and not N. Every failure is collected, not just the first, so the
// operator sees the whole picture from one run.
func waitExternalSecrets(ctx context.Context, opts ApplyOpts, policy RolloutPolicy, refs []stageRef, before string) error {
	if len(refs) == 0 {
		return nil
	}
	timeout := policy.Timeout
	if timeout <= 0 {
		timeout = DefaultRolloutTimeout
	}
	pending := append([]stageRef(nil), refs...)
	for i := range pending {
		if pending[i].Namespace == "" {
			pending[i].Namespace = opts.Namespace
		}
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].Namespace != pending[j].Namespace {
			return pending[i].Namespace < pending[j].Namespace
		}
		return pending[i].Name < pending[j].Name
	})
	if !opts.Quiet {
		fmt.Printf("Waiting for %d ExternalSecret(s) to sync before %s...\n", len(pending), before)
	}
	// reasons is the last thing each unsynced ExternalSecret said.
	reasons := map[stageRef]string{}
	deadline := time.Now().Add(timeout)
	for {
		var still []stageRef
		for _, ref := range pending {
			v, err := getExternalSecret(ctx, opts.Context, ref)
			if err != nil {
				reasons[ref] = err.Error()
				still = append(still, ref)
				continue
			}
			if ok, why := v.ready(); !ok {
				reasons[ref] = why
				still = append(still, ref)
				continue
			}
			fmt.Printf("  %s/%s: synced\n", ref.Namespace, ref.Name)
			observeRollout(opts, "ExternalSecret", ref.Namespace+"/"+ref.Name, RolloutStateReady, nil)
		}
		pending = still
		if len(pending) == 0 {
			return nil
		}
		if ctx.Err() != nil || !time.Now().Add(secretSyncPoll).Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(secretSyncPoll):
		}
	}
	gate := &ExternalSecretSyncError{Context: opts.Context, Held: "workload was applied"}
	for _, ref := range pending {
		reason := fmt.Sprintf("%s (waited %s)", reasons[ref], timeout)
		fmt.Printf("  FAILED: ExternalSecret %s/%s: %s\n", ref.Namespace, ref.Name, reason)
		observeRollout(opts, "ExternalSecret", ref.Namespace+"/"+ref.Name, RolloutStateTimedOut, errors.New(reason))
		gate.Failures = append(gate.Failures, secretSyncFailure{Ref: ref, Reason: reason, State: RolloutStateTimedOut})
	}
	return gate
}
