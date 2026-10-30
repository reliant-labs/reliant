// Copyright (c) 2025 Reliant Labs

/**
 * `controlplane.v1.DashboardService` — native dashboards — mapped into the
 * console's model (./dashboardTypes.ts). The only file that knows its wire
 * shape.
 *
 * Every request carries the caller's org, which the server never trusts: it
 * verifies membership first, and authoring needs the admin role (the index
 * says whether the caller has it, `canWrite`).
 *
 * The spec travels as a protobuf Struct, i.e. the dashboard object exactly as
 * a forge file holds it. Reading, the console normalizes the optional arrays a
 * forge file omits when empty; writing, it sends the object it edited with
 * undefined keys dropped. The server validates with forge's rules and stores
 * forge's canonical form.
 */

import type { JsonObject, JsonValue } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import {
  DashboardService,
  DashboardSource as WireSource,
  DashboardViewKind as WireViewKind,
  type DashboardRef as WireRef,
  type DashboardSummary as WireSummary,
  type DashboardUser as WireUser,
  type DashboardVersionInfo as WireVersion,
} from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";

import { getControlPlaneClient } from "../client";
import type {
  DashboardDocument,
  DashboardIndex,
  DashboardKind,
  DashboardRef,
  DashboardSource,
  DashboardSummary,
  DashboardUser,
  DashboardVersion,
  GetDashboardInput,
  ListDashboardsInput,
  PromotedDashboard,
  SaveAsCopyInput,
  SavedDashboard,
  SaveDashboardInput,
} from "./dashboardTypes";
import type { Dashboard, Panel } from "./types";

function client() {
  return getControlPlaneClient(DashboardService);
}

function optMs(ts: Timestamp | undefined): number | null {
  return ts ? Number(ts.seconds) * 1000 + Math.round(ts.nanos / 1e6) : null;
}

const SOURCE_OF_WIRE: Record<WireSource, DashboardSource | null> = {
  [WireSource.UNSPECIFIED]: null,
  [WireSource.GENERATED]: "generated",
  [WireSource.REPO]: "repo",
  [WireSource.WORKSPACE]: "workspace",
  [WireSource.TEMPLATE]: "template",
  [WireSource.BUILTIN]: "builtin",
};

/** A view kind this client does not know reads as a spec dashboard, as an older client would. */
const VIEW_KIND_OF_WIRE: Partial<Record<WireViewKind, DashboardSummary["viewKind"]>> = {
  [WireViewKind.ORGANIZATION_COSTS]: "organization-costs",
  [WireViewKind.ORGANIZATION_USAGE]: "organization-usage",
  [WireViewKind.ORGANIZATION_BILLING]: "organization-billing",
};

/** A release ref is sent as GENERATED: the server resolves a release dashboard by key whichever of the two it is. */
const WIRE_OF_KIND: Record<DashboardKind, WireSource> = {
  release: WireSource.GENERATED,
  workspace: WireSource.WORKSPACE,
  template: WireSource.TEMPLATE,
  builtin: WireSource.BUILTIN,
};

function refInit(ref: DashboardRef) {
  return { source: WIRE_OF_KIND[ref.kind], projectId: ref.projectId, key: ref.key };
}

function mapUser(user: WireUser | undefined): DashboardUser | null {
  return user && (user.id || user.name || user.email) ? { id: user.id, name: user.name, email: user.email } : null;
}

function mapRef(ref: WireRef | undefined, source: DashboardSource): DashboardRef {
  const kind: DashboardKind = source === "generated" || source === "repo" ? "release" : source;
  return { kind, projectId: ref?.projectId ?? "", key: ref?.key ?? "" };
}

function mapSummary(s: WireSummary): DashboardSummary {
  const source = SOURCE_OF_WIRE[s.source] ?? SOURCE_OF_WIRE[s.ref?.source ?? WireSource.UNSPECIFIED] ?? "workspace";
  return {
    // An older server sends neither (proto3 defaults): absent, so the page infers the group.
    group: s.group || undefined,
    tags: s.tags.length ? [...s.tags] : undefined,
    ref: mapRef(s.ref, source),
    source,
    viewKind: VIEW_KIND_OF_WIRE[s.viewKind],
    title: s.title,
    description: s.description,
    readOnly: s.readOnly,
    starred: s.starred,
    lastViewedAtMs: optMs(s.lastViewedAt),
    panelCount: s.panelCount,
    previewPanels: s.previewPanels.map((p) => panelFromJson(p)),
    release: s.release
      ? {
          releaseVersion: s.release.releaseVersion,
          shippedAtMs: optMs(s.release.shippedAt),
          filePath: s.release.filePath,
          envIds: [...s.release.envIds],
        }
      : null,
    workspace: s.workspace
      ? {
          version: s.workspace.version,
          createdBy: mapUser(s.workspace.createdBy),
          updatedBy: mapUser(s.workspace.updatedBy),
          createdAtMs: optMs(s.workspace.createdAt),
          updatedAtMs: optMs(s.workspace.updatedAt),
          forkedFrom: s.workspace.forkedFrom,
          envIds: [...s.workspace.envIds],
        }
      : null,
    category: s.category,
  };
}

function mapVersion(v: WireVersion | undefined): DashboardVersion {
  return {
    version: v?.version ?? 0,
    releaseVersion: v?.releaseVersion ?? "",
    author: mapUser(v?.author),
    message: v?.message ?? "",
    createdAtMs: optMs(v?.createdAt),
    current: !!v?.current,
  };
}

// ── The spec, as JSON ────────────────────────────────────────────────────────

