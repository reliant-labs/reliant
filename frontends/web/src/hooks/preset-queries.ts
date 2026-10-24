/**
 * Presets: PresetService on the generated hooks and keys.
 *
 * Every preset read in the app is a cache entry under `presetServiceKeys`
 * holding exactly what the generated hook fetches — a `ListPresetsResponse`,
 * a `ListPresetsForWorkflowResponse`, or a workflow's defaults as a
 * `GetDefaultPresetResponse` — converted to plain `Preset` values on read.
 * Every write below invalidates `presetServiceKeys.all` and waits for the
 * mounted reads to refetch, so the composer, the run form, the workflow page
 * and Settings → Presets never disagree after a save.
 *
 * Freshness otherwise: preset matching depends on the workflow's inputs, so a
 * workflow change invalidates the same scope (invalidateWorkflowConfig in
 * workflow-config-queries.ts). The staleTime only bounds how long an edit
 * made OUTSIDE the app (a preset YAML changed in an editor) can go unnoticed.
 */

import { create } from "@bufbuild/protobuf";
import type { Value } from "@bufbuild/protobuf/wkt";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";

import { jsToProtoValue, protoValueToJs } from "../api/proto-utils";
import {
  GetDefaultPresetResponseSchema,
  PresetService,
  type GetDefaultPresetResponse,
  type InvalidPreset as ProtoInvalidPreset,
  type ListPresetsResponse,
  type PresetInfo,
} from "../gen/reliant/v1/preset_pb";
import { trackEvent } from "../lib/analytics";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { queryClient } from "../lib/query-client";
import { useProjectStore } from "../store/projectStore";
import {
  presetServiceKeys,
  useListPresets,
  useListPresetsForWorkflow,
} from "./preset-service-hooks_gen";

// --- Frontend types ---

export interface Preset {
  id?: string; // Only present for user presets
  name: string;
  slug?: string; // URL-safe identifier for user presets
  description: string;
  params: Record<string, unknown>;
  source: "builtin" | "project" | "user";
  tag?: string;
  is_hidden?: boolean; // Whether the preset is hidden from the preset picker by default
}

export interface InvalidPreset {
  name: string;
  source: "builtin" | "project" | "user";
  path: string;
  errors: string[];
}

export interface CreatePresetOptions {
  name: string;
  description?: string;
  params: Record<string, unknown>;
  tag?: string;
}

export interface UpdatePresetOptions {
  newName?: string;
  newDescription?: string;
  newParams?: Record<string, unknown>;
  newTag?: string;
}

export interface PresetWriteResult {
  success: boolean;
  error?: string;
  preset?: Preset;
}

function presetFromProto(proto: PresetInfo): Preset {
  const params: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(proto.params)) {
    params[key] = protoValueToJs(value);
  }
  return {
    id: proto.id || undefined,
    name: proto.name,
    slug: proto.slug || undefined,
    description: proto.description,
    params,
    source: proto.source as Preset["source"],
    tag: proto.tag || undefined,
    is_hidden: proto.isHidden || false,
  };
}

function invalidPresetFromProto(inv: ProtoInvalidPreset): InvalidPreset {
  return {
    name: inv.name,
    source: inv.source as InvalidPreset["source"],
    path: inv.path,
    errors: [...inv.errors],
  };
}

function toProtoParams(params: Record<string, unknown>): Record<string, Value> {
  const out: Record<string, Value> = {};
  for (const [key, value] of Object.entries(params)) {
    out[key] = jsToProtoValue(value);
  }
  return out;
}

const client = connectClient(PresetService);

export const PRESET_STALE_TIME_MS = 5 * 60_000;

const EMPTY_PRESETS: Preset[] = [];
const EMPTY_INVALID: InvalidPreset[] = [];

/** A preset list read, converted: stable arrays while the response is unchanged. */
export interface PresetListView {
  presets: Preset[];
  invalidPresets: InvalidPreset[];
  loading: boolean;
  error: string | null;
}

type PresetListResponse = Pick<ListPresetsResponse, "presets" | "invalidPresets">;

function usePresetListView(
  query: { data?: PresetListResponse; isPending: boolean; error: Error | null },
  enabled: boolean,
): PresetListView {
  const { data, isPending, error } = query;
  const presets = useMemo(() => (data ? data.presets.map(presetFromProto) : EMPTY_PRESETS), [data]);
  const invalidPresets = useMemo(
    () => (data?.invalidPresets.length ? data.invalidPresets.map(invalidPresetFromProto) : EMPTY_INVALID),
    [data],
  );
  return {
    presets,
    invalidPresets,
    loading: enabled && isPending,
    error: error ? error.message || "Failed to load presets" : null,
  };
}

