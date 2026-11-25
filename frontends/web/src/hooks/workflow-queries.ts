// Copyright (c) 2025 Reliant Labs

/**
 * Workflows: WorkflowService on the generated hooks and keys.
 *
 * Every workflow read in the app is a cache entry under `workflowServiceKeys`
 * holding exactly what the generated hook fetches — a `ListWorkflowsResponse`
 * per (project, includeHidden), a `GetWorkflowResponse` per (project, name or
 * draft id) — converted to view types on read with `select`. There is no
 * second copy: the chat pickers, the Library, the builder, the automation
 * form and the mobile catalog all read the same entries.
 *
 * Freshness:
 *  - every write below invalidates `workflowServiceKeys.all` (and the preset
 *    reads, which the server matches against a workflow's inputs) and waits
 *    for the mounted reads to refetch, so a caller that navigates to what it
 *    just saved finds it;
 *  - every workflow-draft write, whoever made it (an agent's tool call,
 *    another tab), arrives as WORKFLOW_DRAFT_UPDATED on the user stream,
 *    which invalidates the same scope (globalUpdatesStore);
 *  - the staleTime only bounds how long an edit made OUTSIDE the app (a
 *    project workflow's YAML changed in an editor) can go unnoticed.
 *
 * The builder loads the definition it edits with `fetchWorkflow(..., { fresh:
 * true })` rather than a mounted query: its canvas is local state seeded once
 * per load, and a background refetch must not replace unsaved edits.
 */

import { create } from "@bufbuild/protobuf";
import { useQuery } from "@tanstack/react-query";
import {
  WorkflowService,
  GetWorkflowRequestSchema,
  type GetWorkflowResponse,
  type ListWorkflowsResponse,
  type WorkflowListItem as ProtoWorkflowListItem,
  type ValidationError,
} from "../gen/reliant/v1/workflow_pb";
import { ResourceVisibility } from "../gen/reliant/v1/common_pb";
import {
  draftStatusFromProto,
  draftStatusToProto,
  type DraftStatus,
} from "../components/workflow/workflowDraftStatus";
import { connectClient } from "../lib/connect";
import { getOrgScope, orgQueryEnabled, orgRequest, useOrgScope, whenOrgScopeReady, withOrgFailure } from "../lib/orgScope";
import { queryClient } from "../lib/query-client";
import { toWorkflowMessage } from "../lib/workflowProto";
import { useProjectStore } from "../store/projectStore";
import type { Edge, Step, Workflow } from "../types/workflow";
import { presetServiceKeys } from "./preset-service-hooks_gen";
import { triggerServiceKeys } from "./trigger-service-hooks_gen";
import {
  useGetWorkflow,
  useListWorkflows,
  workflowServiceKeys,
  workflowServiceQueries,
} from "./workflow-service-hooks_gen";

export type {
  Workflow,
  Step,
  Edge,
  EdgeCase,
  Param,
  NodeThreadConfig,
  SaveMessageConfig,
  InjectConfig,
  ProjectConfig,
  PresetsConfig,
  WorkflowUI,
  Position,
  SwitchMetadata,
  SwitchCase,
} from "../types/workflow";
export type { ValidationError, HighlightSpan } from "../gen/reliant/v1/workflow_pb";

const client = connectClient(WorkflowService);

export const WORKFLOW_STALE_TIME_MS = 5 * 60_000;

// ============================================
// View types
// ============================================

type WorkflowSource = "builtin" | "user" | "project";

