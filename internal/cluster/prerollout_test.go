package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for the pre-rollout gate (prerollout.go): a one-shot Job — a schema
// migration, by default every one — is applied and must COMPLETE before any
// workload in the same apply is sent, and a Job that fails or times out means
// NO workload is applied at all.
//
// They drive applyRendered — Apply minus the KCL render — against a fake
// kubectl on PATH that records every invocation in order, argv and stdin. The
// order of those invocations IS the property under test: that the Job was
// applied, awaited, and only then were the Deployments sent. Nothing here can
// be observed from Apply's return value alone, which is why a recorder and
// not a stub.

// gateContext is the kubectl context every test apply targets. KubectlApply
// refuses an empty one, which is the production invariant, so the tests keep
// it rather than weakening it.
const gateContext = "k3d-gate-test"

// migrateJob is a one-shot Job with NO deploy-phase annotation, so the tests
// that use it exercise the DEFAULT (pre-rollout) rather than an explicit one.
const migrateJob = `apiVersion: batch/v1
kind: Job
metadata:
  name: app-migrate-abc123
  namespace: app-prod
  labels:
    forge.dev/job-name: app-migrate
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: app-migrate
          image: reg.example.com/app@sha256:1111`

// gateStream is the shape of a real release, in stream order as forge's KCL
// emits it: the Namespace and config, the Job's identity, the workloads, the
// migrate Job, and a scheduled CronJob that must not tick against the old
// schema either.
var gateStream = strings.Join([]string{
	`apiVersion: v1
kind: Namespace
metadata:
  name: app-prod`,
	`apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: app-prod`,
	`apiVersion: v1
kind: ServiceAccount
metadata:
  name: app-migrate
  namespace: app-prod`,
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: app-prod
  labels:
    app.kubernetes.io/name: admin-server`,
	`apiVersion: v1
kind: Service
metadata:
  name: admin-server
  namespace: app-prod`,
	`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: search
  namespace: app-prod`,
	migrateJob,
	`apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly-report
  namespace: app-prod`,
}, docDelimiter)

// jobOutcome is what the fake kubectl reports for every Job wait.
type jobOutcome string

const (
	jobCompletes jobOutcome = "complete"
	jobFails     jobOutcome = "failed"
	jobTimesOut  jobOutcome = "timeout"
)

// fakeGateKubectl installs a fake `kubectl` on PATH and returns a reader for
// the invocations, in the order they started.
//
//   - apply echoes `<kind>/<name> serverside-applied` per document, so the
//     apply-completeness check is satisfied (a fake that prints nothing is
//     indistinguishable from the silent-partial-apply incident).
//   - wait answers as `outcome` dictates. WaitJobCompleteTimeout races a
//     condition=complete watcher against a condition=failed one, so the
//     losing watcher blocks until it is cancelled — exactly as kubectl does.
//     A timeout exits 1 with kubectl's own message on both. A CRD
//     condition=Established wait succeeds at once: nothing races it, so
//     sending it down the blocking path made it sit out the full `sleep 30`
//     and then "succeed" anyway — the same verdict, 30s later.
//   - get deployments lists `deployments` (the rollout wait's enumeration).
//   - everything else (rollout status, logs, describe) exits 0.
//
// Each record is assembled in a temp file and appended with ONE write, so the
// two concurrent Job-wait watchers cannot interleave their records.
func fakeGateKubectl(t *testing.T, outcome jobOutcome, deployments ...string) func() []kubectlCall {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl-calls.log")
	script := `#!/bin/sh
in=$(mktemp); rec=$(mktemp)
cat > "$in"
{ printf '<<<ARGS>>> %s\n' "$*"; printf '<<<STDIN>>>\n'; cat "$in"; printf '\n<<<END>>>\n'; } > "$rec"
cat "$rec" >> ` + logPath + `
rm -f "$rec"
case " $* " in
  *' apply '*)
    awk '/^kind:/{k=tolower($2)} /^  name:/{if(k!=""&&n==""){n=$2}} /^---$/{if(k!=""&&n!="")print k"/"n" serverside-applied"; k=""; n=""} END{if(k!=""&&n!="")print k"/"n" serverside-applied"}' "$in"
    rm -f "$in"; exit 0 ;;
  *' wait '*)
    rm -f "$in"
    case "$FAKE_JOB_OUTCOME" in
      timeout) echo "error: timed out waiting for the condition on jobs/x" >&2; exit 1 ;;
    esac
    case " $* " in
      *'--for=condition=Established'*) exit 0 ;;
      *'--for=condition=complete'*) [ "$FAKE_JOB_OUTCOME" = complete ] && exit 0 ;;
      *'--for=condition=failed'*) [ "$FAKE_JOB_OUTCOME" = failed ] && exit 0 ;;
    esac
    exec sleep 30 ;;
  *' get deployments '*)
    rm -f "$in"
    for d in $FAKE_DEPLOYMENTS; do echo "$d"; done
    exit 0 ;;
  *' get externalsecret'*)
    rm -f "$in"
    # FAKE_ES_JSON is what the fake ESO reports; secret_order_test.go sets it.
    # FAKE_ES_NAME singles one ExternalSecret out for FAKE_ES_NAME_JSON.
    case " $* " in
      *" $FAKE_ES_NAME "*) [ -n "$FAKE_ES_NAME" ] && { printf '%s\n' "$FAKE_ES_NAME_JSON"; exit 0; } ;;
    esac
    [ -n "$FAKE_ES_JSON" ] && { printf '%s\n' "$FAKE_ES_JSON"; exit 0; }
    echo 'Error from server (NotFound): externalsecrets.external-secrets.io not found' >&2; exit 1 ;;
