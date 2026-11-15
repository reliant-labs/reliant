package cluster

import "testing"

// prodIncidentManifests is the shape of the prod deploy that caused the
// incident this file exists for (run 33217453299), in miniature: the two
// one-shot Jobs as they were ACTUALLY applied — with the spec-hash suffix
// forge appends to a one-shot Job's name — alongside a Deployment and a
// scheduled CronJob.
//
// The hashed names are the point, not padding. The wait set has to come
// from these documents, because these are the only Job objects that exist
// in the namespace after the apply. An unhashed "control-plane-migrate"
// is not a stale spelling of the same object; it is no object at all.
const prodIncidentManifests = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: control-plane-prod
---
apiVersion: batch/v1
kind: Job
metadata:
  name: control-plane-migrate-aed30b6854
  namespace: control-plane-prod
  labels:
    forge.dev/job-name: control-plane-migrate
    forge.dev/spec-hash: aed30b6854
---
apiVersion: batch/v1
kind: Job
metadata:
  name: control-plane-idp-provision-e8b66e53c9
  namespace: control-plane-prod
  labels:
    forge.dev/job-name: control-plane-idp-provision
    forge.dev/spec-hash: e8b66e53c9
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly-report
  namespace: control-plane-prod`

// TestOneShotWaitSet_OnlyWaitsOnJobsThatWereApplied is the regression test
// for the prod deploy that succeeded and was reported as failed.
//
// The deploy was green: 201 objects server-side applied, all 13 Deployments
// ready, and both one-shot Jobs reached `complete`. forge failed it anyway,
// because the wait set was the manifest-derived names UNIONED with a
// caller-supplied list that derived UNHASHED entity names
// ("control-plane-migrate"). Those two names had never been applied and did
// not exist in the namespace, `kubectl wait` errored on both, and the
// rollout policy — correctly, for a Job that really did fail — turned that
// into a failed deploy.
//
// The invariant that keeps it fixed: a name that was never applied cannot
// enter the wait set, because the wait set is derived from the applied
// manifests and from nothing else.
func TestOneShotWaitSet_OnlyWaitsOnJobsThatWereApplied(t *testing.T) {
	got := oneShotWaitSet(prodIncidentManifests)

	// The two Jobs that were actually applied MUST be waited on. This is
	// the half that must not regress in the name of fixing the other half:
	// a Job that was applied and then FAILED still has to fail the deploy,
	// and it can only do that if it is in the wait set.
	for _, want := range []string{
		"control-plane-migrate-aed30b6854",
		"control-plane-idp-provision-e8b66e53c9",
	} {
		if !contains(got, want) {
			t.Errorf("applied Job %q missing from wait set %v — a Job that fails must still fail the deploy", want, got)
		}
	}

	// The unhashed entity names are what the caller-supplied list used to
	// contribute. No object by either name was ever applied, so waiting on
	// them can only ever produce a false failure.
	for _, neverApplied := range []string{
		"control-plane-migrate",
		"control-plane-idp-provision",
	} {
		if contains(got, neverApplied) {
			t.Errorf("wait set %v contains %q, which was never applied — this fails a deploy that succeeded", got, neverApplied)
		}
	}

	// A scheduled CronJob runs on its own cadence and renders as
	// `kind: CronJob`, so it is not a one-shot and is not waited on.
	if contains(got, "nightly-report") {
		t.Errorf("scheduled CronJob in one-shot wait set: %v", got)
	}

	if len(got) != 2 {
		t.Errorf("expected exactly the 2 applied Jobs, got %v", got)
	}
}

// TestOneShotWaitSet_EmptyStreamWaitsOnNothing confirms a render with no
// Jobs produces no wait rather than an inherited list from somewhere else.
// With the wait set derived solely from the manifests, "nothing applied"
// and "nothing awaited" cannot drift apart.
func TestOneShotWaitSet_EmptyStreamWaitsOnNothing(t *testing.T) {
	const noJobs = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server`

	if got := oneShotWaitSet(noJobs); len(got) != 0 {
		t.Errorf("expected no one-shot waits for a Job-less render, got %v", got)
	}
}
