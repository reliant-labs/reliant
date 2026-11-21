package deploytarget

import (
	"errors"

	"github.com/reliant-labs/forge/internal/cluster"
)

// A FRONTEND THAT FAILS BEFORE ITS HOST IS WRITTEN SHIPPED NOTHING.
//
// A static frontend deploy is a long local prelude — install, build, assemble,
// file source maps, carry the live release's assets — and then the one step
// that writes the host: `firebase deploy`, or the bucket upload. A failure in
// the prelude leaves the previous release serving, untouched. Reported as a
// stage that "failed part-way; some of it may be live", it sent an operator to
// verify a site nothing had written to (2026-10-19, control-plane prod:
// reliant-web's `npm run build` failed, and the deploy said PARTLY SHIPPED).
//
// The deploy's ship log already reads cluster.ErrNothingApplied as "this
// stage changed nothing", so the prelude's failures carry it.

// notShippedError is a frontend failure decided before anything was written to
// its host. Its message is the failure's own; only errors.Is can tell.
type notShippedError struct{ err error }

func (e notShippedError) Error() string { return e.err.Error() }

func (e notShippedError) Unwrap() []error { return []error{e.err, cluster.ErrNothingApplied} }

// shippedNothing marks err as a failure that wrote nothing to the host.
func shippedNothing(err error) error {
	if err == nil {
		return nil
	}
	return notShippedError{err: err}
}

// afterShipping is err once an earlier frontend of the same group has already
// gone live: the group changed its host, so the failure is no longer "nothing
// shipped", whatever this frontend reached.
func afterShipping(err error) error {
	var ns notShippedError
	if errors.As(err, &ns) {
		return ns.err
	}
	return err
}
