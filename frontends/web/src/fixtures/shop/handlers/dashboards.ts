// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's DashboardService reads (the index, templates, one dashboard,
 * its versions) and AppDatabaseService's two dashboard reads (a postgres
 * panel, the databases a panel may read) — shop's story (model/dashboards.ts,
 * model/panels.ts) encoded on the wire, so the console's real client
 * (services/controlPlane/observability/dashboardsApi.ts, appDatabaseApi.ts)
 * decodes it. A dashboard's spec travels as the JSON control-plane stores.
 *
 * Saving, copying, restoring, archiving, starring, promoting and recording a
 * view are writes: in the demo and the tour the transport's read-only guard
 * refuses them first. They are encoded here too, over the same model, for a
 * test fake that answers them (test/observabilityFixtures.tsx): the shop
 * model's own writes refuse (model/dashboards.ts).
 */

import type { JsonObject } from "@bufbuild/protobuf";
import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { DashboardService, DashboardSource as WireSource, DashboardViewKind as WireViewKind, type DashboardRef as WireRef } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import { dashboardFromJson, dashboardToJson } from "@/services/controlPlane/observability/dashboardsApi";
import type { DashboardKind, DashboardRef, DashboardSource, DashboardSummary, DashboardUser, DashboardVersion, SavedDashboard } from "@/services/controlPlane/observability/dashboardTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createShopApi } from "../model/shopApi";
import { completed } from "./defaults";
import { dbl, int64, msOf, points, ts, tsOpt } from "./wire";

const WIRE_SOURCE: Record<DashboardSource, WireSource> = {
  generated: WireSource.GENERATED,
  repo: WireSource.REPO,
  workspace: WireSource.WORKSPACE,
  template: WireSource.TEMPLATE,
  builtin: WireSource.BUILTIN,
};
const WIRE_OF_KIND: Record<DashboardKind, WireSource> = {
  release: WireSource.GENERATED,
  workspace: WireSource.WORKSPACE,
  template: WireSource.TEMPLATE,
  builtin: WireSource.BUILTIN,
};
const KIND_OF_WIRE: Partial<Record<WireSource, DashboardKind>> = {
  [WireSource.GENERATED]: "release",
  [WireSource.REPO]: "release",
  [WireSource.WORKSPACE]: "workspace",
  [WireSource.TEMPLATE]: "template",
  [WireSource.BUILTIN]: "builtin",
};
const WIRE_VIEW_KIND: Partial<Record<NonNullable<DashboardSummary["viewKind"]>, WireViewKind>> = {
  "organization-costs": WireViewKind.ORGANIZATION_COSTS,
  "organization-usage": WireViewKind.ORGANIZATION_USAGE,
  "organization-billing": WireViewKind.ORGANIZATION_BILLING,
};

const modelRef = (ref: WireRef | undefined): DashboardRef => ({ kind: KIND_OF_WIRE[ref?.source ?? WireSource.UNSPECIFIED] ?? "workspace", projectId: ref?.projectId ?? "", key: ref?.key ?? "" });
const wireRef = (ref: DashboardRef) => ({ source: WIRE_OF_KIND[ref.kind], projectId: ref.projectId, key: ref.key });
const wireUser = (u: DashboardUser | null) => (u ? { ...u } : undefined);
const asJson = <T>(value: T): JsonObject => JSON.parse(JSON.stringify(value ?? {})) as JsonObject;

function wireSummary(s: DashboardSummary) {
  return {
    group: s.group ?? "",
    tags: [...(s.tags ?? [])],
    ref: wireRef(s.ref),
    source: WIRE_SOURCE[s.source],
    viewKind: s.viewKind ? (WIRE_VIEW_KIND[s.viewKind] ?? WireViewKind.UNSPECIFIED) : WireViewKind.UNSPECIFIED,
    title: s.title,
    description: s.description,
    readOnly: s.readOnly,
    starred: s.starred,
    lastViewedAt: tsOpt(s.lastViewedAtMs),
    panelCount: s.panelCount,
    previewPanels: s.previewPanels.map(asJson),
    release: s.release ? { releaseVersion: s.release.releaseVersion, shippedAt: tsOpt(s.release.shippedAtMs), filePath: s.release.filePath, envIds: [...s.release.envIds] } : undefined,
    workspace: s.workspace
      ? {
          version: s.workspace.version,
          createdBy: wireUser(s.workspace.createdBy),
          updatedBy: wireUser(s.workspace.updatedBy),
          createdAt: tsOpt(s.workspace.createdAtMs),
          updatedAt: tsOpt(s.workspace.updatedAtMs),
          forkedFrom: s.workspace.forkedFrom,
          envIds: [...s.workspace.envIds],
        }
      : undefined,
    category: s.category,
  };
}

const wireVersion = (v: DashboardVersion) => ({ version: v.version, releaseVersion: v.releaseVersion, author: wireUser(v.author), message: v.message, createdAt: tsOpt(v.createdAtMs), current: v.current });
const wireSaved = (s: SavedDashboard) => ({ summary: wireSummary(s.summary), version: wireVersion(s.version) });
/** An env scope left off the request is left off the model's input (absent: unchanged on an update). */
const modelEnvScope = (scope: { envIds: string[] } | undefined) => (scope ? { envScope: { envIds: [...scope.envIds] } } : {});

