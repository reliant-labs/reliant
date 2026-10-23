package release

import "fmt"

// Validate checks a builder receipt's effective input and output contract. It
// does not establish receipt authenticity or authorize automatic reuse: callers
// must verify origin authority and require Inputs.Cacheable separately.
func (r BuildReceipt) Validate() error {
	d, err := BuildInputDigest(r.Inputs)
	if err != nil {
		return err
	}
	if d != r.InputDigest {
		return fmt.Errorf("%w: receipt input digest mismatch", ErrInvalid)
	}
	if len(r.Outputs) != len(r.Inputs.Outputs) {
		return fmt.Errorf("%w: receipt output contract mismatch", ErrInvalid)
	}
	for _, name := range r.Inputs.Outputs {
		if name == "" || !ValidDigest(r.Outputs[name]) {
			return fmt.Errorf("%w: missing/invalid receipt output %s", ErrInvalid, name)
		}
	}
	if r.WorkingDirectory != nil && !ValidObjectID(r.WorkingDirectory.Commit) {
		return fmt.Errorf("%w: unresolved receipt working directory", ErrInvalid)
	}
	return nil
}
