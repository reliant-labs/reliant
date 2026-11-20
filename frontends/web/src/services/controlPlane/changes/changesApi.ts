// Copyright (c) 2025 Reliant Labs

/**
 * ChangesApi: every Forge Changes read and write the Experiments page makes,
 * one method per ChangesService rpc. The page reaches it through
 * ChangesApiContext, so tests and previews hand it an in-memory fake; the
 * default is `createChangesApi(callChanges)`, the live control plane.
 *
 * Mutations take the revision the caller read (`expectedRevision`); a stale
 * one is refused with reason `change_revision_conflict` and changes nothing.
 * StopChange alone may omit it: the kill switch never loses a race. Launch,
 * ramp and roll-out take an optional `stepUpCode`: a protected environment's
 * one-time approval code (RequestChangeApproval), for a caller that is not a
 * console session.
 */

import { currentOrgId } from "../orgs";

import type {
  Change,
  ChangeApprovalCode,
  ChangeDraftEdit,
  ChangeEventPage,
  ChangeList,
  ChangePreviewInput,
  ChangeReadout,
  ChangeUnit,
} from "./changesTypes";
import { callChanges, type ChangesCall } from "./changesTransport";
import { changeEventOf, changeOf, unitEnum } from "./changesWire";

export interface ListChangesInput {
  orgId: string;
  environmentId?: string;
  /** The forge project (forge.yaml name). */
  project?: string;
  pageSize?: number;
  pageToken?: string;
}

export interface CreateChangeInput {
  environmentId: string;
  key: string;
  description: string;
  unit: ChangeUnit;
}

/** What launch, ramp and roll-out share: the revision read, and a protected environment's approval code. */
export interface GatedWrite {
  expectedRevision: number;
  stepUpCode?: string;
}

export interface RollOutInput extends GatedWrite {
  variant: string;
  /** Everyone gets it (remote config); percentBp is then ignored. */
  everyone: boolean;
  percentBp: number;
}

export interface ChangesApi {
  /** The org every list is scoped by: the caller's active org. */
  currentOrgId: () => Promise<string | null>;
  listChanges: (input: ListChangesInput, signal?: AbortSignal) => Promise<ChangeList>;
  getChange: (changeId: string, signal?: AbortSignal) => Promise<Change>;
  listChangeEvents: (changeId: string, pageToken?: string, signal?: AbortSignal) => Promise<ChangeEventPage>;
  createChange: (input: CreateChangeInput) => Promise<Change>;
  updateChangeDraft: (changeId: string, expectedRevision: number, edit: ChangeDraftEdit) => Promise<Change>;
  setChangePreview: (changeId: string, expectedRevision: number, entries: ChangePreviewInput[], activate: boolean) => Promise<Change>;
  requestChangeApproval: (changeId: string) => Promise<ChangeApprovalCode>;
  launchChange: (changeId: string, percentBp: number, write: GatedWrite) => Promise<Change>;
  rampChange: (changeId: string, percentBp: number, write: GatedWrite) => Promise<Change>;
  rollOutChange: (changeId: string, input: RollOutInput) => Promise<Change>;
  /** Never blocked: no approval, and no revision unless the caller names one. */
  stopChange: (changeId: string, expectedRevision?: number) => Promise<Change>;
  endChangeRun: (changeId: string, expectedRevision: number) => Promise<Change>;
  /**
   * The results of one run. ABSENT until the control plane serves
   * GetChangeReadout (changesTypes.ts, ChangeReadout); the results panel says
   * so instead of drawing anything.
   */
  getChangeReadout?: (changeId: string, runId: string, signal?: AbortSignal) => Promise<ChangeReadout>;
}

const changeIn = (resp: unknown): Change => changeOf((resp as { change?: unknown } | null)?.change);

function gated(changeId: string, write: GatedWrite): Record<string, unknown> {
  const body: Record<string, unknown> = { changeId, expectedRevision: String(write.expectedRevision) };
  if (write.stepUpCode) body.stepUpCode = write.stepUpCode;
  return body;
}

