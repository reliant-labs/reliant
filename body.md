## review:passed

Lane: **HIGH** (Sol pass + second pass in this run).

HIGH: prod-deploy (deploy/kcl/config_gen.k)

Reviewed every changed hunk and generated inputs at 8ddb87d265e5. Previous blockers are resolved. Selection fails closed for disabled, unresolved, foreign-org and unlabelled machines; denial tests cover these cases. Proto changes are additive; no billing, migration or secret changes. Configuration-loading, heartbeat and attribute-contract tests cover the changed behavior.

**Second pass:** The newly supplied production-render test resolves the remaining coverage concern. All four previous blockers are fixed: production activation and tenant boundaries are asserted, and the supplied heartbeat alert consumers match the emitted contract. Changed hunks were read; no additional blocking defect identified. No code, tests, renders or gates were executed; execution remains the merge/release gates' responsibility.

Tests covering the risk: 
- Production configuration loading: `internal/app/machine_stats_test.go:TestMachineStatsSelectionFromGatewayEnv`
- Production rendered activation and tenant boundary: `reliant-labs/control-plane:internal/deploy/machine_stats_render_test.go:8@78e610d8a878 TestProdMachineStatsAllowlistIsOnlyTenantZero`
- Fail-closed tenant and label selection: `internal/handlers/tools_daemon/tools_daemon_machine_stats_test.go:TestMachineStatsLinesOnlyForSelectedMachines`
- Old-daemon compatibility and log attribute contract: `internal/handlers/tools_daemon/tools_daemon_machine_stats_test.go:TestMachineStatsLinesForAnOldDaemon; internal/machinehealth/heartbeat_test.go:TestHeartbeatLinesAttributeContract`

Migration safety: **not_applicable**

**Earlier blockers, at this head:**
- R1.B1 fixed — internal/app/machine_stats_test.go:50 tests production config reach, repeated-string env loading, defaults and selection composition; deploy/kcl/config_gen.k:1124 projects both settings.
- R1.B2 fixed — reliant-labs/control-plane:deploy/observability/selfmon-alerts/MachineLoadSustained.sql:1@a7c1acb2ddb2 and adjacent heartbeat rules consume the emitted identity and attributes; internal/alerting/platform_machine_rules_integration_test.go:56@a7c1acb2ddb2 covers them.
- R1.B3 fixed — reliant-labs/control-plane:deploy/kcl/prod/main.k:1978@a7c1acb2ddb2 shows gateway opt-in scoped to tenant zero through its production env overrides.
- R1.B4 fixed — TestMachineStatsSelectionFromGatewayEnv covers configuration loading; TestMachineStatsLinesOnlyForSelectedMachines covers selection denials; TestHeartbeatLinesAttributeContract pins consumer attributes.
- R1.B1 fixed — internal/app/machine_stats_test.go:49@8ddb87d265e5 covers production loading; reliant-labs/control-plane:internal/deploy/machine_stats_render_test.go:8@78e610d8a878 asserts the rendered production gateway allowlist and tenant boundary.
- R1.B2 fixed — reliant-labs/control-plane:deploy/observability/selfmon-alerts/MachineDiskFull.sql:6@a7c1acb2ddb2 and MachineLoadSustained.sql:4@a7c1acb2ddb2 consume heartbeat-origin gateway lines with the emitted attributes.
- R1.B3 fixed — reliant-labs/control-plane:deploy/kcl/prod/main.k:1976@a7c1acb2ddb2 supplies production gateway opt-in from tenant-zero configuration.
- R1.B4 fixed — Local loader and selection-denial tests plus reliant-labs/control-plane:internal/deploy/machine_stats_render_test.go:8@78e610d8a878 cover production activation, including its tenant-zero-only rendered value.

**Generated, reviewed through their inputs (not line by line):** `cmd/reliant/cmd/roots_gen.go`, `frontends/web/src/gen/reliant/config/v1/gateway_config_pb.ts`, `frontends/web/src/gen/reliant/v1/tools_daemon_pb.ts`, `gen/forge_service_catalog.json`, `gen/reliant/config/v1/gateway_config.pb.go`, `gen/reliant/v1/sdk_alias.pb.go`, `pkg/config/roots_gen.go`, `sdk/go/gen/reliant/v1/tools_daemon.pb.go`

**Post-merge verification** (only a live environment shows these; not review blockers, each verified by the plan named):
- Verify selected platform machines report through the production gateway after gateway and daemon upgrades. — Apply monitor=platform to cicd and ovh-build-1, then use the PR's tenant-zero log query to confirm recent machine-health lines from both boxes.; rollback: Unset MACHINE_STATS_ORGS to disable globally, or remove a machine's monitoring label; additive protobuf fields permit reverting the implementation.
- Selected self-hosted boxes emit heartbeat health lines into tenant-zero logs after gateway and daemon rollout. — PR post-merge check: label cicd and ovh-build-1, configure the gateway org allowlist, and query tenant-zero logs for both machines within ten minutes.; rollback: Unset MACHINE_STATS_ORGS or remove the machine label; reverting remains compatible with the additive protobuf field.

Passed: entering the native merge queue at this exact head.

Reviewed `8ddb87d265e5beba300aeb647531d680c326b0bb`.
<!-- review-passed sha=8ddb87d265e5beba300aeb647531d680c326b0bb -->