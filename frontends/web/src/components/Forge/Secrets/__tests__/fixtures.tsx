// Copyright (c) 2025 Reliant Labs

/**
 * Shared fixtures for the secrets tests.
 *
 * `reportOutcome` deliberately goes through `classifyForgeResponse` with a real
 * JSON string rather than hand-constructing an outcome object, so every test
 * covers the classification path too — the same discipline the topology tests
 * use. A fixture that skipped it could pass while the real response shape failed
 * to classify.
 */

import { classifyForgeResponse } from "@/services/forge/topology";
import type { ForgeOutcome, ForgeTopologyReport } from "@/services/forge/topology";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

export function meta(overrides: Partial<ForgeReportMeta> = {}): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.9.1",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
    ...overrides,
  } as ForgeReportMeta;
}

export function reportOutcome(
  report: ForgeSecretsReport,
  metaOverrides: Partial<ForgeReportMeta> = {}
): ForgeOutcome<ForgeSecretsReport> {
  return classifyForgeResponse<ForgeSecretsReport>(
    meta(metaOverrides),
    JSON.stringify(report)
  );
}

export function topologyOutcome(envs: string[]): ForgeOutcome<ForgeTopologyReport> {
  return classifyForgeResponse<ForgeTopologyReport>(
    meta(),
    JSON.stringify({
      project: "control-plane",
      environments: envs.map((env) => ({ env, declared: true, bound: true, images: [] })),
    })
  );
}

/**
 * The REAL control-plane dev report: 13 declared, 13 missing, store_exists
 * false, provider file. Every name and coordinate below is a name or a
 * coordinate — there is no value in this fixture because there is no field that
 * could hold one.
 */
export function controlPlaneDevReport(): ForgeSecretsReport {
  const declared: Array<[string, string, string]> = [
    ["GITHUB_CLIENT_SECRET", "admin-server", "github_client_secret"],
    ["GITHUB_WEBHOOK_SECRET", "admin-server", "github_webhook_secret"],
    ["STRIPE_SECRET_KEY", "admin-server", "stripe_secret_key"],
    ["STRIPE_WEBHOOK_SECRET", "admin-server", "stripe_webhook_secret"],
    ["ANTHROPIC_API_KEY", "llm-gateway", "anthropic_api_key"],
    ["OPENAI_API_KEY", "llm-gateway", "openai_api_key"],
    ["ZITADEL_CLIENT_SECRET", "admin-server", "zitadel_client_secret"],
    ["DAEMON_TOKEN_SIGNING_KEY", "daemon-gateway", "daemon_token_signing_key"],
    ["POSTGRES_PASSWORD", "admin-server", "postgres_password"],
    ["TEMPORAL_API_KEY", "reliant-temporal-worker", "temporal_api_key"],
    ["SMTP_PASSWORD", "admin-server", "smtp_password"],
    ["SENTRY_DSN", "admin-server", "sentry_dsn"],
    ["GRAFANA_API_KEY", "admin-server", "grafana_api_key"],
  ];

  return {
    env: "dev",
    provider: "file",
    store_path: "/Users/dev/src/control-plane/.forge/secrets/dev.yaml",
    store_exists: false,
    secrets: declared.map(([name, workload, key]) => ({
      name,
      present: false,
      declared_by: [
        { workload, kind: "service", secret_name: "control-plane-secrets", secret_key: key },
      ],
    })),
    inert: [],
    missing: declared.map(([name]) => name),
    missing_count: 13,
    ok: false,
  };
}
