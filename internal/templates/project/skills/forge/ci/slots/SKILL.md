---
name: ci/slots
description: Host-wide heavy slots — why heavy work (test lanes, builds, codegen, full lints, gate checks) queues on a shared machine, `forge slot run` / `forge slot status`, FORGE_HEAVY_SLOTS / FORGE_SLOTS_DIR / FORGE_SLOT_HELD, which forge commands take a slot on their own, and how nesting avoids deadlock. Use when a command prints "waiting for a heavy slot", when sizing a shared machine, or when wrapping your own heavy task.
---

# Host-wide heavy slots

On a shared machine — several agents, worktrees or terminals — the cost of a
heavy job is set by how many others run beside it. Measured on a 16 vCPU /
32 GiB workspace VM: `go build ./...` took 36–50 s beside 0–3 other heavy jobs
and 101–155 s beside 8 or more; a full local gate went from ~40 min to ~2.3 h.
The loss was memory-cgroup reclaim, so past a handful of concurrent heavy jobs
EVERY job slows down. Running them all at once finishes later than queueing
them, so forge queues them.

A **slot** is an exclusive OS file lock on one of N files (`pkg/hostslot`). The
OS drops the lock when the holder exits however it exits, so a crashed or
killed holder never strands a slot and there is nothing to clean up. A waiter
prints one line naming the holders (pid, command, age), repeats it every 30s,
and queues FIFO-ish behind earlier waiters: a newcomer cannot overtake a queue.

## Who takes a slot

| Holder | Why |
| --- | --- |
| `task test`, `task test:integration` (the `SLOT` prefix in Taskfile.yml) | whole-module test lanes |
| `forge generate` (not `--plan`; `--watch` only for its first run) | buf, the schema projection's postgres, a validating compile of every package |
| `forge lint` | golangci-lint plus every frontend's eslint and tsc |
| `forge ci gate`'s `unit-tests`, `integration-tests` and `verify-generated` | through the rows above: they run `task test`, `task test:integration` and `forge generate` |

`task test:short` takes none: the scoped inner loop is not heavy and must not
queue. `forge env up` and `forge build` take none either: a dev loop must not
wait behind someone's gate.

## Commands and knobs

| | |
| --- | --- |
| `forge slot run --class heavy -- <cmd>` | run `<cmd>` holding a slot, exit with its status. Ctrl-C while waiting gives up (130) |
| `forge slot status [--json]` | the count, where it came from, the holders, the queue |
| `FORGE_HEAVY_SLOTS=<n>` | override the count. Default min(CPUs / 2, memory GiB / 8), read from the cgroup v2 limit when there is one (inside a VM sandbox or container that is the truth, not nproc / MemTotal), at least 1 |
| `FORGE_HEAVY_SLOTS=off` | switch the limit off: nothing waits (a CI runner that runs one job per machine may) |
| `FORGE_SLOTS_DIR` | relocate the slot files (default: the user cache dir, so one user's processes share them; point every user of a multi-user host at one mode-1777 directory to share across users) |

Spell `--class heavy` in scripts and Taskfiles: a forge that predates the verb
rejects the flag, where a bare `forge slot run -- <cmd>` could be read as a
request for help and exit 0 without running `<cmd>`.

## Nesting never deadlocks

A process already inside a slot never waits for another of the class. A gate
check holding a slot that runs `forge generate` would otherwise deadlock as
soon as every slot is held by parents waiting on their children. Two signals
mark a process as inside a slot:

- `FORGE_SLOT_HELD` in its environment — the holder passes it to its children.
  It names the slot and a nonce; a token whose slot was since released or
  reassigned is ignored, so a copied or stale value grants nothing.
- an ANCESTOR holding a slot as a wrapper (`forge slot run`, the gate), read
  from the kernel's parent links — this covers a child whose environment was
  scrubbed on the way down.

So never wrap `forge ci gate` (or `task ci`) in `forge slot run`: every check
inside it would count as inside that one slot and skip the queue.

## Using it from Go

```go
slot, err := hostslot.Acquire(ctx, hostslot.Heavy, hostslot.Options{Out: os.Stderr})
if err != nil {
	return err // only ctx ending, or a slot directory that cannot be created
}
defer slot.Release()
cmd.Env = append(os.Environ(), slot.Env()...) // children run inside this slot
```

Set `Options.Subtree` when the work runs in child processes (a wrapper or a
scheduler), so descendants count as inside the slot even if their environment
loses `FORGE_SLOT_HELD`. Leave it unset for a process that does the work
itself — a long-lived host must not exempt every unrelated child it spawns.
