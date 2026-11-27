package core

import "github.com/reliant-labs/forge/pkg/svcerr"

// What the pipeline's status reads return (PipelineService, build plan task
// M2), beside the rows in pipeline_rows.go.

// ReasonProjectPipelineEnrolled is the x-forge-error-reason of
// ErrProjectPipelineEnrolled.
const ReasonProjectPipelineEnrolled = "pipeline_enrolled"

// ErrProjectPipelineEnrolled refuses moving a project to another org while
// one of its repositories is enrolled in the pipeline with a mode other than
// off: its pipeline rows carry the org (pipeline redesign §13, G1). Setting
// the mode off is unenrolling.
var ErrProjectPipelineEnrolled = svcerr.WithReason(svcerr.FailedPrecondition(
	"a repository of this project is enrolled in Reliant's pipeline; unenroll first "+
		"(reliant pipeline mode <owner/repo> off), then move the project"), ReasonProjectPipelineEnrolled)

// PRPipelineStateCount is how many of a repository's PRs are in State.
type PRPipelineStateCount struct {
	State PRPipelineState
	Count int
}

// QueueEntryListing is a queue entry with its pull request's number.
type QueueEntryListing struct {
	QueueEntry
	Number int
}
