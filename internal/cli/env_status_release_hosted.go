package cli

// `forge env status <env>` for a HOSTED environment (hosted-deploy-primitives
// §3.5, task F7).
//
// A cluster env is verified by reading the cluster with kubectl (env_verify.go).
// A hosted env's cluster belongs to the control plane, and forge cannot read
// it, by design. So the control plane's OBSERVER is the only witness there is,
// and this file reads it through GetRollout for the env's current promotion:
// per pinned workload, the digest the promotion froze against the digest the
// observer last saw.
//
// That is a different source of truth from kubectl, and the report says so
// rather than hiding it: hosted results carry `source: control-plane observer`.
// The two reasons env_verify.go gave for refusing control-plane state no longer
// hold here:
//
//   - "nothing writes the observed columns": the tier observer now writes them
//     every pass;
//   - "a mirror can be stale": the rollout phase function marks an observation
//     older than 2 × the observer's interval UNKNOWN (control-plane #491), and
//     UNKNOWN is UNREACHABLE here, never MATCH.
//
// The five states and the exit codes are env verify's own, unchanged.

import (
	"fmt"
	"sort"
	"strings"
)

// hostedObserverSource names where a hosted verdict came from.
const hostedObserverSource = "control-plane observer"

// verifyHostedRollout maps one GetRollout answer onto env verify's five image
// states, one row per PINNED workload. Pure, so the mapping is table-tested
// without a control plane.
//
// UNTAGGED never occurs: a hosted workload is always pinned by digest.
func verifyHostedRollout(r wireRollout) []imageVerification {
	out := make([]imageVerification, 0, len(r.Workloads))
	for _, w := range r.Workloads {
		if w.PinnedDigest == "" {
			// Not pinned by this promotion: reported by `env rollout`,
			// never verified here, exactly as an unpinned workload never
			// gates a rollout.
			continue
		}
		name := w.Artifact
		if name == "" {
			name = w.Name
		}
		v := imageVerification{
			Image:     name,
			Declared:  w.PinnedDigest,
			Running:   w.ObservedDigest,
			Workloads: []string{w.Name},
		}
		phase := rolloutPhaseName(w.Phase)
		switch {
		case phase == "unknown" || phase == "unspecified" || (w.ObservedDigest == "" && w.ObservedState == ""):
			// Not observed, or observed too long ago to count (the
			// server decides staleness). Says nothing about the env.
			v.State = imageUnreachable
			v.Running = ""
			v.Detail = firstNonEmpty(w.LastError, "the control plane has no current observation of this workload")
		case observedStateName(w.ObservedState) == "deleted":
			v.State = imageMissing
			v.Running = ""
			v.Detail = "the control plane observes this workload as deleted"
		case w.ObservedDigest == "":
			v.State = imageMissing
			v.Detail = "observed, but running no digest"
		case w.ObservedDigest == w.PinnedDigest:
			v.State = imageMatch
		default:
			v.State = imageDrift
			v.Detail = fmt.Sprintf("declared %s but running %s", w.PinnedDigest, w.ObservedDigest)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out
}

// observedStateName lowercases a DeployObservedState enum value:
// "DEPLOY_OBSERVED_STATE_DELETED" → "deleted".
func observedStateName(wire string) string {
	return strings.ToLower(strings.TrimPrefix(wire, "DEPLOY_OBSERVED_STATE_"))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
