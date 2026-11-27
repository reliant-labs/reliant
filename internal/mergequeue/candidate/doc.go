// Copyright (c) 2025 Reliant Labs

// Package candidate builds a merge-queue batch's candidate: the commit the gate
// tests and the lander later reproduces on main, one GitHub squash merge at a
// time (design §3.2, steps 1-3).
//
// [Build] starts from main's tip M and squashes each entry's reviewed head onto
// the running candidate, in order, one commit per PR. Each commit is titled
// with the PR's title and carries the trailers `Reliant-PR: <repo>#<n>
// head=<sha>` and `Reliant-Batch: <id>`. After entry i it records T_i, the tree
// of entries 1..i squashed onto M: the lander checks tree(main) == T_i after
// each merge, so T_i must be exactly the tree GitHub's squash merge writes
// (gitport.Squash and fakegithub's equivalence test hold that).
//
// A conflict ejects the entry, with one exception. When the repository
// declares `regen` and every path the entry conflicts on with M matches its
// `generated:` globs, the builder checks out M merged with the head (conflicts
// taken from the head), runs regen there under `credentials: none`
// ([SandboxRunner], the same internal/credsandbox sandbox a `credentials: none`
// run node gets), and keeps the result only if [GeneratedOnly] holds: the
// regenerated tree differs from "M + the reviewed head" in generated paths
// alone. It then writes the "merge M + regen" commit (parents: the reviewed
// head, then M), pushes it to the PR's branch as its new head, and returns it
// in [Candidate.Regenerated] so the caller records that head's verdict under
// the rule `generated_only`. GitHub's squash merge of that head then applies
// cleanly.
//
// The candidate is pushed to refs/heads/reliant-queue/<batch-id>. Both pushes
// are idempotent: a retried Build adopts what an earlier attempt pushed when it
// is the same candidate (the same trees and trailers on M), and the same regen
// commit (the same tree on the same parents).
//
// The package holds no state and reads no database: it works over the ports
// (gitport, ghport) and the regen [Runner] it is handed. Nothing calls it yet;
// the queue's scheduler (M7) will.
//
//forge:exclude-contract: a stateless function (Build) over the ports it is handed; its seams are those ports and the Runner interface, not a service of its own
package candidate
