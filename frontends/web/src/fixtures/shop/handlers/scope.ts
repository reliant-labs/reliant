// Copyright (c) 2025 Reliant Labs

/**
 * The one app the demo has data for: project `shop`, environments
 * env-dev / env-staging / env-prod. Every answer the demo transport gives is
 * about it, and a read that names another org, project or environment gets an
 * EMPTY answer — never shop's data, and never the generated defaults' made-up
 * rows (scopedToDemo).
 *
 * What "names" means: a string field, or a repeated one, whose name says it
 * selects an org (`org_id`, plural `org_ids`, including prefixed fields),
 * which must be shopStory's DEMO_ORG, or a project (`project`, `project_id`, `project_name`, `project_slug`,
 * or any of those after a prefix: `parent_project_id`), an environment by id
 * (ending `environment_id` / `env_id`, plural `_ids`) or an environment by
 * name (ending `environment` / `environment_name` / `env` / `env_name`,
 * plural too: `from_environment`, `env_names`) — at any depth of the request:
 * nested messages, repeated messages and message-valued maps are all walked,
 * and set oneof cases with them. Empty means unscoped (the org's view, which
 * in the demo org is shop's). A plural PROJECT selector (`project_ids`,
 * `projects`) is a union, so it is outside only when it names none of the
 * pack's (foreignList).
 */

import { create, isMessage, type DescMessage, type DescMethod, type DescService, type Message } from "@bufbuild/protobuf";

import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { DEMO_ORG } from "../shopStory";

export const DEMO_PROJECT = "shop";

export const DEMO_ENVS = [
  { id: "env-dev", name: "dev" },
  { id: "env-staging", name: "staging" },
  { id: "env-prod", name: "prod" },
] as const;

export type DemoEnvId = (typeof DEMO_ENVS)[number]["id"];

const ENV_IDS: ReadonlySet<string> = new Set(DEMO_ENVS.map((e) => e.id));

/**
 * Which app, and which org, the pack answers for: the public demo's `shop`
 * in shopStory's DEMO_ORG by default (DEMO_SCOPE). The forge tour's sample
 * tells the same story for its own name (`sample_shop`) and in whichever org
 * the person is in (`org: null`): it is nobody's app, so no org is foreign to it.
 */
export interface PackScope {
  /** The app (forge project) the story is about. */
  readonly app: string;
  /** The org a read must name, or null for any. */
  readonly org: string | null;
}

export const DEMO_SCOPE: PackScope = { app: DEMO_PROJECT, org: DEMO_ORG };

/** Whether a request's `project` is the pack's app (empty = unscoped). */
export const inProject = (scope: PackScope, project: string | undefined): boolean => !project || project === scope.app;

/** Whether a request's `project` is the demo's (empty = unscoped). */
export const inDemoProject = (project: string | undefined): boolean => inProject(DEMO_SCOPE, project);

/** The demo environment an id names, if any. */
export const demoEnvId = (id: string | undefined): DemoEnvId | undefined =>
  id && ENV_IDS.has(id) ? (id as DemoEnvId) : undefined;

/** The demo environment a name names (`prod`), or an id does (`env-prod`). */
const demoEnvName = (name: string): boolean => DEMO_ENVS.some((e) => e.name === name || e.id === name);

const PROJECT_FIELD = /(^|_)project(_id|_name|_slug)?s?$/;
const ENV_ID_FIELD = /(^|_)(environment|env)_ids?$/;
const ENV_NAME_FIELD = /(^|_)(environment|env)(_name)?s?$/;

/** Whether one selector value is outside the demo, judged by its field's name; false for a field that is no selector. */
function foreignSelector(fieldName: string, value: string, scope: PackScope): boolean {
  if (value === "") return false;
  if (/(^|_)org_ids?$/.test(fieldName)) return scope.org !== null && value !== scope.org;
  if (PROJECT_FIELD.test(fieldName)) return !inProject(scope, value);
  if (ENV_ID_FIELD.test(fieldName)) return !demoEnvId(value);
  if (ENV_NAME_FIELD.test(fieldName)) return !demoEnvName(value);
  return false;
}

/**
 * Whether a repeated selector is outside the demo. A list of PROJECTS is a
 * union (QueryScope.project_ids, GetTestPortfolioRequest.projects: the
 * environments or rows of each), so it is outside only when none of its
 * projects is the pack's: the other projects add nothing, as a project the
 * org does not have adds nothing on the server. Every other list narrows
 * (env_ids AND), so any foreign entry makes it outside.
 */
function foreignList(fieldName: string, values: readonly unknown[], scope: PackScope): boolean {
  const strings = values.filter((v): v is string => typeof v === "string" && v !== "");
  if (PROJECT_FIELD.test(fieldName)) return strings.length > 0 && strings.every((v) => foreignSelector(fieldName, v, scope));
  return strings.some((v) => foreignSelector(fieldName, v, scope));
}

/**
 * How deep a request is walked. Every console request is far shallower; the
 * cap only stops a self-referencing message type from recursing for ever.
 */
const MAX_DEPTH = 16;

/** Whether `req` names an org, project or environment that is not the pack's (`scope`), anywhere in it. */
export function outsideDemo(input: DescMessage, req: Message, scope: PackScope = DEMO_SCOPE, depth = 0): boolean {
  if (depth > MAX_DEPTH) return false;
  const msg = req as unknown as Record<string, unknown>;
  const nested = (desc: DescMessage | undefined, value: unknown): boolean => !!desc && isMessage(value) && outsideDemo(desc, value, scope, depth + 1);
  for (const field of input.fields) {
    const value = field.oneof
      ? (msg[field.oneof.localName] as { case?: string; value?: unknown } | undefined)?.case === field.localName
        ? (msg[field.oneof.localName] as { value?: unknown }).value
        : undefined
      : msg[field.localName];
    if (value == null) continue;
    switch (field.fieldKind) {
      case "scalar":
        if (typeof value === "string" && foreignSelector(field.name, value, scope)) return true;
        break;
      case "message":
        if (nested(field.message, value)) return true;
        break;
      case "list":
        if (field.listKind === "scalar" && foreignList(field.name, value as unknown[], scope)) return true;
        if (field.listKind === "message" && (value as unknown[]).some((v) => nested(field.message, v))) return true;
        break;
      case "map":
        if (field.mapKind === "message" && Object.values(value as Record<string, unknown>).some((v) => nested(field.message, v))) return true;
        break;
    }
  }
  return false;
}

/**
 * `handlers` answering EMPTY for a read outside the pack's `scope`: an empty
 * response message (an empty stream for a streaming read), before the handler
 * — the story's or a generated default — is asked. Requests must already be
 * messages (withMessageRequests, outside this).
 */
export function scopedToDemo(handlers: MockHandlers, services: readonly DescService[], scope: PackScope = DEMO_SCOPE): MockHandlers {
  const methods = new Map<string, DescMethod>(services.flatMap((s) => s.methods.map((m): [string, DescMethod] => [`${s.typeName}/${m.name}`, m])));
  const out: MockHandlers = {};
  for (const [key, handler] of Object.entries(handlers)) {
    const method = methods.get(key);
    out[key] = !method
      ? handler
      : (req: unknown) => {
          // A request stream is scoped by its last message, as the engine's defaults read it.
          const last = Array.isArray(req) ? req[req.length - 1] : req;
          if (isMessage(last) && outsideDemo(method.input, last, scope)) {
            return method.methodKind === "server_streaming" || method.methodKind === "bidi_streaming" ? [] : create(method.output);
          }
          return handler(req);
        };
  }
  return out;
}