// --- Reads ---

/**
 * Every preset in a project (ListPresets). `includeHidden` is for management
 * views; the picker reads without it.
 */
export function useProjectPresets(
  projectId: string | undefined,
  options: { includeHidden?: boolean } = {},
): PresetListView {
  const enabled = !!projectId;
  const query = useListPresets(
    { projectId: projectId ?? "", includeHidden: options.includeHidden ?? false },
    { enabled, staleTime: PRESET_STALE_TIME_MS },
  );
  return usePresetListView(query, enabled);
}

/** A project's presets for an imperative caller, from cache while fresh. */
export async function fetchProjectPresets(projectId: string): Promise<Preset[]> {
  const response = await queryClient.fetchQuery({
    queryKey: presetServiceKeys.listPresets({ projectId, includeHidden: false }),
    queryFn: () => client.listPresets({ projectId, includeHidden: false }),
    staleTime: PRESET_STALE_TIME_MS,
  });
  return response.presets.map(presetFromProto);
}

/**
 * Presets compatible with a workflow (ListPresetsForWorkflow — the server
 * checks the preset's params exist in the workflow and its tag matches), in
 * an explicit project.
 *
 * The project is a parameter, not the current one, because the Run form and
 * the setup dialog act on a project the user chose: presets differ per
 * project, so reading the current one would offer — and store — presets the
 * run's project does not have.
 */
export function useWorkflowPresets(
  projectId: string | undefined,
  workflowName: string,
  options: { includeHidden?: boolean } = {},
): PresetListView {
  const enabled = !!projectId && !!workflowName;
  const query = useListPresetsForWorkflow(
    { projectId: projectId ?? "", workflowName, includeHidden: options.includeHidden ?? false },
    { enabled, staleTime: PRESET_STALE_TIME_MS },
  );
  useEffect(() => {
    if (query.error) {
      logger.error("[useWorkflowPresets] Failed to load presets", { error: query.error, workflowName });
    }
  }, [query.error, workflowName]);
  return usePresetListView(query, enabled);
}

/** Presets compatible with a workflow, in the CURRENT project — the chat composer's reading. */
export function usePresetsForWorkflow(workflowName: string): PresetListView {
  const projectId = useProjectStore((state) => state.currentProject?.id);
  return useWorkflowPresets(projectId, workflowName);
}

// --- Default presets ---
//
// A workflow's default preset per group (group name -> preset name; "" is the
// top-level inputs), cached per workflow under the generated GetDefaultPreset
// key with GetDefaultPreset's own response shape. It is FETCHED through
// GetDefaultPresetsBatch, coalesced per tick:
//
// defaults are asked per WORKFLOW, and a screen showing N agents mounts N
// components that each ask for a different one, so N components produced N
// parallel RPCs. The queue below collects every workflow requested during the
// current microtask and issues ONE GetDefaultPresetsBatch for the lot. A
// microtask (`Promise.resolve().then`) rather than a timer: React mounts a
// subtree's effects synchronously within one commit, so everything on screen
// lands in the same window without adding latency to the first caller.
// Identical concurrent asks never reach the queue twice — the query cache
// dedupes them.

interface PendingBatch {
  workflowNames: Set<string>;
  resolvers: Array<{ workflowName: string; resolve: (value: Record<string, string>) => void }>;
}

const pendingDefaultPresetBatches = new Map<string, PendingBatch>();

async function fetchDefaultPresetsBatch(
  projectId: string,
  workflowNames: string[],
): Promise<Record<string, Record<string, string>>> {
  if (workflowNames.length === 0) return {};
  try {
    const response = await client.getDefaultPresetsBatch({ projectId, workflowNames });
    const result: Record<string, Record<string, string>> = {};
    for (const [workflowName, entry] of Object.entries(response.presetsByWorkflow ?? {})) {
      result[workflowName] = entry.presets ?? {};
    }
    return result;
  } catch (error) {
    // Fail gracefully — the caller's contract is "no defaults", not an error.
    console.warn("[preset-queries] Failed to get default presets (batch):", error);
    return {};
  }
}