/** One workflow of a ListWorkflows response, as the views read it. */
export interface WorkflowResponse {
  name: string;
  filename: string;
  description?: string;
  stepCount: number;
  source: WorkflowSource;
  /** Lifecycle; builtin and project workflows are always "complete". */
  status: DraftStatus;
  /** Current findings, computed on read (errors, then "warning:*"). */
  validationErrors: ValidationError[];
  nodes: Step[];
  edges: Edge[];
  updatedAt?: string;
  isHidden?: boolean;
  /** Definition-level display name; empty when the workflow has none. */
  title?: string;
  hasPresetGroups?: boolean;
  draftId?: string;
  /**
   * Why this workflow cannot run without the user's machine, at its input
   * defaults. Empty: it can run with no machine (research/DAEMONLESS_RUNS.md).
   * Absent on responses built client-side, which carry no analysis — read
   * that as unknown, never as "runs without one".
   */
  needsMachine?: string[];
  /**
   * The definition's Chat trigger is off (`automation_only`): a chat cannot
   * start it, so every chat picker leaves it out (see isChatLaunchable).
   */
  automationOnly?: boolean;
  /** The definition's declared `triggers:` (its WHEN). */
  triggers?: ProtoWorkflowListItem["triggers"];
  /** The definition's declared `permissions:` (e.g. "approvals:decide"). */
  permissions?: string[];
  /**
   * A saved workflow's (source "user") organization; absent for builtin and
   * project workflows, and for a saved workflow with no org (not shareable).
   */
  orgId?: string;
  /**
   * Whether a saved workflow is shared with its org's members, to read and
   * fork (SharingService.SetWorkflowDraftSharing). Absent for builtin and
   * project workflows, and from a server older than the field.
   */
  shared?: boolean;
}

export interface InvalidWorkflow {
  name: string;
  source: WorkflowSource;
  path: string;
  errors: string[];
}

export interface ListWorkflowsResult {
  workflows: WorkflowResponse[];
  invalidWorkflows: InvalidWorkflow[];
}

/**
 * The chat-safe list's row shape (the composer, the slash menu, the agent
 * selector, the mobile catalog): snake_case, as those views were written
 * against it.
 */
export interface WorkflowDef {
  name: string;
  filename: string; // Filename/slug used for API calls
  description: string;
  step_count: number;
  source: WorkflowSource;
  updated_at?: string;
  title?: string; // Definition-level display name
  is_hidden?: boolean; // Whether the workflow is hidden from the workflow dropdown
  status?: DraftStatus; // Lifecycle: drafts are never runnable
  automation_only?: boolean; // Chat trigger off: chat pickers must not offer it (isChatLaunchable)
  // The graph travels on the wire already. Dropping it made the app
  // contradict itself one tap apart: the mobile catalog row read
  // `step_count` and said "17 steps", while the detail screen read `nodes`
  // and rendered "This workflow has no steps".
  nodes: Step[];
  edges: Edge[];
}

/** A GetWorkflow response, as the views read it. */
export interface GetWorkflowResult {
  /** Absent when the stored YAML failed to parse (`parseError` says why). */
  workflow?: Workflow;
  draftId?: string;
  version: number;
  parseError?: string;
  rawDefinition?: string;
  source?: WorkflowSource;
  sourcePath?: string;
  yamlDefinition?: string;
  status: DraftStatus;
  validationErrors: ValidationError[];
  /** As WorkflowResponse.orgId: a saved workflow's organization. */
  orgId?: string;
  /** As WorkflowResponse.shared: whether a saved workflow is shared with its org. */
  shared?: boolean;
}

export interface ValidationResult {
  valid: boolean;
  errors: ValidationError[];
}

export interface SaveWorkflowResult {
  success: boolean;
  message: string;
  workflow?: Workflow;
  /** Whether THIS request's definition passed validation. */
  isValid: boolean;
  validationErrors: ValidationError[];
  id: string;
  slug: string;
  version: number;
  yamlDefinition?: string;
  /** Resulting status; meaningful only when success. */
  status: DraftStatus;
}

export interface SetWorkflowStatusResult {
  success: boolean;
  message: string;
  status: DraftStatus;
  validationErrors: ValidationError[];
  version: number;
}

export interface ImportWorkflowResult {
  success: boolean;
  message: string;
  workflow?: Workflow;
  id: string;
  slug: string;
  isValid: boolean;
  validationErrors: ValidationError[];
  conflict: boolean;
  existingId: string;
  status: DraftStatus;
}

export interface ExportWorkflowResult {
  success: boolean;
  yamlContent: Uint8Array;
  filename: string;
  workflow?: Workflow;
}

// ============================================
// Mappers
// ============================================

/** "global" and "worktree" are where a user's own workflows live: "user". */
function sourceFromProto(source: string | undefined): WorkflowSource {
  const s = source || "user";
  return (s === "global" || s === "worktree" ? "user" : s) as WorkflowSource;
}

