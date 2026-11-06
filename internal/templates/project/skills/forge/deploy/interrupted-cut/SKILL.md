---
name: deploy/interrupted-cut
description: A release cut whose images were pushed but whose ledger record was never written (the machine died in between) — re-running the same `forge env build <env> --release <v>` from the same clean commit and forge resumes it from its cut journal, rebuilding nothing; `forge env releases interrupted <env> <v>` says whether it can. Use when a cut retry fails with "already tagged", "Registry tags are immutable" or "an earlier cut … was never recorded".
---

# Resuming an interrupted cut

`forge env build <env> --release <v>` pushes every image the env declares under
the tag `<v>`, then records the release in the env's ledger. The two writes are
not atomic. A machine that dies between them leaves `<v>`'s image tags in the
registry (immutable) and no release in the ledger, so a plain retry fails at its
first push and the version looks burned.

It is not. After its last push and before its record, the cut writes a **cut
journal**: an OCI artifact tagged `<v>.forge-cut` in the first repository the
env pushes to (sorted). It records the commit, tree and forge that built the
cut, every image digest it pushed, and the exact build state the record
harvests.

## Resume it: run the same command again

```
forge env releases interrupted prod v1.7.94-rc.1     # can this checkout resume it? (exit 0 yes)
forge env build prod --release v1.7.94-rc.1          # resumes: records the release, builds nothing
```

The retry finds the journal before building and resumes when ALL hold:

| check | why |
|---|---|
| this checkout is the journal's commit (and tree), and clean | only then do the tagged bytes provably come from this source |
| this is the journal's forge version | the state it restores is that forge's format |
| every version tag still names its recorded digest; every reused image's digest still exists | the release pins what the registry serves |
| each ShellBuild sibling checkout here is the recorded repository and commit, clean | the cut's source checks run against it |
| the env declares no build-only binaries | those are not in the registry to recover |

It then restores the build state and runs the ordinary cut over it: the same
coverage, source and migration checks, the same record and bundle. The
sibling checkout may be in another directory than the interrupted cut's (a
fresh worktree after a reboot): it is matched by repository and commit.

Anything else is refused before anything is built, naming each mismatch, with
the remedy: resume from the checkout the journal names, or cut the next version.

## When there is no journal

A cut interrupted DURING its pushes (some tags written, no journal), or one
made by a forge that predates journals, cannot be resumed: nothing proves the
tags' source. Cut the next version. A journal that could not be written is a
warning in the cut's log, never a failure.
