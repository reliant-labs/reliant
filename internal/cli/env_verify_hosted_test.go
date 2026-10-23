package cli

import "testing"

// The pure hosted mapping: one GetRollout answer → env verify's states.
// The staleness case is the reason it exists: the server marks a stale
// observation UNKNOWN (control-plane #491), and that must read as UNREACHABLE,
// never as MATCH on the last digest it saw.
func TestVerifyHostedRollout(t *testing.T) {
	pin, other := sha("1"), sha("2")
	w := func(name, phase, state, observed string) wireWorkloadRollout {
		return wireWorkloadRollout{Name: name, Artifact: name, PinnedDigest: pin, ObservedDigest: observed,
			ObservedState: state, Phase: phase}
	}
	got := verifyHostedRollout(wireRollout{Workloads: []wireWorkloadRollout{
		w("api", wireRolloutPhaseSucceeded, "DEPLOY_OBSERVED_STATE_READY", pin),
		w("web", wireRolloutPhaseProgressing, "DEPLOY_OBSERVED_STATE_READY", other),
		w("jobs", wireRolloutPhaseUnknown, "DEPLOY_OBSERVED_STATE_READY", pin), // stale: last saw the pin
		w("cron", wireRolloutPhaseDegraded, "DEPLOY_OBSERVED_STATE_DELETED", ""),
		{Name: "postgres", ObservedState: "DEPLOY_OBSERVED_STATE_READY"}, // unpinned: not verified
	}})
	want := map[string]imageState{"api": imageMatch, "web": imageDrift, "jobs": imageUnreachable, "cron": imageMissing}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d (an unpinned workload must not be verified): %+v", len(got), len(want), got)
	}
	for _, v := range got {
		if v.State != want[v.Image] {
			t.Errorf("%s: state %s, want %s (%s)", v.Image, v.State, want[v.Image], v.Detail)
		}
		if v.Image == "jobs" && v.Running != "" {
			t.Errorf("a stale observation must not report a running digest, got %q", v.Running)
		}
		if v.Image == "web" && (v.Declared != pin || v.Running != other) {
			t.Errorf("drift must carry both digests, got %+v", v)
		}
	}
	if tally := tallyEnvStatusRelease(got); tally.Unreachable != 1 || tally.Match != 1 || tally.Drift != 1 || tally.Missing != 1 {
		t.Errorf("tally = %+v", tally)
	}
}
