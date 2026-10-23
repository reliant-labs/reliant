package release

// Input is captured before build and never automatically rebased. Candidate is
// an isolated full inventory; unselected candidate drift is never deployed.
type CompositionInput struct {
	Base      *Snapshot
	Candidate Snapshot
	BaseRef   *SnapshotBase
	Selected  []ConsumerKey
}

// ClosureError refuses implicit selector widening. Required is the additional
// explicit selection; Reasons are deterministic and suitable for native plans.
type ClosureError struct {
	Required []ConsumerKey
	Reasons  []string
}
