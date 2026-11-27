// Copyright (c) 2025 Reliant Labs

// Package jobrun is the server side of the merge queue's code operations:
// it runs each one as a job on a daemon of the repository's gate pool and
// checks what the daemon reports. The server holds no repository bytes
// (owner, 2026-11-27): it runs no git and reads no repository contents
// (internal/architecture's TestServerRolesRunNoCode), so this package sends
// buildjob requests and receives ids, paths and bytes.
//
//   - [Builder] is the queue's mergequeue.Builder: Build builds a batch's
//     candidate (a build job) and ChangedPaths lists a head's changed paths
//     (a paths job).
//   - [Files] lists a head's changed files with statuses, line counts and
//     blobs (a paths job with stats), for pr.next's risk tier, so GitHub's
//     pull request files list, which carries every file's patch, is never
//     read on the worker.
//   - [ConfigReader] is prrecords.Contents: it reads .reliant/pipeline.yaml
//     at a commit (a config job). The server parses and validates the bytes;
//     the daemon never reports a resolved config.
//
// A job goes to the pool named by the repository's recorded `gate.pool`
// (default `gate`) of the user who enrolled it (pipeline_repos
// created_by_user_id), on the first connected, eligible member that takes it,
// over the daemon command path (buildjob.CommandStart, then
// buildjob.CommandStatus until it is done). A member running an older
// Reliant, which does not know the commands, counts as one that refused the
// job; the next member is tried.
//
// A daemon's report is a claim. Before Build returns, the candidate it
// reports is checked through the GitHub API (metadata only): the batch's
// queue ref points at the reported commit, the commit chain from it down to
// the base is one single-parent commit per included entry whose trees are
// the reported trees T_1..T_n, and a regenerated entry's new head is the
// PR's head and a commit on the reviewed head and the base. A report that
// does not hold is refused with [ErrUnverified].
//
//forge:exclude-contract: the queue's scheduler and the PR record projector call it through their own consumer-side interfaces (mergequeue.Builder, prrecords.Contents); its seams are the consumer-side Deps interfaces
package jobrun