/** The ChangesApi over one call function: the live one by default, a fake transport in tests. */
export function createChangesApi(call: ChangesCall = callChanges, orgId: () => Promise<string | null> = currentOrgId): ChangesApi {
  return {
    currentOrgId: orgId,
    async listChanges(input, signal) {
      const body: Record<string, unknown> = { orgId: input.orgId, pageSize: input.pageSize ?? 200 };
      if (input.environmentId) body.environmentId = input.environmentId;
      if (input.project) body.project = input.project;
      if (input.pageToken) body.pageToken = input.pageToken;
      const resp = (await call("ListChanges", body, signal)) as { changes?: unknown[]; nextPageToken?: string } | null;
      return { changes: (resp?.changes ?? []).map(changeOf), nextPageToken: resp?.nextPageToken ?? "" };
    },
    async getChange(changeId, signal) {
      return changeIn(await call("GetChange", { changeId }, signal));
    },
    async listChangeEvents(changeId, pageToken, signal) {
      const body: Record<string, unknown> = { changeId, pageSize: 50 };
      if (pageToken) body.pageToken = pageToken;
      const resp = (await call("ListChangeEvents", body, signal)) as { events?: unknown[]; nextPageToken?: string } | null;
      return { events: (resp?.events ?? []).map(changeEventOf), nextPageToken: resp?.nextPageToken ?? "" };
    },
    async createChange(input) {
      return changeIn(
        await call("CreateChange", { environmentId: input.environmentId, key: input.key, description: input.description, unit: unitEnum(input.unit) })
      );
    },
    async updateChangeDraft(changeId, expectedRevision, edit) {
      const body: Record<string, unknown> = { changeId, expectedRevision: String(expectedRevision) };
      if (edit.description !== undefined) body.description = edit.description;
      if (edit.unit !== undefined) body.unit = unitEnum(edit.unit);
      if (edit.audience === null) body.clearAudience = true;
      else if (edit.audience) body.audience = edit.audience;
      if (edit.split !== undefined) body.split = { arms: edit.split };
      if (edit.metric === null) body.clearMetric = true;
      else if (edit.metric) {
        const { minSamplePerArm, ...rest } = edit.metric;
        body.metric = { ...rest, ...(minSamplePerArm > 0 ? { minSamplePerArm: String(minSamplePerArm) } : {}) };
      }
      return changeIn(await call("UpdateChangeDraft", body));
    },
    async setChangePreview(changeId, expectedRevision, entries, activate) {
      const wire = entries.map((e) => ({ ...(e.unitId ? { unitId: e.unitId } : { unitHash: e.unitHash }), variant: e.variant, label: e.label }));
      return changeIn(await call("SetChangePreview", { changeId, expectedRevision: String(expectedRevision), entries: wire, activate }));
    },
    async requestChangeApproval(changeId) {
      const resp = (await call("RequestChangeApproval", { changeId })) as { sentTo?: string; expiresAt?: string } | null;
      const expires = resp?.expiresAt ? new Date(resp.expiresAt) : null;
      return { sentTo: resp?.sentTo ?? "", expiresAt: expires && !Number.isNaN(expires.getTime()) ? expires : null };
    },
    async launchChange(changeId, percentBp, write) {
      return changeIn(await call("LaunchChange", { ...gated(changeId, write), percentBp }));
    },
    async rampChange(changeId, percentBp, write) {
      return changeIn(await call("RampChange", { ...gated(changeId, write), percentBp }));
    },
    async rollOutChange(changeId, input) {
      const body = { ...gated(changeId, input), variant: input.variant, ...(input.everyone ? { everyone: true } : { percentBp: input.percentBp }) };
      return changeIn(await call("RollOutChange", body));
    },
    async stopChange(changeId, expectedRevision) {
      return changeIn(await call("StopChange", expectedRevision ? { changeId, expectedRevision: String(expectedRevision) } : { changeId }));
    },
    async endChangeRun(changeId, expectedRevision) {
      return changeIn(await call("EndChangeRun", { changeId, expectedRevision: String(expectedRevision) }));
    },
  };
}

export const liveChangesApi: ChangesApi = createChangesApi();