function queueDefaultPresets(projectId: string, workflowName: string): Promise<Record<string, string>> {
  let batch = pendingDefaultPresetBatches.get(projectId);
  if (!batch) {
    const created: PendingBatch = { workflowNames: new Set(), resolvers: [] };
    batch = created;
    pendingDefaultPresetBatches.set(projectId, created);
    // Flush at the end of the current microtask, once every caller in this
    // tick has enqueued.
    void Promise.resolve().then(async () => {
      pendingDefaultPresetBatches.delete(projectId);
      const byWorkflow = await fetchDefaultPresetsBatch(projectId, [...created.workflowNames]);
      for (const { workflowName: name, resolve } of created.resolvers) {
        // A workflow with no defaults is omitted from the response; that is
        // the same "{}" the single-workflow RPC returns for it.
        resolve(byWorkflow[name] ?? {});
      }
    });
  }
  batch.workflowNames.add(workflowName);
  const target = batch;
  return new Promise<Record<string, string>>((resolve) => {
    target.resolvers.push({ workflowName, resolve });
  });
}

function defaultPresetsQuery(projectId: string, workflowName: string) {
  return {
    queryKey: presetServiceKeys.getDefaultPreset({ projectId, workflowName }),
    queryFn: async (): Promise<GetDefaultPresetResponse> =>
      create(GetDefaultPresetResponseSchema, { presets: await queueDefaultPresets(projectId, workflowName) }),
    staleTime: PRESET_STALE_TIME_MS,
  };
}

/** A workflow's default preset per group, for an imperative caller: from cache while fresh. */
export async function fetchDefaultPresets(projectId: string, workflowName: string): Promise<Record<string, string>> {
  return (await queryClient.fetchQuery(defaultPresetsQuery(projectId, workflowName))).presets;
}

const NO_DEFAULTS: Record<string, string> = {};

/** A workflow's default preset per group, as a mounted read. */
export function useDefaultPresets(
  projectId: string | undefined,
  workflowName: string | undefined,
): { defaults: Record<string, string>; loading: boolean } {
  const query = useQuery({
    ...defaultPresetsQuery(projectId ?? "", workflowName ?? ""),
    enabled: !!projectId && !!workflowName,
  });
  return { defaults: query.data?.presets ?? NO_DEFAULTS, loading: query.isLoading };
}

// --- Writes ---

/**
 * Something changed the project's presets: refetch every mounted preset read
 * (lists and defaults). Resolves once they have, so a caller that selects
 * what it just saved finds it in the list.
 */
export function invalidatePresets(): Promise<void> {
  return queryClient.invalidateQueries({ queryKey: presetServiceKeys.all });
}

/** Create a preset in the project's .reliant/presets/ directory. */
export async function createPreset(projectId: string, options: CreatePresetOptions): Promise<PresetWriteResult> {
  const response = await client.createPreset({
    projectId,
    name: options.name,
    description: options.description || "",
    params: toProtoParams(options.params),
    tag: options.tag || "",
  });
  if (response.success) {
    trackEvent("preset_created");
    await invalidatePresets();
  }
  return {
    success: response.success,
    error: response.error || undefined,
    preset: response.preset ? presetFromProto(response.preset) : undefined,
  };
}

/** Update a project preset (name, description, tag or params). */
export async function updatePreset(
  projectId: string,
  name: string,
  updates: UpdatePresetOptions,
): Promise<PresetWriteResult> {
  const response = await client.updatePreset({
    projectId,
    name,
    newName: updates.newName,
    newDescription: updates.newDescription,
    newParams: updates.newParams ? toProtoParams(updates.newParams) : undefined,
    newTag: updates.newTag,
  });
  if (response.success) await invalidatePresets();
  return {
    success: response.success,
    error: response.error || undefined,
    preset: response.preset ? presetFromProto(response.preset) : undefined,
  };
}

/** Delete a project preset file. */
export async function deletePreset(projectId: string, name: string): Promise<PresetWriteResult> {
  const response = await client.deletePreset({ projectId, name });
  if (response.success) await invalidatePresets();
  return { success: response.success, error: response.error || undefined };
}

/**
 * Set (or, with a null preset, clear) the default preset for one group of a
 * workflow. `groupName` "" is the top-level inputs.
 */
export async function setDefaultPreset(
  projectId: string,
  workflowName: string,
  groupName: string,
  presetName: string | null,
): Promise<PresetWriteResult> {
  const response = await client.setDefaultPreset({
    projectId,
    workflowName,
    groupName: groupName || undefined,
    presetName: presetName || undefined,
  });
  if (response.success) await invalidatePresets();
  return { success: response.success, error: response.error || undefined };
}
