package core

// The responder's and the release workflow's vocabularies
// (db/migrations/20261126161542_pipeline_incidents_and_releases.up.sql). Each All* set is mirrored by a CHECK that
// declares it with forge:closed-set, so `forge lint --closed-sets` fails the
// moment the two drift: a value added here needs a migration widening its
// CHECK.

// IncidentSource is the detector that opened an incident.
type IncidentSource string

const (
	IncidentSourceMain      IncidentSource = "main"
	IncidentSourceRelease   IncidentSource = "release"
	IncidentSourceProdAlert IncidentSource = "prod_alert"
	IncidentSourceObsIssue  IncidentSource = "obs_issue"
	IncidentSourceNightly   IncidentSource = "nightly"
	IncidentSourceFlake     IncidentSource = "flake"
	IncidentSourceInfra     IncidentSource = "infra"
	IncidentSourceRecurring IncidentSource = "recurring"
	// IncidentSourceVuln is a new blocking advisory against a deployed
	// release's stored SBOM (owner D16).
	IncidentSourceVuln IncidentSource = "vuln"
)

// AllIncidentSources is every IncidentSource (incidents_source).
var AllIncidentSources = []IncidentSource{
	IncidentSourceMain,
	IncidentSourceRelease,
	IncidentSourceProdAlert,
	IncidentSourceObsIssue,
	IncidentSourceNightly,
	IncidentSourceFlake,
	IncidentSourceInfra,
	IncidentSourceRecurring,
	IncidentSourceVuln,
}

// IncidentState is where an incident is in the responder's hands.
type IncidentState string

const (
	IncidentOpen        IncidentState = "open"
	IncidentResponding  IncidentState = "responding"
	IncidentMitigating  IncidentState = "mitigating"
	IncidentResolved    IncidentState = "resolved"
	IncidentNeedsPerson IncidentState = "needs_person"
)

// AllIncidentStates is every IncidentState (incidents_state).
var AllIncidentStates = []IncidentState{
	IncidentOpen,
	IncidentResponding,
	IncidentMitigating,
	IncidentResolved,
	IncidentNeedsPerson,
}

// IncidentSeverity ranks an incident; the responder's priority follows it.
type IncidentSeverity string

const (
	IncidentSeverityLow      IncidentSeverity = "low"
	IncidentSeverityMedium   IncidentSeverity = "medium"
	IncidentSeverityHigh     IncidentSeverity = "high"
	IncidentSeverityCritical IncidentSeverity = "critical"
)

// AllIncidentSeverities is every IncidentSeverity, lowest first
// (incidents_severity).
var AllIncidentSeverities = []IncidentSeverity{
	IncidentSeverityLow,
	IncidentSeverityMedium,
	IncidentSeverityHigh,
	IncidentSeverityCritical,
}

// FlakyTestState is what has been done about a flaky test.
type FlakyTestState string

const (
	FlakyTestSuspected   FlakyTestState = "suspected"
	FlakyTestQuarantined FlakyTestState = "quarantined"
	FlakyTestDeleted     FlakyTestState = "deleted"
	FlakyTestFixed       FlakyTestState = "fixed"
)

// AllFlakyTestStates is every FlakyTestState (flaky_tests_state).
var AllFlakyTestStates = []FlakyTestState{
	FlakyTestSuspected,
	FlakyTestQuarantined,
	FlakyTestDeleted,
	FlakyTestFixed,
}

// ReleaseState is where a release is: cut, built, gated, then promoted env
// by env.
type ReleaseState string

const (
	ReleaseCut        ReleaseState = "cut"
	ReleaseBuilding   ReleaseState = "building"
	ReleaseGating     ReleaseState = "gating"
	ReleaseGreen      ReleaseState = "green"
	ReleaseRed        ReleaseState = "red"
	ReleasePromoting  ReleaseState = "promoting"
	ReleasePromoted   ReleaseState = "promoted"
	ReleaseRolledBack ReleaseState = "rolled_back"
	ReleaseAbandoned  ReleaseState = "abandoned"
)

// AllReleaseStates is every ReleaseState (releases_state).
var AllReleaseStates = []ReleaseState{
	ReleaseCut,
	ReleaseBuilding,
	ReleaseGating,
	ReleaseGreen,
	ReleaseRed,
	ReleasePromoting,
	ReleasePromoted,
	ReleaseRolledBack,
	ReleaseAbandoned,
}

// ReleaseArtifactScanState is whether a release artifact's SBOM scan counts
// (owner D14). Only scanned counts; unscanned and stale are unproven.
type ReleaseArtifactScanState string

const (
	ReleaseArtifactScanned   ReleaseArtifactScanState = "scanned"
	ReleaseArtifactUnscanned ReleaseArtifactScanState = "unscanned"
	// ReleaseArtifactStale was scanned against a scanner database past
	// forge's freshness rule.
	ReleaseArtifactStale ReleaseArtifactScanState = "stale"
)

// AllReleaseArtifactScanStates is every ReleaseArtifactScanState
// (release_artifacts_scan_state).
var AllReleaseArtifactScanStates = []ReleaseArtifactScanState{
	ReleaseArtifactScanned,
	ReleaseArtifactUnscanned,
	ReleaseArtifactStale,
}

// ReleaseArtifactScanVerdict is forge's vuln_gate verdict on an artifact.
type ReleaseArtifactScanVerdict string

const (
	ReleaseArtifactScanPass ReleaseArtifactScanVerdict = "pass"
	// ReleaseArtifactScanWarn goes in the release notes.
	ReleaseArtifactScanWarn ReleaseArtifactScanVerdict = "warn"
	// ReleaseArtifactScanBlock refuses the cut.
	ReleaseArtifactScanBlock ReleaseArtifactScanVerdict = "block"
)

// AllReleaseArtifactScanVerdicts is every ReleaseArtifactScanVerdict
// (release_artifacts_scan_verdict).
var AllReleaseArtifactScanVerdicts = []ReleaseArtifactScanVerdict{
	ReleaseArtifactScanPass,
	ReleaseArtifactScanWarn,
	ReleaseArtifactScanBlock,
}

// ReleaseEnvStepState is where one env of a release's promotion chain is.
type ReleaseEnvStepState string

const (
	ReleaseEnvWaiting          ReleaseEnvStepState = "waiting"
	ReleaseEnvAwaitingApproval ReleaseEnvStepState = "awaiting_approval"
	ReleaseEnvDeploying        ReleaseEnvStepState = "deploying"
	ReleaseEnvSoaking          ReleaseEnvStepState = "soaking"
	ReleaseEnvPassed           ReleaseEnvStepState = "passed"
	ReleaseEnvFailed           ReleaseEnvStepState = "failed"
	// ReleaseEnvInconclusive: obs check could not judge it (exit 2); the
	// env's probes decide (owner D12).
	ReleaseEnvInconclusive ReleaseEnvStepState = "inconclusive"
	ReleaseEnvRolledBack   ReleaseEnvStepState = "rolled_back"
	ReleaseEnvSkipped      ReleaseEnvStepState = "skipped"
)

// AllReleaseEnvStepStates is every ReleaseEnvStepState
// (release_env_steps_state).
var AllReleaseEnvStepStates = []ReleaseEnvStepState{
	ReleaseEnvWaiting,
	ReleaseEnvAwaitingApproval,
	ReleaseEnvDeploying,
	ReleaseEnvSoaking,
	ReleaseEnvPassed,
	ReleaseEnvFailed,
	ReleaseEnvInconclusive,
	ReleaseEnvRolledBack,
	ReleaseEnvSkipped,
}
