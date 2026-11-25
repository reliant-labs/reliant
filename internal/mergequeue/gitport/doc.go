// Copyright (c) 2025 Reliant Labs

// Package gitport is the merge queue's local git: the operations the candidate
// builder (Q2) and the reverter (Q6) run in a workdir, over the real git
// binary.
//
// Everything works on a bare repository with no checkout. A squash is
// `git merge-tree --write-tree` (git's merge-ort, in memory) followed by
// `git commit-tree`, so building a candidate of n PRs writes objects and never
// touches a worktree or an index. That needs git 2.40 or newer
// (`merge-tree --merge-base`); [Open] and [Init] refuse an older git with
// [ErrGitTooOld] rather than fall back to a checkout-based merge that would
// compute a different tree.
//
// Git runs hermetically: the system and global config are not read
// (GIT_CONFIG_NOSYSTEM, GIT_CONFIG_GLOBAL=/dev/null), so a host's merge
// settings, hooks or credential helpers cannot change a tree the queue
// computes. Credentials for a push or fetch are the caller's, passed through
// [Options.Env] (an askpass helper, or GIT_CONFIG_COUNT/KEY/VALUE for an
// http.extraHeader); never put a token in a URL or an argument.
//
//forge:not-a-component: built per batch in a workdir by the merge queue's candidate builder and reverter (Q2, Q6), never by the server's composition root
package gitport