const asArray = (value: unknown): unknown[] => (Array.isArray(value) ? value : []);

/**
 * One forge panel object, with the arrays forge omits when empty filled in.
 * Every other key is kept as it came, so a field this console does not know
 * yet survives an edit-and-save.
 */
export function panelFromJson(value: JsonValue | unknown): Panel {
  const raw = (value && typeof value === "object" && !Array.isArray(value) ? value : {}) as Record<string, unknown>;
  return {
    ...(raw as unknown as Panel),
    id: String(raw.id ?? ""),
    title: String(raw.title ?? ""),
    kind: String(raw.kind ?? ""),
    signal: String(raw.signal ?? ""),
    series: asArray(raw.series) as Panel["series"],
  };
}

/** One forge dashboard object, normalized for the console. */
export function dashboardFromJson(value: JsonObject | undefined): Dashboard {
  const raw = (value ?? {}) as Record<string, unknown>;
  return {
    ...(raw as unknown as Dashboard),
    id: String(raw.id ?? ""),
    title: String(raw.title ?? ""),
    filters: asArray(raw.filters) as Dashboard["filters"],
    annotations: asArray(raw.annotations) as Dashboard["annotations"],
    panels: asArray(raw.panels).map(panelFromJson),
  };
}

/** The dashboard as a Struct: plain JSON, undefined keys dropped. */
export function dashboardToJson(dashboard: Dashboard): JsonObject {
  return JSON.parse(JSON.stringify(dashboard)) as JsonObject;
}

// ── RPCs ─────────────────────────────────────────────────────────────────────

/**
 * The merged index for a project and (optionally) one environment: release
 * dashboards, then workspace ones. No project is the whole org: the field is
 * left off the request, because "" would mean only the project-less ones.
 */
export async function listDashboards(req: ListDashboardsInput): Promise<DashboardIndex> {
  const res = await client().listDashboards({
    orgId: req.orgId,
    ...(req.projectId === undefined ? {} : { projectId: req.projectId }),
    envId: req.envId,
  });
  return { dashboards: res.dashboards.map(mapSummary), canWrite: res.canWrite };
}

export async function listDashboardTemplates(orgId: string): Promise<DashboardSummary[]> {
  const res = await client().listDashboardTemplates({ orgId });
  return res.templates.map(mapSummary);
}

export async function getDashboard(req: GetDashboardInput): Promise<DashboardDocument> {
  const res = await client().getDashboard({
    orgId: req.orgId,
    ref: refInit(req.ref),
    version: req.version,
    releaseVersion: req.releaseVersion,
  });
  if (!res.summary) throw new Error("the control plane answered without the dashboard");
  return { summary: mapSummary(res.summary), spec: dashboardFromJson(res.spec), version: res.version, canWrite: res.canWrite };
}

export async function saveDashboard(req: SaveDashboardInput): Promise<SavedDashboard> {
  const res = await client().saveDashboard({
    orgId: req.orgId,
    projectId: req.projectId,
    slug: req.slug,
    spec: dashboardToJson(req.spec),
    envScope: req.envScope ? { envIds: req.envScope.envIds } : undefined,
    baseVersion: req.baseVersion,
    message: req.message,
  });
  if (!res.summary) throw new Error("the control plane answered without the saved dashboard");
  return { summary: mapSummary(res.summary), version: mapVersion(res.version) };
}

export async function saveDashboardAsCopy(req: SaveAsCopyInput): Promise<SavedDashboard> {
  const res = await client().saveAsCopy({
    orgId: req.orgId,
    from: refInit(req.from),
    projectId: req.projectId,
    slug: req.slug ?? "",
    title: req.title ?? "",
    envScope: req.envScope ? { envIds: req.envScope.envIds } : undefined,
    message: req.message ?? "",
  });
  if (!res.summary) throw new Error("the control plane answered without the copy");
  return { summary: mapSummary(res.summary), version: mapVersion(res.version) };
}

export async function listDashboardVersions(orgId: string, ref: DashboardRef): Promise<DashboardVersion[]> {
  const res = await client().listDashboardVersions({ orgId, ref: refInit(ref), pageSize: 100 });
  return res.versions.map(mapVersion);
}

export async function restoreDashboardVersion(orgId: string, ref: DashboardRef, version: number, baseVersion: number): Promise<SavedDashboard> {
  const res = await client().restoreDashboardVersion({ orgId, ref: refInit(ref), version, baseVersion });
  if (!res.summary) throw new Error("the control plane answered without the restored dashboard");
  return { summary: mapSummary(res.summary), version: mapVersion(res.version) };
}

export async function archiveDashboard(orgId: string, ref: DashboardRef): Promise<void> {
  await client().archiveDashboard({ orgId, ref: refInit(ref) });
}

export async function starDashboard(orgId: string, ref: DashboardRef, starred: boolean): Promise<void> {
  if (starred) await client().starDashboard({ orgId, ref: refInit(ref) });
  else await client().unstarDashboard({ orgId, ref: refInit(ref) });
}

export async function recordDashboardView(orgId: string, ref: DashboardRef): Promise<void> {
  await client().recordDashboardView({ orgId, ref: refInit(ref) });
}

export async function promoteDashboardToCode(orgId: string, ref: DashboardRef, version?: number): Promise<PromotedDashboard> {
  const res = await client().promoteDashboardToCode({ orgId, ref: refInit(ref), version });
  return {
    path: res.file?.path ?? "",
    content: new TextDecoder().decode(res.file?.content ?? new Uint8Array()),
    prUrl: res.prUrl,
    warnings: [...res.warnings],
  };
}
