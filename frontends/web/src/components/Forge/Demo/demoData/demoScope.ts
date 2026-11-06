// Copyright (c) 2025 Reliant Labs

/**
 * The one app the demo has data for: project `shop`, environments
 * env-dev / env-staging / env-prod. Every answer the demo transport gives is
 * about it, and a read that names another project or environment gets an
 * EMPTY answer — never shop's data, and never the generated defaults' made-up
 * rows (scopedToDemo).
 *
 * What "names" means: a request field `project` / `project_id`, or one ending
 * `environment_id` / `env_id` (`env_ids` too), at the request's top level or
 * one message down (a `scope`). Empty means unscoped (the org's view, which in
 * the demo org is shop's).
 */

import { create, isMessage, type DescMessage, type DescMethod, type DescService, type Message } from "@bufbuild/protobuf";

import type { MockHandlers } from "./forgeMockValues/mock-values";

export const DEMO_PROJECT = "shop";

export const DEMO_ENVS = [
  { id: "env-dev", name: "dev" },
  { id: "env-staging", name: "staging" },
  { id: "env-prod", name: "prod" },
] as const;

export type DemoEnvId = (typeof DEMO_ENVS)[number]["id"];

const ENV_IDS: ReadonlySet<string> = new Set(DEMO_ENVS.map((e) => e.id));

/** Whether a request's `project` is the demo's (empty = unscoped). */
export const inDemoProject = (project: string | undefined): boolean => !project || project === DEMO_PROJECT;

/** The demo environment an id names, if any. */
export const demoEnvId = (id: string | undefined): DemoEnvId | undefined =>
  id && ENV_IDS.has(id) ? (id as DemoEnvId) : undefined;

const PROJECT_FIELD = /^project(_id)?$/;
const ENV_FIELD = /(^|_)(environment_id|env_id)$/;
const ENV_LIST_FIELD = /(^|_)(environment_ids|env_ids)$/;

/** Whether `req` names a project or environment that is not the demo's. */
export function outsideDemo(input: DescMessage, req: Message, depth = 0): boolean {
  const msg = req as unknown as Record<string, unknown>;
  for (const field of input.fields) {
    const value = field.oneof
      ? (msg[field.oneof.localName] as { case?: string; value?: unknown } | undefined)?.case === field.localName
        ? (msg[field.oneof.localName] as { value?: unknown }).value
        : undefined
      : msg[field.localName];
    if (value == null || value === "") continue;
    if (field.fieldKind === "scalar" && typeof value === "string") {
      if (PROJECT_FIELD.test(field.name) && !inDemoProject(value)) return true;
      if (ENV_FIELD.test(field.name) && !demoEnvId(value)) return true;
    } else if (field.fieldKind === "list" && field.listKind === "scalar" && ENV_LIST_FIELD.test(field.name)) {
      if ((value as unknown[]).some((id) => typeof id === "string" && id !== "" && !demoEnvId(id))) return true;
    } else if (field.fieldKind === "message" && depth < 1 && isMessage(value)) {
      if (outsideDemo(field.message, value, depth + 1)) return true;
    }
  }
  return false;
}

/**
 * `handlers` answering EMPTY for a read outside the demo's scope: an empty
 * response message (an empty stream for a streaming read), before the handler
 * — the story's or a generated default — is asked. Requests must already be
 * messages (withMessageRequests, outside this).
 */
export function scopedToDemo(handlers: MockHandlers, services: readonly DescService[]): MockHandlers {
  const methods = new Map<string, DescMethod>(services.flatMap((s) => s.methods.map((m): [string, DescMethod] => [`${s.typeName}/${m.name}`, m])));
  const out: MockHandlers = {};
  for (const [key, handler] of Object.entries(handlers)) {
    const method = methods.get(key);
    out[key] = !method
      ? handler
      : (req: unknown) => {
          // A request stream is scoped by its last message, as the engine's defaults read it.
          const last = Array.isArray(req) ? req[req.length - 1] : req;
          if (isMessage(last) && outsideDemo(method.input, last)) {
            return method.methodKind === "server_streaming" || method.methodKind === "bidi_streaming" ? [] : create(method.output);
          }
          return handler(req);
        };
  }
  return out;
}
