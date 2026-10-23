package release

import "fmt"

// ValidateSnapshotTransition independently verifies that candidate is a complete
// legal transition from the recorded base, including carry bytes, provenance,
// closure and exact compatibility subjects. Servers must fetch and verify the
// recorded base bundle and bind candidate.Base to its promotion under the lock.
// Reports' authenticity and policy authority are additional admission checks.
func ValidateSnapshotTransition(base *Snapshot, candidate Snapshot) error {
	recomposed, err := ComposeSnapshot(CompositionInput{Base: base, Candidate: candidate, BaseRef: candidate.Base, Selected: candidate.Selected})
	if err != nil {
		return err
	}
	want, err := candidate.Digest()
	if err != nil {
		return err
	}
	actual, err := recomposed.Digest()
	if err != nil {
		return err
	}
	if want != actual {
		return fmt.Errorf("%w: submitted snapshot changes unselected inventory or closure/decisions", ErrInvalid)
	}
	return nil
}