function listItemFromProto(proto: ProtoWorkflowListItem): WorkflowResponse {
  return {
    name: proto.name,
    filename: proto.filename,
    description: proto.description || undefined,
    stepCount: proto.stepCount,
    source: sourceFromProto(proto.source),
    nodes: proto.nodes as unknown as Step[],
    edges: proto.edges as unknown as Edge[],
    updatedAt: proto.updatedAt || undefined,
    status: draftStatusFromProto(proto.status),
    validationErrors: [...(proto.validationErrors || [])],
    isHidden: proto.isHidden || false,
    title: proto.title || undefined,
    hasPresetGroups: proto.hasPresetGroups || false,
    draftId: proto.draftId || undefined,
    needsMachine: [...(proto.needsMachine || [])],
    automationOnly: proto.automationOnly || false,
    triggers: [...(proto.triggers || [])],
    permissions: [...(proto.permissions || [])],
    orgId: proto.orgId || undefined,
    shared: sharedFromProto(proto.visibility),
  };
}

/** ORG is shared, PRIVATE is not; UNSPECIFIED (builtin, project, an older server) is unknown. */
function sharedFromProto(visibility: ResourceVisibility): boolean | undefined {
  if (visibility === ResourceVisibility.ORG) return true;
  if (visibility === ResourceVisibility.PRIVATE) return false;
  return undefined;
}

/** A ListWorkflows response as the views read it (a `select`). */
export function workflowListFromProto(response: ListWorkflowsResponse): ListWorkflowsResult {
  return {
    workflows: response.workflows.map(listItemFromProto),
    invalidWorkflows: (response.invalidWorkflows || []).map((inv) => ({
      name: inv.name,
      source: inv.source as WorkflowSource,
      path: inv.path,
      errors: [...inv.errors],
    })),
  };
}

function workflowDefFromProto(w: ProtoWorkflowListItem): WorkflowDef {
  return {
    name: w.name,
    filename: w.filename,
    description: w.description || "",
    step_count: w.stepCount,
    source: sourceFromProto(w.source),
    is_hidden: w.isHidden || false,
    title: w.title || undefined,
    status: draftStatusFromProto(w.status), // builtin/project are always complete
    automation_only: w.automationOnly || false,
    nodes: w.nodes as unknown as Step[],
    edges: w.edges as unknown as Edge[],
  };
}

/** A ListWorkflows response as the chat-safe rows (a `select`). */
export function workflowDefsFromProto(response: ListWorkflowsResponse): WorkflowDef[] {
  return response.workflows.map(workflowDefFromProto);
}

/**
 * A GetWorkflow response as the views read it (a `select`). Throws when the
 * response holds neither a workflow nor a parse error: the workflow is gone.
 */
export function workflowFromProto(response: GetWorkflowResponse): GetWorkflowResult {
  const common = {
    draftId: response.draftId || undefined,
    version: Number(response.version),
    source: sourceFromProto(response.source),
    sourcePath: response.sourcePath || undefined,
    yamlDefinition: response.yamlDefinition || undefined,
    status: draftStatusFromProto(response.status),
    validationErrors: [...(response.validationErrors || [])],
    orgId: response.orgId || undefined,
    shared: sharedFromProto(response.visibility),
  };
  if (response.parseError) {
    return { ...common, parseError: response.parseError, rawDefinition: response.rawDefinition || undefined };
  }
  if (!response.workflow) {
    throw new Error("Workflow not found");
  }
  return { ...common, workflow: response.workflow as unknown as Workflow };
}

// ============================================
// Chat-picker rules
// ============================================

/**
 * Whether a chat can start this workflow: its Chat trigger is on. The one
 * rule every chat picker (composer, slash menu, new-chat views) filters by,
 * read from the server's list item so they all agree. The server refuses a
 * chat start of any other workflow, so a picker that skipped this would only
 * offer an error.
 */
export function isChatLaunchable(workflow: { automationOnly?: boolean; automation_only?: boolean }): boolean {
  return !(workflow.automationOnly ?? workflow.automation_only ?? false);
}

/**
 * Whether a chat picker offers this workflow: a chat can start it
 * (isChatLaunchable) and its definition is not a building block
 * (`hidden: true`). The server already leaves hidden workflows out of the
 * default listing; checking here too keeps a picker right whichever list it
 * is handed. Every chat picker (composer, slash menu, agent selector, mobile
 * new chat) filters by this one rule.
 */
