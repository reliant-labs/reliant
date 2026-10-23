package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestEmptyRolloutAfterPublish pins the check that replaces a 15-minute silent
// wait with one sentence, now that it runs only AFTER this command published
// the env's hosted tiers.
//
// The case that matters is the FIRST row: a control plane that answers
// GetRollout with `status=ok` and an empty workload list has no unhealthy
// workload to report, which is indistinguishable from "healthy" to a phase
// check — hence the guard keys on the workload list instead.
//
// MUTATION VERIFIED RED: drop the `len(rollout.Workloads) > 0` guard and the
// "published backend" / "unpinned database only" rows start refusing a
// perfectly good deploy.
func TestEmptyRolloutAfterPublish(t *testing.T) {
	published := []wireWorkloadRollout{{DeploymentID: "dep-api", Name: "api"}}
	database := []wireWorkloadRollout{{DeploymentID: "dep-db", Name: "db"}}
	tiers := []hostedTier{{Name: "api", Kind: hostedTierWorkload}}

	for _, tc := range []struct {
		name    string
		rollout wireRollout
		refuse  bool
	}{
		{
			name:    "no workloads at all",
			rollout: wireRollout{Phase: wireRolloutPhaseUnspecified},
			refuse:  true,
		},
		{
			// A phase that READS fine must not rescue an empty rollout: the
			// server has no workload to call unhealthy.
			name:    "succeeded phase but still no workloads",
			rollout: wireRollout{Phase: "DEPLOY_ROLLOUT_PHASE_SUCCEEDED"},
			refuse:  true,
		},
		{
			name:    "published backend",
			rollout: wireRollout{Phase: "DEPLOY_ROLLOUT_PHASE_PROGRESSING", Workloads: published},
			refuse:  false,
		},
		{
			// A database cannot carry a release artifact, so it is unpinned —
			// but its presence proves the control plane holds the publish.
			name:    "unpinned database only",
			rollout: wireRollout{Phase: wireRolloutPhaseUnspecified, Unpinned: database},
			refuse:  false,
		},
		{
			// A queued promotion is empty because it is queued.
			name:    "held",
			rollout: wireRollout{Phase: wireRolloutPhaseHeld},
			refuse:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := emptyRolloutAfterPublish("staging", tiers, "promo-1", tc.rollout)
			if !tc.refuse {
				if err != nil {
					t.Fatalf("refused a published env: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("no refusal: the deploy would wait out its whole budget for a rollout with nothing in it")
			}
			// Exit 2 — we could not determine health — not 1.
			var coded *exitCodeError
			if !errors.As(err, &coded) || coded.code != exitUndetermined {
				t.Errorf("exit code = %v, want exitUndetermined (%d)", err, exitUndetermined)
			}
			// It must name what was published and say the publish RAN. The
			// old wording told the operator to "publish first" with a command
			// that, after O-15, builds and cuts a different release.
			for _, want := range []string{"api (workload)", "promo-1", "promotion IS recorded", "publish above DID run"} {
				if !strings.Contains(coded.msg, want) {
					t.Errorf("message does not mention %q:\n%s", want, coded.msg)
				}
			}
			if strings.Contains(coded.msg, "Publish them first") {
				t.Errorf("the publish just ran; telling the operator to publish first is false:\n%s", coded.msg)
			}
		})
	}
}
