// Copyright (c) 2025 Reliant Labs
package types

// The Temporal error types an activity fails with when one of the run's org's
// admin policies refused its call (control-plane docs/design/org-policies.md
// §5.1). Both are non-retryable: a verdict, not a fault.
const (
	// OrgPolicyRefusedErrType: a person is behind the run. The step's
	// exhausted retries pause the run as usual; switching model, Resume or
	// the policy changing wakes it.
	OrgPolicyRefusedErrType = "OrgPolicyRefused"
	// OrgPolicyRefusedUnattendedErrType: nobody is present (a trigger run).
	// The run ends with the reason instead of pausing
	// (PauseController.endsRunInsteadOfPausing), and its trigger is paused
	// once with core.TriggerPausedOrgPolicy.
	OrgPolicyRefusedUnattendedErrType = "OrgPolicyRefusedUnattended"
)
