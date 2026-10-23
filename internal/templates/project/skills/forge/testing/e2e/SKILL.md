---
name: e2e
description: "End-to-end tests — full stack, multi-service flows, browser testing."
---

# End-to-End Tests

## Overview

E2E tests exercise the **full running stack** — real services, real database, real network calls between services. They live in the `e2e/` directory and validate multi-service flows as a user or client would experience them.

## Prerequisites

The full stack must be running before you execute e2e tests. Don't hand-roll the stack with `kubectl apply` or a bespoke compose file — `forge env up <env>` builds every service, brings up the compose-managed infra, and deploys each service to its declared target in one command:

```bash
forge env up dev     # in one terminal — builds + infra + deploys all services
task test:e2e         # in another terminal — runs e2e suite
```

If your services span multiple clusters, you don't script that yourself: each workload's binding names its own `forge.ClusterTarget` (its `cluster` field is its kubectl context), and `forge env up` routes each workload to its context. A cross-cluster e2e flow is just real Connect clients talking to services that happen to live in different contexts.

## Running

```bash
task test:e2e                          # all e2e tests (already verbose)
task test:e2e -- ./e2e/<name>/...      # target a specific service's e2e suite
task test:e2e -- -run TestCheckout ./e2e/...   # one flow
```

## Multi-Service Flow Testing

E2E tests verify cross-service behavior over **real Connect RPC** calls. A typical test might:

1. Create a resource via Service A
2. Verify it propagates to Service B
3. Trigger a workflow and assert the final state

Use real Connect clients pointed at the running stack — mock nothing internal at this level. The only legitimate mock in an e2e test is a third-party boundary you don't own (a payment sandbox, an upstream provider); everything inside your system stays real.

**Auth at the e2e tier:** the stack enforces authentication in every mode, so an e2e flow needs a real token — mint one signed with the key the server validates against (`jwt_secret`, or the IdP behind `jwt_jwks_url`) and reuse it across the flow's clients. Assert the negative too: the same call without a token, or with a tampered one, must come back `Unauthenticated`.

## Determinism Requirements

E2E tests are the most flake-prone level. Follow these rules strictly:

- **No ordering assumptions** — tests must pass in any execution order
- **No fixed sleeps as a sync primitive** — `time.Sleep()` to "wait for it to settle" is always wrong. Poll the real state with a short interval (50–100ms) and a tight overall timeout (a few seconds), and fail with the last observed state. Once the infra is up, e2e tests should be *fast* — they wait for convergence, not for a wall-clock guess.
- **`t.Cleanup()` for everything** — every resource created must be cleaned up
- **Idempotent setup** — running the same test twice must produce the same result

## Debugging Failures

When an e2e test fails, the stack is still running. You can attach a debugger:

```bash
forge debug start
```

Inspect logs, database state, and service health while the failure is reproducible.

When the stack spans **multiple clusters** (a secondary k3d cluster joined to the
primary's docker network) and a workload is stuck `Pending` / not-ready, or a flow
that passes in single-cluster dev fails here — read the pod's STATUS before the
network layer. The failure is almost always image-pull or node-setup, not app
logic. See the `forge/debug/multi-cluster-e2e` skill for the pod-status-first
discipline, the hold-on-fail pattern, and the nested-cluster gotchas (registry
mirror not loaded, host-gateway DNS missing on the secondary, path-MTU dropping
TLS handshakes).

## Frontend E2E

For UI-level end-to-end tests:

- Use **chrome-devtools MCP** for browser interaction in agent-driven testing
- Use **Playwright** for traditional automated browser testing
- Same rules apply: deterministic, isolated, no production URLs

## Rules

- **Never hit production URLs** — all tests target the local stack only
- **Quarantine flaky tests** — mark with `t.Skip()` and link an issue reference
- **No test interdependence** — each test stands alone

## When to Drop Down a Level

If a scenario can be fully tested with mocked Connect clients and doesn't need the live stack, write it as an **integration test** instead. Reserve e2e for flows that genuinely require multiple running services.