esac
rm -f "$in"
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_JOB_OUTCOME", string(outcome))
	t.Setenv("FAKE_DEPLOYMENTS", strings.Join(deployments, " "))

	return func() []kubectlCall {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		var calls []kubectlCall
		for _, rec := range strings.Split(string(data), "<<<ARGS>>> ") {
			if strings.TrimSpace(rec) == "" {
				continue
			}
			argsPart, rest, found := strings.Cut(rec, "\n<<<STDIN>>>\n")
			if !found {
				continue
			}
			stdin, _, _ := strings.Cut(rest, "\n<<<END>>>")
			calls = append(calls, kubectlCall{Args: strings.TrimSpace(argsPart), Stdin: stdin})
		}
		return calls
	}
}

// gateOpts is a deploy-path ApplyOpts for the gate tests: framed output, the
// real default rollout policy, a short per-resource budget.
func gateOpts(mode RolloutMode) ApplyOpts {
	return ApplyOpts{
		Namespace: "app-prod",
		Context:   gateContext,
		Rollout:   RolloutPolicy{Mode: mode, Timeout: 5 * time.Second},
	}
}

// applies returns the apply calls, in order.
func applies(calls []kubectlCall) []kubectlCall {
	var out []kubectlCall
	for _, c := range calls {
		if c.IsApply() {
			out = append(out, c)
		}
	}
	return out
}

// firstIndex returns the position of the first call satisfying match, or -1.
func firstIndex(calls []kubectlCall, match func(kubectlCall) bool) int {
	for i, c := range calls {
		if match(c) {
			return i
		}
	}
	return -1
}

func appliesKind(kind string) func(kubectlCall) bool {
	return func(c kubectlCall) bool {
		return c.IsApply() && strings.Contains(c.Stdin, "\nkind: "+kind+"\n")
	}
}

func waitsForJobComplete(name string) func(kubectlCall) bool {
	return func(c kubectlCall) bool {
		return strings.Contains(c.Args, " wait ") && strings.Contains(c.Args, "--for=condition=complete") &&
			strings.Contains(c.Args, "job/"+name)
	}
}

// gatedKinds is every kind the gate must hold back that gateStream carries.
var gatedKinds = []string{"Deployment", "StatefulSet", "CronJob"}