export function isOfferedInChatPicker(workflow: {
  automationOnly?: boolean;
  automation_only?: boolean;
  isHidden?: boolean;
  is_hidden?: boolean;
}): boolean {
  return isChatLaunchable(workflow) && !(workflow.isHidden ?? workflow.is_hidden ?? false);
}

// ============================================
// Reads
// ============================================

/**
 * A project's workflow list in `orgId`: the request names the tab's org
 * (lib/orgScope), so every cache entry of it says which org it was read in.
 */
export function workflowListRequest(projectId: string, includeHidden: boolean, orgId: string | null) {
  return orgRequest({ projectId, includeHidden }, orgId);
}

/**
 * A project's workflows and the ones that failed to load. `includeHidden` is
 * for management views (the Library, the builder, the Run form); the chat
 * pickers read without it (useWorkflows).
 */
export function useWorkflowList(
  projectId: string | undefined,
  options: { includeHidden?: boolean; enabled?: boolean } = {},
) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      workflowServiceQueries.listWorkflows(
        workflowListRequest(projectId ?? "", options.includeHidden ?? false, org.orgId),
        {
          enabled: !!projectId && (options.enabled ?? true) && orgQueryEnabled(org),
          staleTime: WORKFLOW_STALE_TIME_MS,
          select: workflowListFromProto,
        },
      ),
      org,
    ),
  );
}

const selectListedWorkflows = (response: ListWorkflowsResponse) => workflowListFromProto(response).workflows;

/**
 * The workflows of an EXPLICIT project (not the current one), for a form that
 * picks a workflow in the project it has selected: the automation form.
 */
export function useProjectWorkflowList(projectId: string | undefined) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      workflowServiceQueries.listWorkflows(workflowListRequest(projectId ?? "", false, org.orgId), {
        enabled: !!projectId && orgQueryEnabled(org),
        staleTime: WORKFLOW_STALE_TIME_MS,
        select: selectListedWorkflows,
      }),
      org,
    ),
  );
}

/** A project's workflows for an imperative caller, from cache while fresh. */
export async function fetchWorkflowList(projectId: string, includeHidden = false): Promise<ListWorkflowsResult> {
  const scope = getOrgScope();
  const { orgId } = scope.ready ? scope : await whenOrgScopeReady();
  return workflowListFromProto(
    await queryClient.fetchQuery(
      workflowServiceQueries.listWorkflows(workflowListRequest(projectId, includeHidden, orgId), {
        staleTime: WORKFLOW_STALE_TIME_MS,
      }),
    ),
  );
}

const NO_WORKFLOW_DEFS: WorkflowDef[] = [];

/**
 * The CURRENT project's chat-safe workflows (runnable, visible): what the
 * composer, the slash menu, the agent selector and the mobile catalog offer.
 */
export function useWorkflows(): { workflows: WorkflowDef[]; loading: boolean; error: string | null } {
  const projectId = useProjectStore((state) => state.currentProject?.id);
  const org = useOrgScope();
  const query = useQuery(
    withOrgFailure(
      workflowServiceQueries.listWorkflows(workflowListRequest(projectId ?? "", false, org.orgId), {
        enabled: !!projectId && orgQueryEnabled(org),
        staleTime: WORKFLOW_STALE_TIME_MS,
        select: workflowDefsFromProto,
      }),
      org,
    ),
  );
  return {
    workflows: query.data ?? NO_WORKFLOW_DEFS,
    loading: !!projectId && query.isPending,
    error: query.error ? query.error.message || "Failed to fetch workflows" : null,
  };
}

/**
 * The current project's chat-safe workflows as far as the cache already
 * knows them, without fetching: for lookups (a display name) that must not
 * cost a request of their own.
 */
export function useCachedWorkflows(): WorkflowDef[] {
  const projectId = useProjectStore((state) => state.currentProject?.id);
  const org = useOrgScope();
  const query = useListWorkflows(
    workflowListRequest(projectId ?? "", false, org.orgId),
    { enabled: false, select: workflowDefsFromProto },
  );
  return query.data ?? NO_WORKFLOW_DEFS;
}

