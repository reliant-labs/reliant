# Local startup dependencies

A long-running workload on `forge.OnHost` may declare `startup_after` to wait
for another host workload or a frontend dev server before its runner starts:

```kcl
runtime = forge.OnHost {
    listen_ports = []
    startup_after = ["reliant-web"]
}
```

Names refer to rendered workload/frontend names, not process labels or ports.
This is an additive `forge env up` contract; omitted or empty lists preserve the
usual host-then-frontend ordering. Infrastructure and one-shot jobs still finish
before local long-running processes start. Frontends may be dependency targets,
but do not declare `startup_after` themselves. Cluster workloads, jobs, tools,
and compose services are not valid targets of these edges.

Forge validates missing names, ambiguous names and cycles before launching.
`--target` remains strict: selecting a consumer without its dependency fails
and asks you to select both. Dependencies are not silently added, and an old
process is not adopted to satisfy an excluded dependency. A selected graph is
started in declaration order except where an explicit edge requires a dependency
to start first. Independent processes retain their existing order.

Each dependency must have its declared ports listening under this Forge run's
ownership before its consumer starts. Frontends use their resolved dev-server
port. Each dependency wait is bounded by `--host-ready-timeout` (default two
minutes), and cancellation interrupts the wait. This is a per-wait budget, not
a global wall-clock startup budget. Immediately before launching each consumer,
Forge takes a fresh aggregate readiness snapshot of all its dependencies; any
observed exit or lost owned listener fails startup without a new wait budget. An observed runner exit, including exit 0,
is a failure for a long-running dependency even if a descendant still holds its
port. Failed launches stop dependent startup. Already-started processes remain
tracked: supervising runs clean them up on failure; detached runs can be stopped
with `forge env down`.

`listen_ports = []` continues to mean **this process binds no ports**. Do not
list a frontend's port on a desktop shell merely because it connects there.
A portless runner is checked for observed exits and reported as
`running (process only)`, not application-ready. This is not an Electron renderer
handshake or a promise that future startup cannot fail. Applications must propagate
launch failures and keep their runner alive for the actual application lifetime.
Startup dependencies do not add ongoing restart/supervision policy.