// TestApply_PreRolloutJobCompletesBeforeAnyWorkloadIsApplied is the incident
// this gate exists for: the migrate Job used to share ONE apply with every
// Deployment and was awaited only after the rollouts, so new code served
// against the old schema until it finished.
//
// The required order is Job applied → Job complete → workloads applied, and
// the Job's apply must carry NO workload.
//
// Mutation that fails it: send `rest` in one pass again (drop the
// `phases.gated()` branch in applyRendered) — the Deployment rides the Job's
// apply and is sent before the Job completes. Also fails if an absent
// deploy-phase annotation defaults to post-rollout: migrateJob carries none.
func TestApply_PreRolloutJobCompletesBeforeAnyWorkloadIsApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")

	if err := applyRendered(context.Background(), gateOpts(RolloutWait), gateStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	got := calls()

	jobApply := firstIndex(got, appliesKind("Job"))
	jobWait := firstIndex(got, waitsForJobComplete("app-migrate-abc123"))
	if jobApply < 0 || jobWait < 0 {
		t.Fatalf("the migrate Job must be applied and awaited; job apply at %d, wait at %d\ncalls:\n%s", jobApply, jobWait, describeCalls(got))
	}
	if jobWait < jobApply {
		t.Errorf("the Job was awaited (call %d) before it was applied (call %d)", jobWait, jobApply)
	}
	for _, kind := range gatedKinds {
		at := firstIndex(got, appliesKind(kind))
		if at < 0 {
			t.Errorf("%s was never applied — a completed gate must release the workloads\ncalls:\n%s", kind, describeCalls(got))
			continue
		}
		if at < jobWait {
			t.Errorf("%s was applied (call %d) BEFORE the migrate Job completed (call %d) — new code would serve against the old schema\ncalls:\n%s",
				kind, at, jobWait, describeCalls(got))
		}
	}
	// The Job's own apply carries nothing that runs the release's code.
	jobStdin := got[jobApply].Stdin
	for _, kind := range gatedKinds {
		if strings.Contains(jobStdin, "\nkind: "+kind+"\n") {
			t.Errorf("the pre-rollout Job's apply also carried a %s — a single apply lets it land before the Job runs", kind)
		}
	}
	// The Job's identity lands before the Job does: its pod cannot start
	// without its ServiceAccount.
	if sa := firstIndex(got, appliesKind("ServiceAccount")); sa < 0 || sa > jobApply {
		t.Errorf("the ServiceAccount must be applied before the Job that runs as it (sa call %d, job call %d)", sa, jobApply)
	}
	// And the gate awaited it once: no second wait after the rollouts.
	waits := 0
	for _, c := range got {
		if waitsForJobComplete("app-migrate-abc123")(c) {
			waits++
		}
	}
	if waits != 1 {
		t.Errorf("the migrate Job was awaited %d times, want exactly 1 (at the gate)", waits)
	}
}

// TestApply_FailedPreRolloutJobAppliesNoWorkload: a migration that fails must
// stop the deploy BEFORE any workload changes, so the previous release keeps
// serving — and the error must name the Job and hand over the exact command
// that shows why.
//
// Mutation that fails it: ignore applyPreRolloutGate's error (fall through to
// the workload apply) — the Deployment, StatefulSet and CronJob are applied.
func TestApply_FailedPreRolloutJobAppliesNoWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobFails, "admin-server")

	err := applyRendered(context.Background(), gateOpts(RolloutWait), gateStream)
	assertGateHeld(t, err, calls(), "app-migrate-abc123")
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("a Job that reported condition=failed must say it failed, got:\n%v", err)
	}
}

// TestApply_TimedOutPreRolloutJobAppliesNoWorkload: a migration that is still
// running when its budget expires is not a success either, and says so as a
// timeout, not a failure, so the operator knows to look for a slow Job rather
// than a broken one.
//
// Mutation that fails it: treat a gate timeout as non-fatal.
func TestApply_TimedOutPreRolloutJobAppliesNoWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobTimesOut, "admin-server")

	err := applyRendered(context.Background(), gateOpts(RolloutWait), gateStream)
	assertGateHeld(t, err, calls(), "app-migrate-abc123")
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("an expired budget must read as a timeout, got:\n%v", err)
	}
}

