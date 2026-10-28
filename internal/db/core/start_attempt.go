package core

import "time"

// StartAttemptDeadline bounds one Temporal start of a root workflow. The
// launcher measures it from a moment taken BEFORE the attempt is durably
// stamped (the launch transaction, or the claim of a retry), and the
// ExecuteWorkflow call carries it as its context deadline, so no start RPC
// issued for an attempt outlives StartAttemptDeadline past its stamp.
const StartAttemptDeadline = time.Minute

// StartAttemptBound is how long after its last start attempt a root may be
// treated as having no start in flight. It is the one bound both the
// reconciler's stranded-start sweep and workflow-capacity recovery use.
//
// It must outlast any start still in flight: StartAttemptDeadline on the
// client, plus the time Temporal's server may still commit an accepted start
// after the client's deadline expired (bounded by its own persistence
// timeouts, seconds). The four minutes between the two leave that margin,
// and both sides measure elapsed time on their own clock (the database's
// now() against its own stamp, the launcher's monotonic deadline), so clock
// skew between hosts does not erode it.
//
// The cost of the bound is the other side of the trade-off: a launch whose
// starter died between the stamp and the start reads as in flight until the
// bound passes. That is chosen over a shorter bound because the alternative
// failure, a run admitted after its slot was released, is the one a bound
// exists to prevent.
const StartAttemptBound = 5 * time.Minute
