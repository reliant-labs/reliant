// Copyright (c) 2025 Reliant Labs

/**
 * Convert flat dot-notation params to nested structure.
 * 
 * Example:
 *   Input:  { "agent.model": "claude", "agent.temperature": 0.7, "mode": "fast" }
 *   Output: { "agent": { "model": "claude", "temperature": 0.7 }, "mode": "fast" }
 * 
 * This is needed because the frontend stores params with flat keys like "group.param"
 * but the backend expects nested structure for group inputs.
 */
export function flatToNestedParams(
  flatParams: Record<string, unknown>
): Record<string, unknown> {
  const nested: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(flatParams)) {
    const dotIndex = key.indexOf(".");
    
    if (dotIndex > 0) {
      // Key has dot notation - nest it
      const groupName = key.substring(0, dotIndex);
      const paramName = key.substring(dotIndex + 1);
      
      // Initialize group object if needed
      if (!nested[groupName] || typeof nested[groupName] !== "object") {
        nested[groupName] = {};
      }
      
      (nested[groupName] as Record<string, unknown>)[paramName] = value;
    } else {
      // Top-level param - keep as-is
      nested[key] = value;
    }
  }

  return nested;
}

/**
 * Convert nested params to flat dot-notation, the inverse of flatToNestedParams.
 *
 * Example:
 *   Input:  { "agent": { "model": "claude" }, "mode": "fast" }
 *   Output: { "agent.model": "claude", "mode": "fast" }
 *
 * The backend returns group inputs nested; the params panel keys them flat.
 * Only one level is flattened, matching flatToNestedParams — a param whose
 * value is itself an object (a model selector, say) stays intact.
 */
export function nestedToFlatParams(
  nestedParams: Record<string, unknown>
): Record<string, unknown> {
  const flat: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(nestedParams)) {
    if (isPlainObject(value)) {
      for (const [nestedKey, nestedValue] of Object.entries(value)) {
        flat[`${key}.${nestedKey}`] = nestedValue;
      }
    } else {
      flat[key] = value;
    }
  }

  return flat;
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
 * Reconcile locally displayed params against the values a running workflow
 * actually holds.
 *
 * `current` is what the UI shows, `sent` is what was just pushed to the server,
 * and `server` is what the workflow reports afterwards. A key adopts the server
 * value unless the user has edited it since the push — their newer edit wins and
 * the next sync will carry it.
 *
 * Only keys already present in `current` are considered; a workflow's inputs
 * also carry runtime keys the params panel never displays.
 */
export function reconcileParamsWithServer(
  current: Record<string, unknown>,
  sent: Record<string, unknown>,
  server: Record<string, unknown>
): { params: Record<string, unknown>; changed: boolean } {
  const reconciled = { ...current };
  let changed = false;

  for (const [key, serverValue] of Object.entries(server)) {
    if (!Object.hasOwn(current, key)) continue;
    if (!paramValuesEqual(current[key], sent[key])) continue;
    if (paramValuesEqual(current[key], serverValue)) continue;

    reconciled[key] = serverValue;
    changed = true;
  }

  return { params: reconciled, changed };
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