/** Non-reactive: the current project's cached chat-safe workflows (no network). */
export function getCachedWorkflows(): WorkflowDef[] {
  const projectId = useProjectStore.getState().currentProject?.id;
  if (!projectId) return NO_WORKFLOW_DEFS;
  const data = queryClient.getQueryData<ListWorkflowsResponse>(
    workflowServiceKeys.listWorkflows(workflowListRequest(projectId, false, getOrgScope().orgId)),
  );
  return data ? workflowDefsFromProto(data) : NO_WORKFLOW_DEFS;
}

type WorkflowRef = { name?: string; draftId?: string };

function getRequest(projectId: string, ref: WorkflowRef) {
  return create(GetWorkflowRequestSchema, { projectId, name: ref.name || "", draftId: ref.draftId });
}

/**
 * A workflow's definition by name, as a mounted read. `data.workflow` is
 * undefined when the workflow failed to parse (`data.parseError` says why).
 */
export function useWorkflowDefinitionQuery(projectId: string | undefined, workflowName: string | undefined) {
  return useGetWorkflow(getRequest(projectId ?? "", { name: workflowName ?? "" }), {
    enabled: !!projectId && !!workflowName,
    staleTime: WORKFLOW_STALE_TIME_MS,
    select: workflowFromProto,
  });
}

/**
 * A workflow by name or draft id, for an imperative caller. From cache while
 * fresh, unless `fresh`: a load the user will edit and save (the builder,
 * its draft sync) must carry the server's current version, or the save that
 * follows is refused as stale.
 */
export async function fetchWorkflow(
  projectId: string,
  ref: WorkflowRef,
  options: { fresh?: boolean } = {},
): Promise<GetWorkflowResult> {
  const response = await queryClient.fetchQuery(
    workflowServiceQueries.getWorkflow(getRequest(projectId, ref), {
      staleTime: options.fresh ? 0 : WORKFLOW_STALE_TIME_MS,
    }),
  );
  return workflowFromProto(response);
}

/**
 * A workflow's definition by name. Throws if the workflow is not found or
 * its stored YAML does not parse.
 */
export async function getWorkflow(projectId: string, name: string): Promise<Workflow> {
  const result = await fetchWorkflow(projectId, { name });
  if (!result.workflow) {
    throw new Error(result.parseError || "Workflow not found");
  }
  return result.workflow;
}

/**
 * Validate a workflow without saving it. A read (NO_SIDE_EFFECTS), so it
 * invalidates nothing; and not cached, since every call carries a new canvas.
 */
export async function validateWorkflow(projectId: string, workflow: Workflow): Promise<ValidationResult> {
  const response = await client.validateWorkflow({ projectId, workflow: toWorkflowMessage(workflow) });
  return { valid: response.valid, errors: response.errors };
}

/** A workflow as YAML. A read: not cached (the bytes are a one-shot download). */
export async function exportWorkflow(
  projectId: string,
  slug: string,
  worktreeId?: string,
): Promise<ExportWorkflowResult> {
  const response = await client.exportWorkflow({ projectId, slug, worktreeId });
  return {
    success: response.success,
    yamlContent: response.yamlContent,
    filename: response.filename,
    workflow: response.workflow as unknown as Workflow | undefined,
  };
}

/** Download a workflow as a YAML file. */
export async function downloadWorkflow(projectId: string, slug: string, worktreeId?: string): Promise<void> {
  const response = await exportWorkflow(projectId, slug, worktreeId);
  if (!response.success) {
    throw new Error("Failed to export workflow");
  }
  const blob = new Blob([new Uint8Array(response.yamlContent)], { type: "application/x-yaml" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = response.filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

// ============================================
// Writes
// ============================================

/**
 * Something changed the project's workflows: refetch every mounted workflow
 * read, and the preset reads — presets are matched against a workflow's
 * inputs server-side, so a workflow change can change the compatible preset
 * lists too. Resolves once the mounted reads have refetched.
 */
export function invalidateWorkflows(): Promise<void> {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: workflowServiceKeys.all }),
    queryClient.invalidateQueries({ queryKey: presetServiceKeys.all }),
  ]).then(() => undefined);
}

/**
 * The options a generated workflow write hook (useDeleteWorkflow,
 * useCopyWorkflow, …) takes so the write keeps invalidateWorkflows()'s
 * contract: the preset reads refresh beside the workflow reads, and the write
 * settles only once the mounted reads have refetched (React Query awaits
 * onSettled), so a caller that closes a dialog or navigates finds the change.
 * It replaces the generated refresh (the workflowService scope alone, not
 * awaited) rather than adding to it.
 *
 * `ifSucceeded`: a response with success=false changed nothing (copy and
 * import refuse a write that way), so nothing refreshes. `triggers`: the write
 * also removed triggers (a deleted workflow takes its automations with it).
 */
