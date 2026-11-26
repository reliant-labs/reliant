// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's MlEarlyAccessService handlers: every rpc of the service,
 * answered on the WIRE from the ML fixture world (model/ml/). Each rpc asks
 * every domain (model/ml/index.ts ML_DOMAINS):
 *
 *   - an entity read takes the first domain's answer; an entity nobody knows
 *     is NotFound (a hand-edited URL), never a made-up row;
 *   - a subject-keyed read (model/ml/domain.ts MERGED_METHODS) merges every
 *     domain's answer, concatenating their lists;
 *   - a list read nobody answers is an empty list.
 *
 * Every one is a read the proto declares NO_SIDE_EFFECTS, so the transport's
 * read-only guard passes them as it is. A read naming another org or project
 * is answered empty (scope.ts).
 */

import { create, type DescMethod } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { MlEarlyAccessService } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { ML_DOMAINS, MERGED_METHODS, type MlDomain, type MlMethodName } from "../model/ml";
import { demoNow } from "../shopStory";
import { withMessageRequests } from "./defaults";
import { DEMO_SCOPE, scopedToDemo, type PackScope } from "./scope";

/** The RPCs served here: every method of MlEarlyAccessService, by its full name. */
export const ML_RPCS: Readonly<Record<MlMethodName, string>> = Object.fromEntries(
  MlEarlyAccessService.methods.map((m) => [m.localName, `${MlEarlyAccessService.typeName}/${m.name}`]),
) as Record<MlMethodName, string>;

/** Concatenates the domains' lists; for anything else the first answer wins. */
export function mergeAnswers(answers: readonly Record<string, unknown>[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const answer of answers) {
    for (const [key, value] of Object.entries(answer)) {
      if (Array.isArray(value)) out[key] = [...((out[key] as unknown[] | undefined) ?? []), ...value];
      else if (!(key in out) && value !== undefined) out[key] = value;
    }
  }
  return out;
}

/** An entity read: answering nothing is NotFound. Org-level reads (GetMlFleet, GetMlStack, GetMlQueues) and lists are not. */
const isEntityRead = (m: DescMethod) => m.name.startsWith("Get") && !["GetMlFleet", "GetMlStack", "GetMlQueues"].includes(m.name);

/** Answers `method` from `domains`. Exported for the world's tests. */
export function answerMl(method: DescMethod, req: unknown, now: () => number, domains: readonly MlDomain[] = ML_DOMAINS) {
  const name = method.localName as MlMethodName;
  const answers: Record<string, unknown>[] = [];
  for (const domain of domains) {
    const fn = domain[name] as ((r: unknown, n: () => number) => Record<string, unknown> | undefined) | undefined;
    const answer = fn?.(req, now);
    if (answer) {
      answers.push(answer);
      if (!MERGED_METHODS.has(name)) break;
    }
  }
  if (answers.length === 0 && isEntityRead(method)) throw new ConnectError(`${method.name}: no such entity in the demo`, Code.NotFound);
  return create(method.output, mergeAnswers(answers) as never);
}

export function mlHandlers(now: () => number = demoNow, scope: PackScope = DEMO_SCOPE): MockHandlers {
  const handlers: MockHandlers = {};
  for (const method of MlEarlyAccessService.methods) {
    handlers[`${MlEarlyAccessService.typeName}/${method.name}`] = (req: unknown) => answerMl(method, req, now);
  }
  return withMessageRequests(scopedToDemo(handlers, [MlEarlyAccessService], scope), [MlEarlyAccessService]);
}
