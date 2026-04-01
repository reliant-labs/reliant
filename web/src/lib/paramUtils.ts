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
