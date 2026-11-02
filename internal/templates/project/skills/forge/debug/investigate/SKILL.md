---
name: investigate
description: Investigate bugs — hypothesis formation, code tracing, and common Forge failure patterns.
---

# Investigate Bugs

## When to Use

You have a bug report with no obvious code path — you need to form and rank hypotheses before diving in.

## Investigation Workflow

1. **Understand the bug**: expected vs actual, frequency, environment, recent changes
2. **Trace code paths**: grep for the affected feature, follow the call chain from API endpoint → handler → service → DB
3. **Check recent changes**: `git log` on affected files, look for related commits
4. **Form 2–3 ranked hypotheses** with confidence percentage
5. **Verify each**: read code carefully, check edge cases, look for the patterns below

## Common Forge Bug Patterns

**Stale generated code**
Forgot `forge generate` after a proto change. Run it and retest:
```
forge generate
task test
```

**Nil proto fields**
Optional proto fields accessed without nil checks. Look for bare field access on messages that could be empty.

**Wrong Connect error codes**
Returning `Internal` when it should be `InvalidArgument`, `NotFound`, or `PermissionDenied`. Check handler return paths.

**Race conditions in handlers**
Shared state modified without synchronization. Look for package-level vars, map writes, or shared slices accessed from handlers.

**Migration / codegen drift**
Code expects a column or table that hasn't been migrated yet, or references generated symbols that are stale after a proto/schema change. Run `forge project audit` to surface the mismatch and `forge generate` to refresh — do this *before* chasing the code when a symbol is "undefined" right after a schema change.

**Missing error checks on Connect calls**
Frontend calling Connect endpoints without handling error responses. Check for unchecked `.error` on client responses.

## Runtime Evidence to Rank Hypotheses

Code reading ranks hypotheses; runtime evidence confirms them. The fast forge tools:

- **`forge project introspect handlers`** — localize: if the failing RPC isn't in the assembled binary's list, the fault is a downstream/remote hop, not this code. This often kills several hypotheses at once.
- **`forge api curl <service.method>`** — exercise the endpoint from the shell to confirm the symptom (stops at the auth interceptor — no token minting).
- **`forge cluster logs --service <name>`** — read the server's actual logs for the request (kubectl-backed; owner cluster only — use `kubectl --context <other>` for a peer in another cluster).
- **`forge debug start <svc>`** — attach Delve when logs aren't enough. Caveats: in a multi-binary repo `start <service>` can mis-build (falls back to `./cmd/...`) so pass an explicit path; and `forge debug stop` after `--attach` kills the live process.

## Output Format

Present findings as:
- **Hypothesis 1** (confidence %): description, file:line evidence
- **Hypothesis 2** (confidence %): description, file:line evidence
- **Suggested fix approach** for handoff to implementer

Do not fix the bug in investigation mode — hand off with clear evidence.

**Note for the implementer's handoff:** a green `forge env smoke` / `forge env status <env>` does NOT prove the app flow works (they check listeners/compose/telemetry, not app-flow invariants). The fix is only proven by a declarative, exit-coded app-health assertion (model: a project `doctor:<flow>` task) plus a full `task test:e2e`.