export function workflowWriteRefresh(opts: { ifSucceeded?: boolean; triggers?: boolean } = {}) {
  return {
    invalidate: false as const,
    onSettled: async (response: object | undefined) => {
      if (!response || (opts.ifSucceeded && "success" in response && !response.success)) return;
      await Promise.all([
        invalidateWorkflows(),
        opts.triggers ? queryClient.invalidateQueries({ queryKey: triggerServiceKeys.all }) : undefined,
      ]);
    },
  };
}

/** An ImportWorkflow request: the server takes the YAML as bytes. */
export function importWorkflowRequest(projectId: string, yamlContent: string | Uint8Array, overwrite = false) {
  const yamlBytes = typeof yamlContent === "string" ? new TextEncoder().encode(yamlContent) : yamlContent;
  return { projectId, yamlContent: yamlBytes, overwrite };
}

/**
 * Save a workflow (creates or updates).
 *
 * `status` is the intent: "draft" stores as-is (findings returned, not
 * enforced); "complete" validates and rejects on errors; omitted keeps the
 * current status (new workflows start as drafts), so re-saving a complete
 * workflow is gated.
 */
export async function saveWorkflow(
  projectId: string,
  workflow: Workflow,
  expectedVersion?: number,
  sourcePath?: string,
  draftId?: string,
  status?: DraftStatus,
): Promise<SaveWorkflowResult> {
  const response = await client.saveWorkflow({
    projectId,
    workflow: toWorkflowMessage(workflow),
    expectedVersion: expectedVersion ? BigInt(expectedVersion) : undefined,
    sourcePath: sourcePath || undefined,
    draftId: draftId || undefined,
    status: status ? draftStatusToProto(status) : undefined,
  });
  if (response.success) await invalidateWorkflows();
  return {
    success: response.success,
    message: response.message,
    workflow: response.workflow as unknown as Workflow | undefined,
    isValid: response.isValid,
    validationErrors: response.validationErrors,
    id: response.id,
    slug: response.slug,
    version: Number(response.version),
    yamlDefinition: response.yamlDefinition || undefined,
    status: draftStatusFromProto(response.status),
  };
}

/**
 * Move a stored workflow between draft and complete. Marking complete
 * validates the stored definition and is rejected (success=false, with the
 * errors) when it is invalid.
 */
export async function setWorkflowStatus(
  projectId: string,
  draftId: string,
  status: DraftStatus,
  expectedVersion?: number,
): Promise<SetWorkflowStatusResult> {
  const response = await client.setWorkflowStatus({
    projectId,
    draftId,
    status: draftStatusToProto(status),
    expectedVersion: expectedVersion ? BigInt(expectedVersion) : undefined,
  });
  if (response.success) await invalidateWorkflows();
  return {
    success: response.success,
    message: response.message,
    status: draftStatusFromProto(response.status),
    validationErrors: [...response.validationErrors],
    version: Number(response.version),
  };
}

/** Delete a workflow. The server deletes the workflow's automations with it. */
export async function deleteWorkflow(projectId: string, name: string): Promise<void> {
  await client.deleteWorkflow({ projectId, name });
  await Promise.all([invalidateWorkflows(), queryClient.invalidateQueries({ queryKey: triggerServiceKeys.all })]);
}

/** Import a workflow from YAML content, outside React (the onboarding journey seeds one). */
export async function importWorkflow(
  projectId: string,
  yamlContent: string | Uint8Array,
  overwrite = false,
): Promise<ImportWorkflowResult> {
  const response = await client.importWorkflow(importWorkflowRequest(projectId, yamlContent, overwrite));
  if (response.success) await invalidateWorkflows();
  return {
    success: response.success,
    message: response.message,
    workflow: response.workflow as unknown as Workflow | undefined,
    id: response.id,
    slug: response.slug,
    isValid: response.isValid,
    validationErrors: response.validationErrors,
    conflict: response.conflict,
    existingId: response.existingId,
    status: draftStatusFromProto(response.status),
  };
}
