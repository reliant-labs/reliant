package core

// DuePRPipeline is one pr_pipeline row the due scan found due, with the PR
// record it is about (the repository, number and head a record_due firing
// names).
type DuePRPipeline struct {
	Row    *PRPipeline
	Record *PRRecord
}
