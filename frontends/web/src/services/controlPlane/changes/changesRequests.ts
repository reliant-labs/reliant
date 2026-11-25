// Copyright (c) 2025 Reliant Labs

/**
 * The Experiments page's ChangesService requests, from the console's types
 * (changesTypes.ts) to the generated request messages. The calls are the
 * generated hooks and writes (hooks/changes-queries.ts says which).
 *
 * Mutations take the revision the caller read (`expectedRevision`); a stale
 * one is refused with reason `change_revision_conflict` and changes nothing.
 * StopChange alone may omit it: the kill switch never loses a race. Launch,
 * ramp and roll-out take an optional `stepUpCode`: a protected environment's
 * one-time approval code (RequestChangeApproval), for a caller that is not a
 * console session.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  ChangeUnit as WireUnit,
  type CreateChangeRequestSchema,
  type LaunchChangeRequestSchema,
  type ListChangeEventsRequestSchema,
  type ListChangesRequestSchema,
  type RampChangeRequestSchema,
  type RollOutChangeRequestSchema,
  type SetChangePreviewRequestSchema,
  type StopChangeRequestSchema,
  type UpdateChangeDraftRequestSchema,
} from "@/gen/controlplane/services/changes/v1/changes_pb";

import type { ChangeDraftEdit, ChangePreviewInput, ChangeUnit } from "./changesTypes";

export type ListChangesInit = MessageInitShape<typeof ListChangesRequestSchema>;

export interface ListChangesInput {
  orgId: string;
  environmentId?: string;
  /** The forge project (forge.yaml name). */
  project?: string;
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

/** The most changes one ListChanges page carries. */
export const CHANGES_PAGE_SIZE = 200;
/** The most events one ListChangeEvents page carries. */
export const CHANGE_EVENTS_PAGE_SIZE = 50;

const UNITS: Record<ChangeUnit, WireUnit> = { user: WireUnit.USER, account: WireUnit.ACCOUNT, visitor: WireUnit.VISITOR };

const revision = (n: number): bigint => BigInt(n);

function gated(changeId: string, write: GatedWrite) {
  return { changeId, expectedRevision: revision(write.expectedRevision), ...(write.stepUpCode ? { stepUpCode: write.stepUpCode } : {}) };
}

/** One list's first page; the page token is the reader's (hooks/changes-queries.ts). */
export function listChangesRequest(input: ListChangesInput): ListChangesInit {
  return {
    orgId: input.orgId,
    pageSize: CHANGES_PAGE_SIZE,
    ...(input.environmentId ? { environmentId: input.environmentId } : {}),
    ...(input.project ? { project: input.project } : {}),
  };
}

export function listChangeEventsRequest(changeId: string): MessageInitShape<typeof ListChangeEventsRequestSchema> {
  return { changeId, pageSize: CHANGE_EVENTS_PAGE_SIZE };
}

export function createChangeRequest(input: CreateChangeInput): MessageInitShape<typeof CreateChangeRequestSchema> {
  return { environmentId: input.environmentId, key: input.key, description: input.description, unit: UNITS[input.unit] };
}

export function updateChangeDraftRequest(changeId: string, expectedRevision: number, edit: ChangeDraftEdit): MessageInitShape<typeof UpdateChangeDraftRequestSchema> {
  const { metric } = edit;
  return {
    changeId,
    expectedRevision: revision(expectedRevision),
    ...(edit.description !== undefined ? { description: edit.description } : {}),
    ...(edit.unit !== undefined ? { unit: UNITS[edit.unit] } : {}),
    ...(edit.audience === null ? { clearAudience: true } : edit.audience ? { audience: edit.audience } : {}),
    ...(edit.split !== undefined ? { split: { arms: edit.split } } : {}),
    ...(metric === null
      ? { clearMetric: true }
      : metric
        ? {
            metric: {
              event: metric.event,
              prop: metric.prop,
              propValue: metric.propValue,
              windowHours: metric.windowHours,
              controlVariant: metric.controlVariant,
              ...(metric.minSamplePerArm > 0 ? { minSamplePerArm: BigInt(metric.minSamplePerArm) } : {}),
            },
          }
        : {}),
  };
}

export function setChangePreviewRequest(
  changeId: string,
  expectedRevision: number,
  entries: ChangePreviewInput[],
  activate: boolean
): MessageInitShape<typeof SetChangePreviewRequestSchema> {
  return {
    changeId,
    expectedRevision: revision(expectedRevision),
    entries: entries.map((e) => ({
      unit: e.unitId ? { case: "unitId" as const, value: e.unitId } : { case: "unitHash" as const, value: e.unitHash ?? "" },
      variant: e.variant,
      label: e.label,
    })),
    activate,
  };
}

export function launchChangeRequest(changeId: string, percentBp: number, write: GatedWrite): MessageInitShape<typeof LaunchChangeRequestSchema> {
  return { ...gated(changeId, write), percentBp };
}

export function rampChangeRequest(changeId: string, percentBp: number, write: GatedWrite): MessageInitShape<typeof RampChangeRequestSchema> {
  return { ...gated(changeId, write), percentBp };
}

export function rollOutChangeRequest(changeId: string, input: RollOutInput): MessageInitShape<typeof RollOutChangeRequestSchema> {
  return { ...gated(changeId, input), variant: input.variant, ...(input.everyone ? { everyone: true } : { percentBp: input.percentBp }) };
}

/** Never blocked: no approval, and no revision unless the caller names one. */
export function stopChangeRequest(changeId: string, expectedRevision?: number): MessageInitShape<typeof StopChangeRequestSchema> {
  return expectedRevision ? { changeId, expectedRevision: revision(expectedRevision) } : { changeId };
}
