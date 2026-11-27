// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's ChangesService handlers: every read the Experiments pages
 * make (the list, a change, its history, its results, whether it reached the
 * environment's apps), answered on the WIRE as control-plane would answer
 * them. The pack composes them with the other services' handlers (../index.ts
 * shopScenario).
 *
 * The data is shop's story (model/changes.ts). The demo runs the pages' REAL
 * client (hooks/changes-queries.ts over the generated hooks, and
 * services/controlPlane/changes/changesWire.ts) over the fixture transport.
 * Writes are not served here: the pack's read-only transport refuses them.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  ChangesService,
  GetChangeResponseSchema,
  ListChangeEventsResponseSchema,
  ListChangesResponseSchema,
  type GetChangeReadoutRequest,
  type GetChangeRequest,
  type GetChangesDeliveryStatusRequest,
  type ListChangeEventsRequest,
  type ListChangesRequest,
} from "@/gen/controlplane/services/changes/v1/changes_pb";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { shopChangesAnswers } from "../model/changes";
import { DEMO_ORG, demoNow } from "../shopStory";
import { withMessageRequests } from "./defaults";
import { DEMO_SCOPE, scopedToDemo, type PackScope } from "./scope";

const rpc = (method: string) => `${ChangesService.typeName}/${method}`;

/** The RPCs served here: every read the Experiments pages make (hooks/changes-queries.ts). */
export const CHANGES_RPCS = {
  listChanges: rpc("ListChanges"),
  getChange: rpc("GetChange"),
  listChangeEvents: rpc("ListChangeEvents"),
  getChangeReadout: rpc("GetChangeReadout"),
  getChangesDeliveryStatus: rpc("GetChangesDeliveryStatus"),
} as const;

/** What the server answers for an id that names no change of the org. */
const notFound = () => new ConnectError("change not found", Code.NotFound);

/**
 * ChangesService's reads over shop's story as of `now` (the pack clock), with
 * requests normalized to messages and reads outside shop answered empty
 * (scope.ts).
 */
export function changesHandlers(now: () => number = demoNow, scope: PackScope = DEMO_SCOPE): MockHandlers {
  // One model per transport: its answers are fixed by the pack clock, which never moves.
  let model: ReturnType<typeof shopChangesAnswers> | null = null;
  const answers = () => (model ??= shopChangesAnswers(now(), scope.org ?? DEMO_ORG));
  const handlers: MockHandlers = {
    [CHANGES_RPCS.listChanges]: (req: ListChangesRequest) => create(ListChangesResponseSchema, { changes: answers().listChanges(req.environmentId || undefined) }),
    [CHANGES_RPCS.getChange]: (req: GetChangeRequest) => {
      const change = answers().getChange(req.changeId);
      if (!change) throw notFound();
      return create(GetChangeResponseSchema, { change });
    },
    [CHANGES_RPCS.listChangeEvents]: (req: ListChangeEventsRequest) => {
      if (!answers().getChange(req.changeId)) throw notFound();
      return create(ListChangeEventsResponseSchema, { events: answers().listChangeEvents(req.changeId) });
    },
    [CHANGES_RPCS.getChangeReadout]: (req: GetChangeReadoutRequest) => {
      if (!answers().getChange(req.changeId)) throw notFound();
      const readout = answers().getChangeReadout(req.changeId);
      // A remote config or a rollout that never ran an experiment: the server's refusal, by its reason.
      if (!readout) throw new ConnectError("this change has never run an experiment", Code.FailedPrecondition, { "x-forge-error-reason": "no_experiment_run" });
      return readout;
    },
    [CHANGES_RPCS.getChangesDeliveryStatus]: (req: GetChangesDeliveryStatusRequest) => answers().getChangesDeliveryStatus(req.environmentId),
  };
  return withMessageRequests(scopedToDemo(handlers, [ChangesService], scope), [ChangesService]);
}
