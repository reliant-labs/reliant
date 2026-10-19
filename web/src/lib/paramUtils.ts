// Copyright (c) 2025 Reliant Labs

/**
 * Convert the composer's flat group keys to the nested shape the server stores.
 *
 * Example (groups = {"agent"}):
 *   Input:  { "agent.model": "claude", "agent.temperature": 0.7, "mode": "fast" }
 *   Output: { "agent": { "model": "claude", "temperature": 0.7 }, "mode": "fast" }
 *
 * The composer keys a group's params as "<group>.<param>"; a run's inputs, the
 * send payload and the chat's stored selection nest them. Only keys under a
 * known group are nested: a key with a dot that is not one stays as it is, and
 * the send boundary refuses it rather than this guessing a group out of it.
 */
export function flatToNestedParams(
  flatParams: Record<string, unknown>,
  groupNames: ReadonlySet<string>
): Record<string, unknown> {
  const nested: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(flatParams)) {
    const dotIndex = key.indexOf(".");
    const groupName = dotIndex > 0 ? key.substring(0, dotIndex) : "";

    if (groupName && groupNames.has(groupName)) {
      if (!isPlainObject(nested[groupName])) {
        nested[groupName] = {};
      }
      (nested[groupName] as Record<string, unknown>)[key.substring(dotIndex + 1)] = value;
    } else {
      nested[key] = value;
    }
  }

  return nested;
}

/**
 * The flat keys' edits as a server patch: the changed keys, nested, with a
 * changed group sent WHOLE.
 *
 * A signalled or stored key is replaced as a whole value, so a patch carrying
 * only {"agent": {"model": ...}} would erase the group's other params in the
 * run and in the selection. A group with any changed param therefore goes out
 * with every param the composer holds for it.
 */
export function nestChangedParams(
  changed: Record<string, unknown>,
  all: Record<string, unknown>,
  groupNames: ReadonlySet<string>
): Record<string, unknown> {
  const groupsTouched = new Set<string>();
  const patch: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(changed)) {
    const dotIndex = key.indexOf(".");
    const groupName = dotIndex > 0 ? key.substring(0, dotIndex) : "";
    if (groupName && groupNames.has(groupName)) {
      groupsTouched.add(groupName);
    } else {
      patch[key] = value;
    }
  }
  if (groupsTouched.size === 0) return patch;

  const wholeGroups = flatToNestedParams(
    Object.fromEntries(
      Object.entries(all).filter(([key]) => groupsTouched.has(key.split(".", 1)[0]) && key.includes("."))
    ),
    groupNames
  );
  return { ...patch, ...wholeGroups };
}

/**
 * Convert nested params to the composer's flat group keys, the inverse of
 * flatToNestedParams.
 *
 * Example (groups = {"agent"}):
 *   Input:  { "agent": { "model": "claude" }, "model": { "id": "gpt" }, "mode": "fast" }
 *   Output: { "agent.model": "claude", "model": { "id": "gpt" }, "mode": "fast" }
 *
 * Only the schema's group inputs are flattened. A top-level param whose value
 * is an object — a model selector — is a value and stays whole; flattening it
 * turned {model: {id}} into "model.id", which no input is named, so the
 * composer could never adopt the model a run was really using.
 */
export function nestedToFlatParams(
  nestedParams: Record<string, unknown>,
  groupNames: ReadonlySet<string>
): Record<string, unknown> {
  const flat: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(nestedParams)) {
    if (groupNames.has(key) && isPlainObject(value)) {
      for (const [nestedKey, nestedValue] of Object.entries(value)) {
        flat[`${key}.${nestedKey}`] = nestedValue;
      }
    } else {
      flat[key] = value;
    }
  }

  return flat;
}

/**
 * The group names in a flattened input schema (`buildWorkflowInputsFromProto`
 * names a group's params "<group>.<param>").
 */
