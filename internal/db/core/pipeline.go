package core

// The pipeline's enrolment and settings vocabularies
// (db/migrations/20261125211730_pipeline_repos_and_settings.up.sql, widened
// by 20261126164236_pipeline_setting_changes_gate_stages.up.sql). Each
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

// PipelineSetting names a setting whose change is audited in
// pipeline_setting_changes: a pipeline_project_settings column, or one of the
// built-in gate stages a repository's pipeline.yaml turns off or back on
// (owner D13).
type PipelineSetting string

const (
	PipelineSettingEmergencyLandPolicy   PipelineSetting = "emergency_land_policy"
	PipelineSettingEmergencyLandDailyCap PipelineSetting = "emergency_land_daily_cap"
	PipelineSettingReleaseApproval       PipelineSetting = "release_approval"
	PipelineSettingGateStageLint         PipelineSetting = "gate.stage.lint"
	PipelineSettingGateStageTest         PipelineSetting = "gate.stage.test"
	PipelineSettingGateStageBuild        PipelineSetting = "gate.stage.build"
	PipelineSettingGateStageSBOMScan     PipelineSetting = "gate.stage.sbom_scan"
)

// AllPipelineSettings is every PipelineSetting
// (pipeline_setting_changes_setting).
var AllPipelineSettings = []PipelineSetting{
	PipelineSettingEmergencyLandPolicy,
	PipelineSettingEmergencyLandDailyCap,
	PipelineSettingReleaseApproval,
	PipelineSettingGateStageLint,
	PipelineSettingGateStageTest,
	PipelineSettingGateStageBuild,
	PipelineSettingGateStageSBOMScan,
}

// PipelineGateStageSettings are the settings that change only through
// config (PipelineChangedViaConfig), and the only ones config changes
// (pipeline_setting_changes_config, a CHECK the closed-set lint cannot
// read; TestPipelineSettingChangesGateStages pins the two equal).
var PipelineGateStageSettings = []PipelineSetting{
	PipelineSettingGateStageLint,
	PipelineSettingGateStageTest,
	PipelineSettingGateStageBuild,
	PipelineSettingGateStageSBOMScan,
}

// PipelineChangedVia is how a pipeline setting changed. A project setting
// changes only from a signed-in browser session (web, desktop); no machine
// credential may write one. A gate stage changes only through config.
type PipelineChangedVia string

const (
	PipelineChangedViaWeb     PipelineChangedVia = "web"
	PipelineChangedViaDesktop PipelineChangedVia = "desktop"
	// PipelineChangedViaConfig is a change to a repository's
	// .reliant/pipeline.yaml that landed on its base_branch. The actor is
	// the merging PR's author at the provider, with their Reliant user only
	// when one maps.
	PipelineChangedViaConfig PipelineChangedVia = "config"
)

// AllPipelineChangedVia is every PipelineChangedVia
// (pipeline_setting_changes_changed_via).
var AllPipelineChangedVia = []PipelineChangedVia{
	PipelineChangedViaWeb,
	PipelineChangedViaDesktop,
	PipelineChangedViaConfig,
}