export function dashboardsHandlers(fixtures: ObservabilityApi = createShopApi()): MockHandlers {
  return {
    ...completed(DashboardService, mockService(DashboardService, {
      listDashboards: async (req) => {
        const res = await fixtures.listDashboards({ orgId: req.orgId, projectId: req.projectId, envId: req.envId });
        return { dashboards: res.dashboards.map(wireSummary), canWrite: res.canWrite };
      },
      listDashboardTemplates: async (req) => ({ templates: (await fixtures.listDashboardTemplates(req.orgId)).map(wireSummary) }),
      getDashboard: async (req) => {
        const d = await fixtures.getDashboard({ orgId: req.orgId, ref: modelRef(req.ref), version: req.version, releaseVersion: req.releaseVersion });
        return { summary: wireSummary(d.summary), spec: dashboardToJson(d.spec) as JsonObject, version: d.version, canWrite: d.canWrite };
      },
      listDashboardVersions: async (req) => ({ versions: (await fixtures.listDashboardVersions(req.orgId, modelRef(req.ref))).map(wireVersion), nextPageToken: "" }),
      saveDashboard: async (req) =>
        wireSaved(
          await fixtures.saveDashboard({
            orgId: req.orgId,
            projectId: req.projectId,
            slug: req.slug,
            spec: dashboardFromJson(req.spec),
            ...modelEnvScope(req.envScope),
            baseVersion: req.baseVersion,
            message: req.message,
          })
        ),
      // The console leaves a copy's slug, title and message empty to take the source's: absent, in the model.
      saveAsCopy: async (req) =>
        wireSaved(
          await fixtures.saveDashboardAsCopy({
            orgId: req.orgId,
            from: modelRef(req.from),
            projectId: req.projectId,
            slug: req.slug || undefined,
            title: req.title || undefined,
            ...modelEnvScope(req.envScope),
            message: req.message || undefined,
          })
        ),
      restoreDashboardVersion: async (req) => wireSaved(await fixtures.restoreDashboardVersion(req.orgId, modelRef(req.ref), req.version, req.baseVersion)),
      archiveDashboard: async (req) => {
        await fixtures.archiveDashboard(req.orgId, modelRef(req.ref));
        return {};
      },
      starDashboard: async (req) => {
        await fixtures.starDashboard(req.orgId, modelRef(req.ref), true);
        return {};
      },
      unstarDashboard: async (req) => {
        await fixtures.starDashboard(req.orgId, modelRef(req.ref), false);
        return {};
      },
      recordDashboardView: async (req) => {
        await fixtures.recordDashboardView(req.orgId, modelRef(req.ref));
        return {};
      },
      promoteDashboardToCode: async (req) => {
        const promoted = await fixtures.promoteDashboardToCode(req.orgId, modelRef(req.ref), req.version);
        return { file: { path: promoted.path, content: new TextEncoder().encode(promoted.content) }, prUrl: promoted.prUrl, warnings: [...promoted.warnings] };
      },
    })),
    ...completed(AppDatabaseService, mockService(AppDatabaseService, {
      queryDatabasePanel: async (req) => {
        const window = { startMs: msOf(req.from), endMs: msOf(req.to) };
        const res = await fixtures.queryDatabasePanel({
          environmentId: req.environmentId,
          window,
          query: { database: req.database, sql: req.sql, draw: req.draw || undefined, time: req.timeColumn || undefined, values: [...req.valueColumns], group: [...req.groupColumns] },
          stepSeconds: req.stepSeconds,
          dashboardRef: req.dashboardRef,
          panelId: req.panelId,
        });
        const answered = res.window ?? window;
        return {
          draw: res.series.length ? "line" : "table",
          stepSeconds: res.stepSeconds,
          from: ts(answered.startMs),
          to: ts(answered.endMs),
          truncated: res.truncated,
          truncatedBy: res.truncatedBy,
          columns: [...res.columns],
          rows: res.rows.map((r) => ({ group: { ...r.group }, values: r.values.map((v) => v ?? 0), missingValueIndexes: r.values.flatMap((v, i) => (v === null ? [i] : [])) })),
          series: res.series.map((s) => ({ label: s.label, group: { ...s.group }, points: points(s.timestamps, s.values), value: dbl(s.value) })),
          database: res.database,
          replica: res.replica,
          servedFrom: res.servedFrom,
          durationMs: int64(res.durationMs),
        };
      },
      listDashboardDatabases: async (req) => ({
        databases: (await fixtures.listDashboardDatabases(req.environmentId)).map((db) => ({
          name: db.name,
          rowCap: db.rowCap,
          timeoutMs: db.timeoutMs,
          replica: db.replica,
          tablesError: db.tablesError,
          tables: db.tables.map((t) => ({ schema: t.schema, name: t.name, columns: t.columns.map((c) => ({ name: c.name, pgType: c.type })) })),
        })),
      }),
    })),
  };
}
