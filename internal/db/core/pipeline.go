package core

// The pipeline's enrolment and settings vocabularies
// (db/migrations/20261125211730_pipeline_repos_and_settings.up.sql). Each
// All* set is mirrored by a CHECK that declares it with forge:closed-set, so
// `forge lint --closed-sets` fails the moment the two drift: a value added
// here needs a migration widening its CHECK.

// PipelineRepoMode is how far the pipeline acts on an enrolled repository.
type PipelineRepoMode string

const (
	// PipelineRepoModeOff enrols the repository without acting on it.
	PipelineRepoModeOff PipelineRepoMode = "off"
	// PipelineRepoModeShadow computes and records decisions without acting.
	PipelineRepoModeShadow PipelineRepoMode = "shadow"
	// PipelineRepoModeOn acts.
	PipelineRepoModeOn PipelineRepoMode = "on"
)

// AllPipelineRepoModes is every PipelineRepoMode (pipeline_repos_mode).
var AllPipelineRepoModes = []PipelineRepoMode{
	PipelineRepoModeOff,
	PipelineRepoModeShadow,
	PipelineRepoModeOn,
}

// PipelineQueueState is the state of an enrolled repository's merge queue.
type PipelineQueueState string

const (
	PipelineQueueRunning  PipelineQueueState = "running"
	PipelineQueuePaused   PipelineQueueState = "paused"
	PipelineQueueDegraded PipelineQueueState = "degraded"
)

// AllPipelineQueueStates is every PipelineQueueState
// (pipeline_repos_queue_state).
var AllPipelineQueueStates = []PipelineQueueState{
	PipelineQueueRunning,
	PipelineQueuePaused,
	PipelineQueueDegraded,
}

// PipelineEmergencyLandPolicy is how a project answers a request to land a PR
// past the queue's gate.
type PipelineEmergencyLandPolicy string

const (
	// PipelineEmergencyLandAsk asks a person for each request.
	PipelineEmergencyLandAsk PipelineEmergencyLandPolicy = "ask"
	// PipelineEmergencyLandAutoAllow approves an eligible request, within the
	// project's daily cap.
	PipelineEmergencyLandAutoAllow PipelineEmergencyLandPolicy = "auto_allow"
	// PipelineEmergencyLandOff refuses every request.
	PipelineEmergencyLandOff PipelineEmergencyLandPolicy = "off"
)

// AllPipelineEmergencyLandPolicies is every PipelineEmergencyLandPolicy
// (pipeline_project_settings_emergency_land_policy).
var AllPipelineEmergencyLandPolicies = []PipelineEmergencyLandPolicy{
	PipelineEmergencyLandAsk,
	PipelineEmergencyLandAutoAllow,
	PipelineEmergencyLandOff,
}

// PipelineReleaseApproval is which release promotions need a person's
// approval.
type PipelineReleaseApproval string

const (
	PipelineReleaseApprovalNone     PipelineReleaseApproval = "none"
	PipelineReleaseApprovalPerEnv   PipelineReleaseApproval = "per_env"
	PipelineReleaseApprovalProdOnly PipelineReleaseApproval = "prod_only"
)

// AllPipelineReleaseApprovals is every PipelineReleaseApproval
// (pipeline_project_settings_release_approval).
var AllPipelineReleaseApprovals = []PipelineReleaseApproval{
	PipelineReleaseApprovalNone,
	PipelineReleaseApprovalPerEnv,
	PipelineReleaseApprovalProdOnly,
}

// PipelineSetting names a pipeline_project_settings column whose change is
// audited in pipeline_setting_changes.
type PipelineSetting string

const (
	PipelineSettingEmergencyLandPolicy   PipelineSetting = "emergency_land_policy"
	PipelineSettingEmergencyLandDailyCap PipelineSetting = "emergency_land_daily_cap"
	PipelineSettingReleaseApproval       PipelineSetting = "release_approval"
)

// AllPipelineSettings is every PipelineSetting
// (pipeline_setting_changes_setting).
var AllPipelineSettings = []PipelineSetting{
	PipelineSettingEmergencyLandPolicy,
	PipelineSettingEmergencyLandDailyCap,
	PipelineSettingReleaseApproval,
}

// PipelineChangedVia is the surface a person changed a pipeline setting
// from. Both are signed-in browser sessions; no machine credential may write
// a setting.
type PipelineChangedVia string

const (
	PipelineChangedViaWeb     PipelineChangedVia = "web"
	PipelineChangedViaDesktop PipelineChangedVia = "desktop"
)

// AllPipelineChangedVia is every PipelineChangedVia
// (pipeline_setting_changes_changed_via).
var AllPipelineChangedVia = []PipelineChangedVia{
	PipelineChangedViaWeb,
	PipelineChangedViaDesktop,
}
