package obsbackend

import (
	"testing"
)

func TestCloudWorkloadsMapsHealthRestartsAndJobs(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListWorkloads: `{
	  "workloads":[
	    {"workload":"web","namespace":"ns","health":"WORKLOAD_HEALTH_HEALTHY","restartsInWindow":"0"},
	    {"workload":"api","namespace":"ns","health":"WORKLOAD_HEALTH_CRASH_LOOPING","healthCause":"WORKLOAD_CAUSE_WORKLOAD",
	     "healthReason":"restarted 14x, last exit OOMKilled","restartsInWindow":"14","readyReplicas":0,"desiredReplicas":2,
	     "containers":[{"container":"api","imageName":"img","imageTag":"v2","restartsInWindow":"14","lastTerminatedReason":"OOMKilled","lastTerminatedAt":"2026-10-22T11:30:00Z"}],
	     "latestWarning":{"reason":"BackOff","plainReason":"Restarting after crashes","time":"2026-10-22T11:31:00Z"}}],
	  "jobs":[{"name":"migrate-abc","workload":"migrate","result":"WORKLOAD_JOB_RESULT_FAILED","reason":"BackoffLimitExceeded","durationSeconds":"40"}]}`}}
	res, err := cloudWith(f).Workloads(t.Context(), narrowC())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Workloads) != 2 || res.Workloads[0].Workload != "api" {
		t.Fatalf("worst first: %+v", res.Workloads)
	}
	api := res.Workloads[0]
	if api.Health != WorkloadCrashLooping || api.Cause != "workload" || api.Restarts != 14 || api.LastExit != "OOMKilled" ||
		*api.Desired != 2 || api.Containers[0].Image != "img:v2" || api.LatestWarning.Plain != "Restarting after crashes" || !api.Unhealthy() {
		t.Errorf("api = %+v", api)
	}
	if res.Workloads[1].Unhealthy() {
		t.Errorf("a healthy workload with no restarts needs no look")
	}
	if len(res.Jobs) != 1 || res.Jobs[0].Result != JobFailed || res.Jobs[0].Seconds != 40 {
		t.Errorf("jobs = %+v", res.Jobs)
	}
	if sc, _ := f.reqs[procListWorkloads]["scope"].(map[string]any); sc == nil {
		t.Errorf("no scope sent: %v", f.reqs[procListWorkloads])
	}
}
