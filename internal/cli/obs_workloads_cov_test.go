package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

const covAWorkloadsReply = `{
 "workloads":[
  {"workload":"api","namespace":"shop","health":"WORKLOAD_HEALTH_HEALTHY","restartsInWindow":"0","readyReplicas":2,"desiredReplicas":2},
  {"workload":"worker","namespace":"shop","health":"WORKLOAD_HEALTH_CRASH_LOOPING","healthReason":"restarted 14x","restartsInWindow":"14","readyReplicas":0,"desiredReplicas":1,
   "containers":[{"container":"w","lastTerminatedReason":"OOMKilled","lastTerminatedAt":"2026-10-22T11:30:00Z"}]},
  {"workload":"cron","namespace":"shop","health":"WORKLOAD_HEALTH_DEGRADED","restartsInWindow":"1",
   "latestWarning":{"reason":"BackOff","plainReason":"the container keeps failing","time":"2026-10-22T11:00:00Z"}}
 ],
 "jobs":[
  {"name":"migrate-1","workload":"migrate","result":"WORKLOAD_JOB_RESULT_SUCCEEDED","releaseVersion":"v1","startedAt":"2026-10-22T10:00:00Z"},
  {"name":"backup-2","workload":"backup","result":"WORKLOAD_JOB_RESULT_FAILED","reason":"deadline exceeded","releaseVersion":"v2","startedAt":"2026-10-22T11:00:00Z"}
 ]}`

func covARunWorkloads(t *testing.T, reply string, args ...string) (string, error) {
	t.Helper()
	caller := &seqCaller{replies: map[string][]string{procWorkloads: {reply}}, errs: map[string]error{}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"workloads", "prod"}, args...)...)
	return out, err
}

func TestObsWorkloadsTextListsOnlyWhatNeedsALook(t *testing.T) {
	out, err := covARunWorkloads(t, covAWorkloadsReply)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"3 workload(s) on prod: 2 need a look, 1 crash-looping, 15 restart(s), 1 failed job run(s)",
		"WORKLOAD", "CRASH_LOOPING", "0/1", "OOMKilled", "restarted 14x",
		"the container keeps failing", // a workload with no reason falls back to its latest warning
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\napi ") {
		t.Errorf("the healthy workload must be hidden without --all:\n%s", out)
	}
	if !strings.Contains(out, "backup-2") || !strings.Contains(out, "FAILED") || strings.Contains(out, "migrate-1") {
		t.Errorf("only the failed job is listed by default:\n%s", out)
	}
	// The worst workload's logs and the failed job's logs are suggested.
	for _, want := range []string{
		"forge obs logs prod --since 24h --filter k8s.namespace.name=shop --filter k8s.workload.name=worker",
		"forge obs logs prod --since 24h --filter k8s.workload.name=backup",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("suggested commands lack %q:\n%s", want, out)
		}
	}
}

func TestObsWorkloadsAllAndJobsAll(t *testing.T) {
	out, err := covARunWorkloads(t, covAWorkloadsReply, "--all", "--jobs", "all")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "api") || !strings.Contains(out, "2/2") {
		t.Errorf("--all must list the healthy workload with its replicas:\n%s", out)
	}
	if !strings.Contains(out, "migrate-1") || !strings.Contains(out, "SUCCEEDED") {
		t.Errorf("--jobs all must list successful jobs too:\n%s", out)
	}
	if strings.Index(out, "backup-2") > strings.Index(out, "migrate-1") {
		t.Errorf("failed jobs come first:\n%s", out)
	}
}

func TestObsWorkloadsJobsNone(t *testing.T) {
	out, err := covARunWorkloads(t, covAWorkloadsReply, "--jobs", "none", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if jobs, _ := doc["jobs"].([]any); len(jobs) != 0 {
		t.Errorf("--jobs none must list no jobs: %v", doc["jobs"])
	}
	sum := doc["summary"].(map[string]any)
	if sum["failed_jobs"] != float64(1) || sum["workloads"] != float64(3) || sum["unhealthy"] != float64(2) || sum["crash_looping"] != float64(1) || sum["restarts"] != float64(15) {
		t.Errorf("the summary still counts what was filtered out: %v", sum)
	}
	if doc["schema"] != "forge.obs.workloads/v1" || doc["query_mode"] != "structured" {
		t.Errorf("head = %v", doc)
	}
}

func TestObsWorkloadsAllHealthy(t *testing.T) {
	healthy := `{"workloads":[{"workload":"api","health":"WORKLOAD_HEALTH_HEALTHY"}]}`
	out, err := covARunWorkloads(t, healthy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "every workload is healthy with no restarts in the range (--all lists them)") {
		t.Errorf("output:\n%s", out)
	}
	out, err = covARunWorkloads(t, healthy, "--all")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "every workload is healthy") || !strings.Contains(out, "HEALTHY") {
		t.Errorf("with --all the table replaces the hint:\n%s", out)
	}
}

