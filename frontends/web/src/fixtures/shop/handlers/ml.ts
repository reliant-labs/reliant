// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's MlEarlyAccessService handlers: the six reads the console's
 * ML pages make (Machines, Stack, Recommendations, Runs, Models, Inference),
 * answered on the WIRE from model/ml.ts. The pack composes them with the
 * other services' handlers (../index.ts shopScenario).
 *
 * Every one is a read the proto declares NO_SIDE_EFFECTS, so the transport's
 * read-only guard passes them as it is. A read naming another org or project
 * is answered empty (scope.ts); `cluster_id` narrows to one cluster.
 */

import { create } from "@bufbuild/protobuf";

import {
  GetMlMachinesResponseSchema,
  GetMlStackResponseSchema,
  ListMlFindingsResponseSchema,
  ListMlInferenceServicesResponseSchema,
  ListMlModelsResponseSchema,
  ListMlRunsResponseSchema,
  MlEarlyAccessService,
  type GetMlMachinesRequest,
  type GetMlStackRequest,
  type ListMlFindingsRequest,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { shopMlAnswers } from "../model/ml";
import { demoNow } from "../shopStory";
import { withMessageRequests } from "./defaults";
import { DEMO_SCOPE, scopedToDemo, type PackScope } from "./scope";

const rpc = (method: string) => `${MlEarlyAccessService.typeName}/${method}`;

/** The RPCs served here: every read of MlEarlyAccessService. */
export const ML_RPCS = {
  getMlMachines: rpc("GetMlMachines"),
  getMlStack: rpc("GetMlStack"),
  listMlFindings: rpc("ListMlFindings"),
  listMlRuns: rpc("ListMlRuns"),
  listMlModels: rpc("ListMlModels"),
  listMlInferenceServices: rpc("ListMlInferenceServices"),
} as const;

export function mlHandlers(now: () => number = demoNow, scope: PackScope = DEMO_SCOPE): MockHandlers {
  const answers = shopMlAnswers(scope.app, now);
  const handlers: MockHandlers = {
    [ML_RPCS.getMlMachines]: (req: GetMlMachinesRequest) => create(GetMlMachinesResponseSchema, answers.machines(req.clusterId || undefined)),
    [ML_RPCS.getMlStack]: (req: GetMlStackRequest) => create(GetMlStackResponseSchema, answers.stack(req.clusterId || undefined)),
    [ML_RPCS.listMlFindings]: (req: ListMlFindingsRequest) => create(ListMlFindingsResponseSchema, answers.findings(req.clusterId || undefined)),
    // `project` is a scope selector: another app's is answered empty before these run, and every answer is the pack's app.
    [ML_RPCS.listMlRuns]: () => create(ListMlRunsResponseSchema, answers.runs()),
    [ML_RPCS.listMlModels]: () => create(ListMlModelsResponseSchema, answers.models()),
    [ML_RPCS.listMlInferenceServices]: () => create(ListMlInferenceServicesResponseSchema, answers.inference()),
  };
  return withMessageRequests(scopedToDemo(handlers, [MlEarlyAccessService], scope), [MlEarlyAccessService]);
}