// TestApply_PreRolloutGateHoldsUnderRolloutSkip: `--rollout=skip` means "do
// not wait for the cluster to converge". It never meant "ship code ahead of
// its migration", so the gate still waits for the Job and still refuses the
// workloads when it fails.
//
// Mutation that fails it: enforce the gate only in RolloutWait mode.
func TestApply_PreRolloutGateHoldsUnderRolloutSkip(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	for _, mode := range []RolloutMode{RolloutSkip, RolloutWarn} {
		t.Run(string(mode), func(t *testing.T) {
			calls := fakeGateKubectl(t, jobFails, "admin-server")
			err := applyRendered(context.Background(), gateOpts(mode), gateStream)
			assertGateHeld(t, err, calls(), "app-migrate-abc123")
		})
	}
}

// TestApply_PostRolloutJobIsNotAGate: a Job that needs THIS release's
// workloads (an IdP provisioner that talks to an in-stream IdP) declares
// post-rollout, rides the workload apply and is awaited after the rollouts.
// Held at the gate instead, it would wait on a workload the gate itself was
// holding back and abort a healthy deploy.
//
// Mutation that fails it: ignore the annotation (every Job pre-rollout).
func TestApply_PostRolloutJobIsNotAGate(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")
	provision := strings.Replace(migrateJob, "  name: app-migrate-abc123\n",
		"  name: app-provision-def456\n  annotations:\n    "+DeployPhaseAnnotation+": "+DeployPhasePostRollout+"\n", 1)
	stream := strings.Join([]string{
		`apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: app-prod`,
		provision,
	}, docDelimiter)

	if err := applyRendered(context.Background(), gateOpts(RolloutWait), stream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	got := calls()

	deploy := firstIndex(got, appliesKind("Deployment"))
	job := firstIndex(got, appliesKind("Job"))
	rollout := firstIndex(got, func(c kubectlCall) bool { return strings.Contains(c.Args, "rollout status") })
	jobWait := firstIndex(got, waitsForJobComplete("app-provision-def456"))
	if deploy < 0 || deploy != job {
		t.Errorf("a post-rollout Job applies WITH the workloads (deployment call %d, job call %d)\ncalls:\n%s", deploy, job, describeCalls(got))
	}
	if rollout < 0 || jobWait < rollout {
		t.Errorf("a post-rollout Job is awaited AFTER the rollouts (rollout call %d, job wait %d)\ncalls:\n%s", rollout, jobWait, describeCalls(got))
	}
}

// TestApply_UnknownDeployPhaseIsRefusedBeforeAnyApply: a typo'd phase must
// not quietly pick one. Read as post-rollout it ships code ahead of its
// migration; read as pre-rollout it silently becomes a gate its author meant
// to opt out of. Refused before kubectl is invoked at all.
//
// Mutation that fails it: fall back to the default for an unrecognized value.
func TestApply_UnknownDeployPhaseIsRefusedBeforeAnyApply(t *testing.T) {
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")
	typo := strings.Replace(migrateJob, "  namespace: app-prod\n",
		"  namespace: app-prod\n  annotations:\n    "+DeployPhaseAnnotation+": pre-rolout\n", 1)

	err := applyRendered(context.Background(), gateOpts(RolloutWait), typo)
	if err == nil {
		t.Fatal("an unknown deploy phase must be refused")
	}
	for _, want := range []string{"app-migrate-abc123", "pre-rolout", DeployPhasePreRollout, DeployPhasePostRollout} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q, got: %v", want, err)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("nothing may reach the cluster before the stream is validated, got:\n%s", describeCalls(got))
	}
}

