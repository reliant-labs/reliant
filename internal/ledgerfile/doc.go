// Package ledgerfile is the MACHINE-SCOPED file ledger: one model, the
// non-hosted store.
//
// It holds exactly the records the hosted control plane holds
// (forge/pkg/release: Release, EnvRecord, Promotion, Gate, BundleRecord,
// Apply, ApplyOutcome, LocalSession), serializes them as the SAME canonical
// JSON, and runs the SAME rules (release.Decide, the caller's
// compare-and-set, release.CheckRecut). A line in one of these files is
// exactly what the control plane's ImportLedger accepts, which is what makes
// `forge ledger import --from-file-ledger` a copy rather than a translation.
// See pkg/release's package doc for the one-model-two-backends doctrine.
//
// NOT IN THE CHECKOUT, AND THAT IS THE POINT. The retired backend kept
// promotions in .forge/promotions/<env>.jsonl inside the source tree, which
// is circular by construction: the commit recording "prod runs v1.5.13"
// cannot be in v1.5.13, because it is written after v1.5.13 was cut. It was
// also branch-dependent, so two worktrees of one project saw two different
// histories of the same environment, and a checkout that had not pulled the
// latest ledger commit computed every verdict against a stale truth.
//
// So the ledger is keyed by PROJECT, not by directory:
//
//	$FORGE_LEDGER_HOME (default ~/.forge/ledger)/<project-id>/
//	  lock                  flock(2); held for every read-decide-append
//	  envs.jsonl            EnvRecord, append-only; the newest per env wins
//	  releases.jsonl
//	  promotions/<env>.jsonl
//	  gates/<env>.jsonl     the post-promote evidence the old file ledger could not hold
//	  bundles.jsonl         BundleRecord (the blob is in ./oci/)
//	  hosted-cache/<env>.jsonl  last-known state of a HOSTED env's ledger (--offline-ledger)
//	  hosted-queue/<env>.jsonl  promotions made offline, awaiting replay
//	  applies/<env>.jsonl   Apply and ApplyOutcome lines; outcome joined by apply id
//	  sessions.jsonl        LocalSession heartbeats are COMPACTED (the only non-append file)
//	  verifications.jsonl   reserved for source verification (deferred with §9 to #516)
//	  oci/                  OCI image layout: bundles, content-addressed
//
// Every worktree of a project on this machine therefore shares one ledger,
// and "what does prod run" stops depending on which branch is checked out.
//
// ONE WRITER AT A TIME. The retired backend documented a race rather than
// closing it: two writers could both decide from the same history and both
// append. Here every read-decide-append runs under flock(2) on ./lock, so
// release.Decide and the caller's CAS see the history that is actually on
// disk at the moment of the write. flock is tied to the open file
// description, so the kernel releases it even if the process dies.
//
// THE LOCK DOES NOT SPAN MACHINES, AND THIS PACKAGE SAYS SO. $FORGE_LEDGER_HOME
// on NFS or SMB is REFUSED at open rather than accepted with a caveat: an
// advisory lock on a network filesystem is exactly the shape of failure that
// looks like it works until two machines promote at once. A team's answer is
// a control plane (which is itself a forge project), not a synced directory.
package ledgerfile
