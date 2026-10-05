// Copyright (c) 2025 Reliant Labs

/**
 * Typed access to an integration `action` node's args (ActionArgs in
 * workflow_v2.proto):
 *
 *   - id: open_issue
 *     type: action
 *     uses: github/issue.create@1     # CelString
 *     with: { owner: acme, title: "{{ trigger.payload.data.issue.title }}" }
 *     connection: conn_123            # CelString; omitted = the owner's default
 *
 * `with` is `map<string, google.protobuf.Value>`, so a parameter is stored
 * as a proto Value and read back as plain JS. A template stays a string Value
 * (`"{{ … }}"`); the runtime resolves it before the activity runs. Everything
 * goes through the builder's step model, so the YAML the server writes from it
 * is the same canonical YAML the agent tools produce.
 */

import type { Step } from "../types/workflow";
import { celString, normalizeCelString } from "./celAdapter";
import { jsToProtoValue, protoValueToJs } from "../api/proto-utils";

export const ACTION_NODE_TYPE = "action";

interface ActionArgsShape {
  uses?: unknown;
  with?: Record<string, unknown>;
  connection?: unknown;
}

export function isIntegrationActionStep(step: Step | undefined): boolean {
  return step?.type === ACTION_NODE_TYPE;
}

function actionArgs(step: Step): ActionArgsShape {
  if (step.args?.case !== "action" || !step.args.value) return {};
  return step.args.value as ActionArgsShape;
}

function withActionArgsValue(step: Step, next: ActionArgsShape): Step {
  return { ...step, args: { case: "action", value: next } } as Step;
}

/** The ref the node runs, e.g. "github/issue.create@1". Empty when unset. */
export function getActionUses(step: Step): string {
  return normalizeCelString(actionArgs(step).uses, "");
}

/** The connection id the node names; empty means "the owner's default". */
export function getActionConnection(step: Step): string {
  return normalizeCelString(actionArgs(step).connection, "");
}

/** Every `with:` parameter as plain JS. */
export function getActionParams(step: Step): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(actionArgs(step).with ?? {})) {
    out[key] = isProtoValue(value) ? protoValueToJs(value as Parameters<typeof protoValueToJs>[0]) : value;
  }
  return out;
}

function isProtoValue(value: unknown): boolean {
  return typeof value === "object" && value !== null && "kind" in (value as Record<string, unknown>);
}

/** Set one `with:` parameter; undefined or "" removes it. */
export function withActionParam(step: Step, name: string, value: unknown): Step {
  const args = actionArgs(step);
  const params = { ...(args.with ?? {}) };
  if (value === undefined || value === "" || value === null) delete params[name];
  else params[name] = jsToProtoValue(value);
  return withActionArgsValue(step, { ...args, with: params });
}

/** Set (or with "" clear) the connection, so the owner's default applies. */
export function withActionConnection(step: Step, connectionId: string): Step {
  const args = { ...actionArgs(step) };
  if (connectionId) args.connection = celString(connectionId);
  else delete args.connection;
  return withActionArgsValue(step, args);
}

/**
 * A new action node for `ref`, with its parameters' declared defaults filled
 * in, so choosing "Create issue" in the palette yields a node that validates
 * as far as its defaults allow.
 */
export function newActionStep(id: string, ref: string, defaults: Record<string, unknown> = {}): Step {
  const params: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(defaults)) {
    if (value !== undefined) params[key] = jsToProtoValue(value);
  }
  return {
    id,
    type: ACTION_NODE_TYPE,
    args: { case: "action", value: { uses: celString(ref), with: params } },
  } as Step;
}

/** A CEL-safe node id from a ref: "github/issue.create@1" → "issue_create". */
export function actionNodeIdBase(ref: string): string {
  const id = ref.split("/")[1]?.split("@")[0] ?? "action";
  const base = id.replace(/[^a-zA-Z0-9]+/g, "_").replace(/^_+|_+$/g, "").toLowerCase();
  return /^[a-z]/.test(base) ? base : `action_${base}`;
}

/** The first `base`, `base_2`, … not in `taken`. */
export function uniqueNodeId(base: string, taken: Iterable<string>): string {
  const used = new Set(taken);
  if (!used.has(base)) return base;
  for (let n = 2; ; n += 1) {
    const candidate = `${base}_${n}`;
    if (!used.has(candidate)) return candidate;
  }
}