export function groupNamesOf(
  inputs: Record<string, unknown> | null | undefined
): ReadonlySet<string> {
  const groups = new Set<string>();
  for (const key of Object.keys(inputs ?? {})) {
    const dotIndex = key.indexOf(".");
    if (dotIndex > 0) groups.add(key.substring(0, dotIndex));
  }
  return groups;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Structural equality for param values.
 *
 * Param values are plain JSON (strings, numbers, model selector objects), and
 * they cross the wire on every sync, so a value that round-trips through the
 * server comes back as an equal-but-distinct object. Callers that compare
 * server state to local state need value equality, not reference equality.
 */
export function paramValuesEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;

  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    return a.every((item, index) => paramValuesEqual(item, b[index]));
  }

  if (isPlainObject(a) && isPlainObject(b)) {
    const aKeys = Object.keys(a);
    const bKeys = Object.keys(b);
    if (aKeys.length !== bKeys.length) return false;
    return aKeys.every(
      (key) => Object.hasOwn(b, key) && paramValuesEqual(a[key], b[key])
    );
  }

  return false;
}

/**
 * Adopt the chat's server-side params into the composer, keeping what the user
 * has edited and not yet pushed.
 *
 * `local` is what the composer shows, `synced` the baseline last known to be on
 * the server, and `server` the chat's selection now (flat keys). A key is DIRTY
 * when `local` differs from the baseline: the user's newer value wins, and the
 * next push carries it. Every other key takes the server's value — and a key the
 * server no longer holds is dropped. That is how the pill converges on what
 * actually runs: another device's pick, or a model the server moved off a
 * provider that cannot serve it.
 *
 * The new baseline is the server's value for every adopted key, and the old
 * baseline for dirty ones.
 */
export function adoptServerParams(
  local: Record<string, unknown>,
  synced: Record<string, unknown>,
  server: Record<string, unknown>
): { params: Record<string, unknown>; synced: Record<string, unknown> } {
  const params: Record<string, unknown> = {};
  const baseline: Record<string, unknown> = {};
  const keys = new Set([...Object.keys(local), ...Object.keys(server), ...Object.keys(synced)]);

  for (const key of keys) {
    if (!paramValuesEqual(local[key], synced[key])) {
      if (Object.hasOwn(local, key)) params[key] = local[key];
      if (Object.hasOwn(synced, key)) baseline[key] = synced[key];
      continue;
    }
    if (Object.hasOwn(server, key)) {
      params[key] = server[key];
      baseline[key] = server[key];
    }
  }
  return { params, synced: baseline };
}

/** The keys of `local` that differ from the baseline: what a push would carry. */
export function unsyncedParams(
  local: Record<string, unknown>,
  synced: Record<string, unknown>
): Record<string, unknown> {
  const changed: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(local)) {
    if (!paramValuesEqual(synced[key], value)) changed[key] = value;
  }
  return changed;
}

/**
 * Whether a selected thread's param edits can be pushed to the server. A
 * running thread applies them at once. A sub-agent that stopped on an error
 * (`resumable`) is not running, but a message resumes it, and it resumes with
 * these params — switching its model to another provider is how a user gets it
 * past a provider's usage limit. A finished thread has no next run to apply
 * them to.
 */
export function threadParamsAcceptChanges(
  thread: { isRunning: boolean; resumable: boolean } | null | undefined
): boolean {
  return thread != null && (thread.isRunning || thread.resumable);
}

/**
 * Check if a model input is effectively required (has no meaningful default).
 * 
 * Model inputs with default: '' are considered required because empty string
 * is not a valid model selection.
 */
export function isModelInputRequired(
  schema: { type?: string; default?: unknown }
): boolean {
  if (schema.type !== "model") return false;
  
  const hasDefault = schema.default !== undefined && 
                     schema.default !== null && 
                     schema.default !== "";
  
  return !hasDefault;
}

/**
 * Check if a param has a meaningful default value.
 * Special handling for model type where empty string is not meaningful.
 */
export function hasDefaultValue(schema: { type?: string; default?: unknown }): boolean {
  if (schema.default === undefined || schema.default === null) {
    return false;
  }

  // Model type: empty string is not a meaningful default
  if (schema.type === "model" && schema.default === "") {
    return false;
  }

  return true;
}