// TestApply_StreamWithoutAJobIsUnchanged pins that the gate costs nothing
// when there is nothing to gate: the config pass and ONE apply of the rest,
// exactly as before this gate existed.
//
// Mutation that fails it: split the workloads from the support objects even
// when no pre-rollout Job exists.
func TestApply_StreamWithoutAJobIsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")
	stream := strings.Join([]string{
		`apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: app-prod`,
		`apiVersion: v1
kind: ServiceAccount
metadata:
  name: admin-server
  namespace: app-prod`,
		`apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: app-prod`,
	}, docDelimiter)

	if err := applyRendered(context.Background(), gateOpts(RolloutWait), stream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	if got := applies(calls()); len(got) != 2 {
		t.Errorf("a stream with no Job is the config pass plus ONE apply, got %d applies:\n%s", len(got), describeCalls(got))
	}
}

// TestPartitionRolloutPhases_EveryDocumentLandsOnce pins the partition itself:
// the default phase, the held-back kinds, and that nothing is dropped or
// duplicated — a dropped document is an object the deploy silently never
// applies.
func TestPartitionRolloutPhases_EveryDocumentLandsOnce(t *testing.T) {
	_, rest := PartitionConfigManifests(gateStream)
	phases, err := partitionRolloutPhases(rest)
	if err != nil {
		t.Fatalf("partition: %v", err)
	}
	if len(phases.preJobRefs) != 1 || phases.preJobRefs[0] != (jobRef{Name: "app-migrate-abc123", Namespace: "app-prod"}) {
		t.Errorf("an unannotated Job is pre-rollout by default, got refs %+v", phases.preJobRefs)
	}
	count := func(s string) int { return len(splitDocs(s)) }
	if got, want := count(phases.support)+count(phases.preJobs)+count(phases.workloads), count(rest); got != want {
		t.Errorf("partition holds %d documents, the stream had %d", got, want)
	}
	for _, kind := range gatedKinds {
		if !strings.Contains(phases.workloads, "\nkind: "+kind+"\n") {
			t.Errorf("%s must be held back until the gate opens", kind)
		}
		if strings.Contains(phases.support, "\nkind: "+kind+"\n") {
			t.Errorf("%s leaked into the support pass, which lands before the Job", kind)
		}
	}
}

// assertGateHeld is the shared verdict for a deploy stopped at the gate: a
// *PreRolloutJobError naming the Job and the logs command, no workload
// applied, and no rollout awaited (nothing new was sent to roll out).
func assertGateHeld(t *testing.T, err error, got []kubectlCall, job string) {
	t.Helper()
	var gate *PreRolloutJobError
	if !errors.As(err, &gate) {
		t.Fatalf("a Job that did not complete must stop the deploy with *PreRolloutJobError, got %T: %v\ncalls:\n%s", err, err, describeCalls(got))
	}
	msg := err.Error()
	for _, want := range []string{
		job,
		"kubectl --context " + gateContext + " -n app-prod logs job/" + job + " --all-containers",
		"NO workload in this group was applied",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error must contain %q, got:\n%s", want, msg)
		}
	}
	for _, kind := range gatedKinds {
		if at := firstIndex(got, appliesKind(kind)); at >= 0 {
			t.Errorf("%s was applied (call %d) although the pre-rollout Job did not complete\ncalls:\n%s", kind, at, describeCalls(got))
		}
	}
	if at := firstIndex(got, func(c kubectlCall) bool { return strings.Contains(c.Args, "rollout status") }); at >= 0 {
		t.Errorf("a rollout was awaited (call %d) after the gate held", at)
	}
}

// describeCalls renders the recorded sequence for a failure message: one line
// per call with the kinds its stdin carried, which is the whole story of an
// ordering bug.
func describeCalls(calls []kubectlCall) string {
	var b strings.Builder
	for i, c := range calls {
		var kinds []string
		for _, line := range strings.Split(c.Stdin, "\n") {
			if k, ok := strings.CutPrefix(line, "kind: "); ok {
				kinds = append(kinds, k)
			}
		}
		b.WriteString("  " + strconv.Itoa(i) + " " + c.Args + " [" + strings.Join(kinds, ",") + "]\n")
	}
	return b.String()
}
