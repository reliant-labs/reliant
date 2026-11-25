# Verifying the trace filters and RED summary after release

`pkg/obsquery` compiles the trace page and its RED summary for both
`reliant forge obs traces` and the control plane's `ListTraces` (the console's
traces list). Three behaviours can only be judged against prod's own
telemetry, so they are checked after the control plane that pins this forge
release is promoted to prod:

- the trace-wide filters (user, session, browser, OS, frontend and backend
  version) match traces when a service is also selected;
- a `k8s.workload.name` filter matches spans that carry only the
  k8sattributes processor's kind-specific name (`k8s.deployment.name`, …);
- the RED summary over 24h and 7d answers inside the gateway's 15s budget.

## Check after deploy

Run each against prod once control-plane's prod release carries this forge pin
(`go.mod` in control-plane names the forge commit).

1. **RED summary fits the budget.** Both must exit 0 with a `summary` (no 504,
   no `deadline exceeded`), each in under 15s:

   ```bash
   time reliant forge obs traces prod --since 24h --json --fields summary
   time reliant forge obs traces prod --since 168h --json --fields summary
   ```

2. **Workload filter on traces.** Pick a deployment from
   `reliant forge obs workloads prod`; the filter must return traces
   (`summary.traces > 0`) where it returned none before:

   ```bash
   reliant forge obs traces prod --since 1h --filter k8s.workload.name=<deployment> --json --fields summary
   ```

3. **Trace-wide filters with a service.** In the console's traces list for
   prod, select a backend service and a browser (e.g. Chrome) that the
   unfiltered list shows on that service's traces. The list must be non-empty;
   before this release it was always empty. `--explain` on the CLI prints the
   compiled SQL if it needs reading.

4. `reliant forge obs check prod --wait --json` exits 0 for the control-plane
   release that carried the pin.

## Rollback

The change is query compilation only: no schema, no stored data, no wire
format. If any check fails, revert this PR's commits in forge (one revert PR
through the pipeline) and re-pin control-plane to the reverted forge commit
(`task pin:forge` in control-plane), which promotes like any release. The
previous compiler returns exactly; nothing needs migrating back.
