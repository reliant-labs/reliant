---
name: reproduce
description: Reproduce bugs — diagnostic logging, runtime evidence collection, and e2e reproduction.
---

# Reproduce Bugs

## When to Use

The bug requires runtime evidence — you can't determine the cause from code reading alone.

## System Discovery

1. Check `forge env up dev` output for service ports and startup logs
2. Find API routes in `proto/` definitions
3. Identify the service and handler involved in the bug

## Diagnostic Logging

Mark all debug logging clearly so it can be cleaned up:

```go
// DEBUG-REPRO: Remove after fixing [description]
```

What to log, in order of placement:
1. **Inputs** at function entry
2. **State** before and after mutations
3. **Branches** — which decision path was taken
4. **Errors** with full context (wrapped, not bare)
5. **Exit** values and final state

## Triggering the Bug

- **First confirm the wire.** Before blaming the code, verify the caller can actually reach the target: is the service up, is the kubectl context current (services may live in different clusters), are credentials live? Stale connection state is a top false-cause.
- **Read the actual runtime logs.** `forge cluster logs --service <name>` tails the pod's logs (kubectl-backed) — read the server's view of the request, don't infer from the client error alone. It tails only the owner cluster; for a peer in another cluster, `kubectl --context <other> -n <ns> logs <pod>`.
- **Localize first with `forge project introspect handlers`.** It prints every RPC path the assembled binary serves. If the RPC you're triggering isn't in the list, the fault is a downstream/remote hop — stop digging in this binary.
- **Hit the endpoint with `forge api curl <service.method>`.** Builds a copy-pasteable Connect request from the shell. It stops at the auth interceptor (no token minting), so add your own credential for an authed call.
- Exercise via Connect client; query DB state before and after the operation.
- **Attach Delve with `forge debug start <svc>`** if logging isn't enough. Two caveats: in a multi-binary repo `start <service>` can mis-build (it falls back to `./cmd/...`) — pass an explicit path/service; and `forge debug stop` after `--attach` kills the live process, so detach rather than `stop` when the process must keep running.

## E2E Reproduction

Write a minimal test under `e2e/` that triggers the bug against the live stack:

```
task test:e2e
```

The test should assert on the **wrong** behavior first (red), then flip to expected after fix.

## Prove the Fix — Don't Trust a Green Smoke

A green `forge env smoke` / `forge env status <env>` does NOT mean the app flow works — they check listeners, local compose, and telemetry, not app-flow invariants. The only things that **prove** an app-flow fix:

1. a declarative, exit-coded app-health assertion that fails non-zero when the invariant is violated (model: a project `doctor:<flow>` task), and
2. a full `task test:e2e`.

Add or extend one of these so the fix stays proven, not just observed-once.

## Evidence Format

Collect and present:
- Environment (OS, Go version, service versions)
- Exact trigger command or request
- Observed vs expected behavior
- Relevant log lines
- State changes (DB rows, cache entries)

## User Handoff

If you can't reproduce programmatically, provide exact manual steps and specify what logs to collect.

## Cleanup

Always grep for `DEBUG-REPRO` markers and remove them after the bug is fixed.
