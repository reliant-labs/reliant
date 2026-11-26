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

import type { Transport } from "@connectrpc/connect";
import type { JsonObject, JsonValue, MessageInitShape } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import {
  DashboardService,
  DashboardSource as WireSource,
  DashboardViewKind as WireViewKind,
  type DashboardRef as WireRef,
  type DashboardSummary as WireSummary,
  type DashboardUser as WireUser,
  type DashboardVersionInfo as WireVersion,
  type GetDashboardRequestSchema,
  type GetDashboardResponse,
  type ListDashboardsRequestSchema,
  type ListDashboardsResponse,
  type ListDashboardTemplatesResponse,
  type ListDashboardVersionsRequestSchema,
  type ListDashboardVersionsResponse,
  type PromoteDashboardToCodeRequestSchema,
  type PromoteDashboardToCodeResponse,
  type RestoreDashboardVersionRequestSchema,
  type RestoreDashboardVersionResponse,
  type SaveAsCopyRequestSchema,
  type SaveAsCopyResponse,
  type SaveDashboardRequestSchema,
  type SaveDashboardResponse,
} from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";

import { getControlPlaneClient } from "../client";
import { canonicalJson } from "./canonicalJson";
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

function client(transport?: Transport) {
  return getControlPlaneClient(DashboardService, transport);
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
    // Empty (proto3 default) reads as absent.
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

/**
 * The dashboard as a Struct, in forge's canonical form: what the console
 * fills in when reading (an empty `series` on a text panel, an empty signal)
 * and every other empty value is dropped, as forge's omitempty fields drop
 * them, because forge's validator refuses a field a kind does not take even
 * when it is empty.
 */
export function dashboardToJson(dashboard: Dashboard): JsonObject {
  return JSON.parse(JSON.stringify(canonicalJson(dashboard) ?? {})) as JsonObject;
}

// ── Requests and answers ─────────────────────────────────────────────────────
//
// The console calls DashboardService through its generated hooks
// (hooks/controlplane/dashboard-service-hooks_gen.ts): hooks/dashboard-queries.ts
// builds each request here and maps each answer in `select`, and every write's
// call site sends the request built here. The functions below them run the
// same builders and mappers on a transport, for the model-level contract the
// shop fixture pack and the over-the-wire fixture tests compare
// (ObservabilityApi).

/**
 * The merged index for a project and (optionally) one environment: release
 * dashboards, then workspace ones. No project is the whole org: the field is
 * left off the request, because "" would mean only the project-less ones.
 */
export function listDashboardsRequest(req: ListDashboardsInput): MessageInitShape<typeof ListDashboardsRequestSchema> {
  return {
    orgId: req.orgId,
    ...(req.projectId === undefined ? {} : { projectId: req.projectId }),
    envId: req.envId,
  };
}

export function dashboardIndexOf(res: ListDashboardsResponse): DashboardIndex {
  return { dashboards: res.dashboards.map(mapSummary), canWrite: res.canWrite };
}

export function dashboardTemplatesOf(res: ListDashboardTemplatesResponse): DashboardSummary[] {
  return res.templates.map(mapSummary);
}

/** `ref` null is a read not sent yet (its query is disabled). */
export function getDashboardRequest(req: Omit<GetDashboardInput, "ref"> & { ref: DashboardRef | null }): MessageInitShape<typeof GetDashboardRequestSchema> {
  return {
    orgId: req.orgId,
    ref: req.ref ? refInit(req.ref) : undefined,
    version: req.version,
    releaseVersion: req.releaseVersion,
  };
}

export function dashboardDocumentOf(res: GetDashboardResponse): DashboardDocument {
  if (!res.summary) throw new Error("the control plane answered without the dashboard");
  return { summary: mapSummary(res.summary), spec: dashboardFromJson(res.spec), version: res.version, canWrite: res.canWrite };
}

export function saveDashboardRequest(req: SaveDashboardInput): MessageInitShape<typeof SaveDashboardRequestSchema> {
  return {
    orgId: req.orgId,
    projectId: req.projectId,
    slug: req.slug,
    spec: dashboardToJson(req.spec),
    envScope: req.envScope ? { envIds: req.envScope.envIds } : undefined,
    baseVersion: req.baseVersion,
    message: req.message,
  };
}

/** A write's answer: the dashboard as saved (`what` names it when the answer leaves it out). */
export function savedDashboardOf(res: SaveDashboardResponse | SaveAsCopyResponse | RestoreDashboardVersionResponse, what = "the saved dashboard"): SavedDashboard {
  if (!res.summary) throw new Error(`the control plane answered without ${what}`);
  return { summary: mapSummary(res.summary), version: mapVersion(res.version) };
}

export function saveAsCopyRequest(req: SaveAsCopyInput): MessageInitShape<typeof SaveAsCopyRequestSchema> {
  return {
    orgId: req.orgId,
    from: refInit(req.from),
    projectId: req.projectId,
    slug: req.slug ?? "",
    title: req.title ?? "",
    envScope: req.envScope ? { envIds: req.envScope.envIds } : undefined,
    message: req.message ?? "",
  };
}

/** `ref` null is a read not sent yet (its query is disabled). */
export function listDashboardVersionsRequest(orgId: string, ref: DashboardRef | null): MessageInitShape<typeof ListDashboardVersionsRequestSchema> {
  return { orgId, ref: ref ? refInit(ref) : undefined, pageSize: 100 };
}

export function dashboardVersionsOf(res: ListDashboardVersionsResponse): DashboardVersion[] {
  return res.versions.map(mapVersion);
}

export function restoreDashboardVersionRequest(orgId: string, ref: DashboardRef, version: number, baseVersion: number): MessageInitShape<typeof RestoreDashboardVersionRequestSchema> {
  return { orgId, ref: refInit(ref), version, baseVersion };
}

/** The request of a write that names only the dashboard: archive, star, unstar, record a view. */
export function dashboardRefRequest(orgId: string, ref: DashboardRef) {
  return { orgId, ref: refInit(ref) };
}

export function promoteDashboardToCodeRequest(orgId: string, ref: DashboardRef, version?: number): MessageInitShape<typeof PromoteDashboardToCodeRequestSchema> {
  return { orgId, ref: refInit(ref), version };
}

export function promotedDashboardOf(res: PromoteDashboardToCodeResponse): PromotedDashboard {
  return {
    path: res.file?.path ?? "",
    content: new TextDecoder().decode(res.file?.content ?? new Uint8Array()),
    prUrl: res.prUrl,
    warnings: [...res.warnings],
  };
}

/** The dashboard a wire summary names, as the console's index names it (a generated or repo one is a release dashboard). */
export function refOfSummary(s: WireSummary): DashboardRef {
  return mapSummary(s).ref;
}

export function sameDashboardRef(a: DashboardRef, b: DashboardRef): boolean {
  return a.kind === b.kind && a.projectId === b.projectId && a.key === b.key;
}

// ── The model-level contract, on a transport ─────────────────────────────────

export async function listDashboards(req: ListDashboardsInput, transport?: Transport): Promise<DashboardIndex> {
  return dashboardIndexOf(await client(transport).listDashboards(listDashboardsRequest(req)));
}

export async function listDashboardTemplates(orgId: string, transport?: Transport): Promise<DashboardSummary[]> {
  return dashboardTemplatesOf(await client(transport).listDashboardTemplates({ orgId }));
}

export async function getDashboard(req: GetDashboardInput, transport?: Transport): Promise<DashboardDocument> {
  return dashboardDocumentOf(await client(transport).getDashboard(getDashboardRequest(req)));
}

export async function saveDashboard(req: SaveDashboardInput, transport?: Transport): Promise<SavedDashboard> {
  return savedDashboardOf(await client(transport).saveDashboard(saveDashboardRequest(req)));
}

export async function saveDashboardAsCopy(req: SaveAsCopyInput, transport?: Transport): Promise<SavedDashboard> {
  return savedDashboardOf(await client(transport).saveAsCopy(saveAsCopyRequest(req)), "the copy");
}

export async function listDashboardVersions(orgId: string, ref: DashboardRef, transport?: Transport): Promise<DashboardVersion[]> {
  return dashboardVersionsOf(await client(transport).listDashboardVersions(listDashboardVersionsRequest(orgId, ref)));
}

export async function restoreDashboardVersion(orgId: string, ref: DashboardRef, version: number, baseVersion: number, transport?: Transport): Promise<SavedDashboard> {
  return savedDashboardOf(await client(transport).restoreDashboardVersion(restoreDashboardVersionRequest(orgId, ref, version, baseVersion)), "the restored dashboard");
}

export async function archiveDashboard(orgId: string, ref: DashboardRef, transport?: Transport): Promise<void> {
  await client(transport).archiveDashboard(dashboardRefRequest(orgId, ref));
}

export async function starDashboard(orgId: string, ref: DashboardRef, starred: boolean, transport?: Transport): Promise<void> {
  if (starred) await client(transport).starDashboard(dashboardRefRequest(orgId, ref));
  else await client(transport).unstarDashboard(dashboardRefRequest(orgId, ref));
}

export async function recordDashboardView(orgId: string, ref: DashboardRef, transport?: Transport): Promise<void> {
  await client(transport).recordDashboardView(dashboardRefRequest(orgId, ref));
}

export async function promoteDashboardToCode(orgId: string, ref: DashboardRef, version?: number, transport?: Transport): Promise<PromotedDashboard> {
  return promotedDashboardOf(await client(transport).promoteDashboardToCode(promoteDashboardToCodeRequest(orgId, ref, version)));
}