import { protoValueToJs } from "../api/proto-utils";

/**
 * Unwrap google.protobuf.Value (e.g. { $typeName, kind: { case, value } }) to the actual JS value.
 * Delegates to the canonical protoValueToJs from proto-utils.
 */
export const unwrapProtoValue = protoValueToJs;

/**
 * Format a value for display in the UI. Avoids "[object Object]" when value is an object.
 * Unwraps google.protobuf.Value so "{{inputs.model}}" is shown, not the raw proto JSON.
 */
export function formatValueForDisplay(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (Array.isArray(value)) return JSON.stringify(value);
  if (typeof value === "object") {
    const obj = value as Record<string, unknown>;
    if ("text" in obj && typeof obj.text === "string" && Object.keys(obj).length <= 3) {
      return obj.text;
    }
    if ("id" in obj && typeof obj.id === "string" && Object.keys(obj).length <= 2) {
      return obj.id;
    }
    return JSON.stringify(value);
  }
  return String(value);
}

/** The workflow-level preset target. The settings popover used to call it "default". */
const LEGACY_WORKFLOW_PRESET_TARGET = "default";

/**
 * Repair workflow params stored by older builds so a send is not refused.
 *
 *   - `thinking_level` at the top level (the Cmd+K H shortcut wrote it there)
 *     is not a workflow input; effort lives in `model.thinking_level`. It moves
 *     there when the model is already a selector without one, else is dropped.
 *   - `default.<param>` keys (the popover's preset picker wrote them under the
 *     wrong target) are workflow-level params. They fill `<param>` only when it
 *     is unset: the plain key is what the pill and the run were using.
 *
 * Group params ("Agent A.model") are left alone. Returns the input object
 * itself when nothing needed repair.
 */
export function repairLegacyWorkflowParams(
  params: Record<string, unknown>
): Record<string, unknown> {
  const legacyPrefix = `${LEGACY_WORKFLOW_PRESET_TARGET}.`;
  const needsRepair = Object.keys(params).some(
    (key) => key === "thinking_level" || key.startsWith(legacyPrefix)
  );
  if (!needsRepair) return params;

  const repaired: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(params)) {
    if (key === "thinking_level" || key.startsWith(legacyPrefix)) continue;
    repaired[key] = value;
  }
  for (const [key, value] of Object.entries(params)) {
    if (!key.startsWith(legacyPrefix)) continue;
    const name = key.slice(legacyPrefix.length);
    if (name && repaired[name] === undefined) repaired[name] = value;
  }

  const level = params.thinking_level;
  const model = repaired.model;
  if (typeof level === "string" && level && isPlainObject(model) && !model.thinking_level) {
    repaired.model = { ...model, thinking_level: level };
  }
  return repaired;
}

/** Preset selections stored under the legacy "default" target belong to the workflow ("" target). */
export function repairLegacyPresetSelections<T>(
  selections: Record<string, T>
): Record<string, T> {
  if (!(LEGACY_WORKFLOW_PRESET_TARGET in selections)) return selections;
  const { [LEGACY_WORKFLOW_PRESET_TARGET]: legacy, ...rest } = selections;
  return "" in rest ? rest : { ...rest, "": legacy };
}

/**
 * Set the reasoning effort on the params' model selector — the one place a
 * send accepts it. A bare-string model becomes `{id}`; with no model set there
 * is no selector to carry the effort, so the params come back unchanged.
 */
export function withThinkingLevel(
  params: Record<string, unknown>,
  level: string
): Record<string, unknown> {
  const model = params.model;
  if (typeof model === "string" && model) {
    return { ...params, model: { id: model, thinking_level: level } };
  }
  if (isPlainObject(model)) {
    return { ...params, model: { ...model, thinking_level: level } };
  }
  return params;
}

/** The effort currently set on the params' model selector, if any. */
export function currentThinkingLevel(params: Record<string, unknown>): string | undefined {
  const model = params.model;
  if (!isPlainObject(model)) return undefined;
  const level = model.thinking_level;
  return typeof level === "string" && level ? level : undefined;
}
