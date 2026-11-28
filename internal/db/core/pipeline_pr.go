package core

// The PR pipeline's per-PR vocabularies
// (db/migrations/20261126105949_pr_pipeline_tables.up.sql). Each All* set is
// mirrored by a CHECK that declares it with forge:closed-set, so
// `forge lint --closed-sets` fails the moment the two drift: a value added
// here needs a migration widening its CHECK.

// PRPipelineState is where a PR of an enrolled repository is in the
// pipeline's state machine (pr_pipeline.state).
type PRPipelineState string

const (
	PRPipelineDraft            PRPipelineState = "draft"
	PRPipelineAwaitingReview   PRPipelineState = "awaiting_review"
	PRPipelineReviewing        PRPipelineState = "reviewing"
	PRPipelineChangesRequested PRPipelineState = "changes_requested"
	PRPipelineFixing           PRPipelineState = "fixing"
	PRPipelineApproved         PRPipelineState = "approved"
	PRPipelineQueued           PRPipelineState = "queued"
	PRPipelineGating           PRPipelineState = "gating"
	PRPipelineLanded           PRPipelineState = "landed"
	PRPipelineNeedsPerson      PRPipelineState = "needs_person"
	PRPipelineClosed           PRPipelineState = "closed"
)

// AllPRPipelineStates is every PRPipelineState (pr_pipeline_state).
var AllPRPipelineStates = []PRPipelineState{
	PRPipelineDraft,
	PRPipelineAwaitingReview,
	PRPipelineReviewing,
	PRPipelineChangesRequested,
	PRPipelineFixing,
	PRPipelineApproved,
	PRPipelineQueued,
	PRPipelineGating,
	PRPipelineLanded,
	PRPipelineNeedsPerson,
	PRPipelineClosed,
}

// PRPrioritySource is what set a PR's effective priority.
type PRPrioritySource string

const (
	PRPriorityDefault  PRPrioritySource = "default"
	PRPriorityIncident PRPrioritySource = "incident"
	PRPriorityRelease  PRPrioritySource = "release"
	PRPriorityDecision PRPrioritySource = "decision"
	PRPriorityAging    PRPrioritySource = "aging"
)

// AllPRPrioritySources is every PRPrioritySource
// (pr_pipeline_priority_source).
var AllPRPrioritySources = []PRPrioritySource{
	PRPriorityDefault,
	PRPriorityIncident,
	PRPriorityRelease,
	PRPriorityDecision,
	PRPriorityAging,
}

// PRHeadVerdict is the review role's verdict on one head.
type PRHeadVerdict string

const (
	PRHeadVerdictPass    PRHeadVerdict = "pass"
	PRHeadVerdictChanges PRHeadVerdict = "changes"
	PRHeadVerdictClose   PRHeadVerdict = "close"
)

// AllPRHeadVerdicts is every PRHeadVerdict (pr_head_verdicts_verdict).
var AllPRHeadVerdicts = []PRHeadVerdict{
	PRHeadVerdictPass,
	PRHeadVerdictChanges,
	PRHeadVerdictClose,
}

// PRVerdictRule is how a head's verdict was reached.
type PRVerdictRule string

const (
	// PRVerdictRuleModel is a model reviewer's verdict.
	PRVerdictRuleModel PRVerdictRule = "model"
	// PRVerdictRuleNoModelLowRisk passes a low-risk diff without a model.
	PRVerdictRuleNoModelLowRisk PRVerdictRule = "no_model_low_risk"
	// PRVerdictRuleExactRevert passes an exact revert of a landed change.
	PRVerdictRuleExactRevert PRVerdictRule = "exact_revert"
	// PRVerdictRuleGeneratedOnly passes a head whose diff from a reviewed
	// head touches only generated paths (the queue's regen).
	PRVerdictRuleGeneratedOnly PRVerdictRule = "generated_only"
)

// AllPRVerdictRules is every PRVerdictRule (pr_head_verdicts_rule).
var AllPRVerdictRules = []PRVerdictRule{
	PRVerdictRuleModel,
	PRVerdictRuleNoModelLowRisk,
	PRVerdictRuleExactRevert,
	PRVerdictRuleGeneratedOnly,
}

// PRRiskTier is the risk class of a head's diff.
type PRRiskTier string

const (
	PRRiskLow    PRRiskTier = "low"
	PRRiskMedium PRRiskTier = "medium"
	PRRiskHigh   PRRiskTier = "high"
)

// AllPRRiskTiers is every PRRiskTier (pr_head_verdicts_risk_tier).
var AllPRRiskTiers = []PRRiskTier{
	PRRiskLow,
	PRRiskMedium,
	PRRiskHigh,
}

// PipelineAttemptSubjectKind is what a pipeline attempt is about.
type PipelineAttemptSubjectKind string

const (
	PipelineAttemptSubjectPR       PipelineAttemptSubjectKind = "pr"
	PipelineAttemptSubjectIncident PipelineAttemptSubjectKind = "incident"
	PipelineAttemptSubjectBatch    PipelineAttemptSubjectKind = "batch"
)

// AllPipelineAttemptSubjectKinds is every PipelineAttemptSubjectKind
// (pipeline_attempts_subject_kind).
var AllPipelineAttemptSubjectKinds = []PipelineAttemptSubjectKind{
	PipelineAttemptSubjectPR,
	PipelineAttemptSubjectIncident,
	PipelineAttemptSubjectBatch,
}

// PipelineAttemptStage is the pipeline stage an attempt ran.
type PipelineAttemptStage string

const (
	PipelineAttemptReview  PipelineAttemptStage = "review"
	PipelineAttemptFix     PipelineAttemptStage = "fix"
	PipelineAttemptGate    PipelineAttemptStage = "gate"
	PipelineAttemptRespond PipelineAttemptStage = "respond"
	// PipelineAttemptClose is a close of the PR, with its evidence's
	// signature: a PR a person reopened is never closed again for the same
	// evidence.
	PipelineAttemptClose PipelineAttemptStage = "close"
)

// AllPipelineAttemptStages is every PipelineAttemptStage
// (pipeline_attempts_stage).
var AllPipelineAttemptStages = []PipelineAttemptStage{
	PipelineAttemptReview,
	PipelineAttemptFix,
	PipelineAttemptGate,
	PipelineAttemptRespond,
	PipelineAttemptClose,
}

// PipelineAttemptOutcome is how a finished attempt ended. A running attempt
// has none.
type PipelineAttemptOutcome string

const (
	PipelineAttemptSucceeded PipelineAttemptOutcome = "succeeded"
	PipelineAttemptFailed    PipelineAttemptOutcome = "failed"
	// PipelineAttemptUnproven could not prove a result either way (§3.4).
	PipelineAttemptUnproven PipelineAttemptOutcome = "unproven"
	// PipelineAttemptSuperseded was overtaken by a newer head or run.
	PipelineAttemptSuperseded PipelineAttemptOutcome = "superseded"
	PipelineAttemptCancelled  PipelineAttemptOutcome = "cancelled"
)

// AllPipelineAttemptOutcomes is every PipelineAttemptOutcome
// (pipeline_attempts_outcome).
var AllPipelineAttemptOutcomes = []PipelineAttemptOutcome{
	PipelineAttemptSucceeded,
	PipelineAttemptFailed,
	PipelineAttemptUnproven,
	PipelineAttemptSuperseded,
	PipelineAttemptCancelled,
}