func TestObsWorkloadsTruncationNotice(t *testing.T) {
	out, err := covARunWorkloads(t, `{"truncated":true,"workloads":[{"workload":"api","health":"WORKLOAD_HEALTH_DEGRADED"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(more than 500 workloads matched; narrow with --service)") {
		t.Errorf("output:\n%s", out)
	}
}

func TestObsWorkloadsRefusals(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		exit int
	}{
		{"bad --jobs", []string{"--jobs", "some"}, `--jobs "some": use failed, all or none`, 1},
		{"range over 7 days", []string{"--since", "200h"}, "the range is at most 7 days", 1},
		{"bad --from", []string{"--from", "yesterday"}, "is not an RFC 3339 time", 1},
		{"unknown field", []string{"--fields", "nope"}, "nope", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := &seqCaller{replies: map[string][]string{}, errs: map[string]error{}}
			_, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"workloads", "prod"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if got := exitCodeForError(err); got != tc.exit {
				t.Errorf("exit = %d, want %d", got, tc.exit)
			}
			if len(caller.calls) != 0 {
				t.Errorf("a refused request must not reach the control plane: %+v", caller.calls)
			}
		})
	}
}

func TestObsWorkloadsFailureIsAJSONDocument(t *testing.T) {
	caller := &seqCaller{replies: map[string][]string{}, errs: map[string]error{procWorkloads: &cloud.Error{Code: cloud.CodeUnavailable}}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "workloads", "prod", "--json")
	if exitCodeForError(err) != 2 {
		t.Fatalf("err = %v, want could-not-look", err)
	}
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.workloads/v1" || doc["ok"] != false {
		t.Errorf("a failed read states its schema and ok=false: %v", doc)
	}
}

func TestObsWorkloadsLocalBackendHasNoWorkloadState(t *testing.T) {
	caller := &seqCaller{replies: map[string][]string{}, errs: map[string]error{}}
	_, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "workloads", "prod", "--backend", "local")
	if err == nil || !strings.Contains(err.Error(), "the local backend has no workload state") || !strings.Contains(err.Error(), "env status prod") {
		t.Fatalf("err = %v", err)
	}
	if exitCodeForError(err) != 2 {
		t.Errorf("exit = %d, want 2", exitCodeForError(err))
	}
}

func TestFailedJobsFirstKeepsOrderWithinEachGroup(t *testing.T) {
	jobs := []obsbackend.Job{
		{Name: "ok1", Result: obsbackend.JobSucceeded}, {Name: "bad1", Result: obsbackend.JobFailed},
		{Name: "ok2", Result: obsbackend.JobSucceeded}, {Name: "bad2", Result: obsbackend.JobFailed},
	}
	var got []string
	for _, j := range failedJobsFirst(jobs) {
		got = append(got, j.Name)
	}
	if want := "bad1 bad2 ok1 ok2"; strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
	if jobs[0].Name != "ok1" {
		t.Error("the input must not be reordered in place")
	}
}

func TestSinceFlag(t *testing.T) {
	tests := map[time.Duration]string{
		0:                               "1m",
		10 * time.Second:                "1m",
		90 * time.Second:                "2m",
		10 * time.Minute:                "10m",
		24 * time.Hour:                  "24h",
		70 * time.Minute:                "1h10m",
		7*24*time.Hour + 30*time.Minute: "168h30m",
	}
	for d, want := range tests {
		if got := sinceFlag(d); got != want {
			t.Errorf("sinceFlag(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestWorkloadLogsCommand(t *testing.T) {
	if got := workloadLogsCommand("prod", "1h", "", "api"); got != "forge obs logs prod --since 1h --filter k8s.workload.name=api" {
		t.Errorf("got %q", got)
	}
	if got := workloadLogsCommand("prod", "1h", "ns", ""); got != "forge obs logs prod --since 1h --filter k8s.namespace.name=ns" {
		t.Errorf("got %q", got)
	}
}

func TestFirstNonEmptyString(t *testing.T) {
	if got := firstNonEmptyString("", "", "x", "y"); got != "x" {
		t.Errorf("got %q", got)
	}
	if got := firstNonEmptyString("", ""); got != "" {
		t.Errorf("got %q", got)
	}
}
