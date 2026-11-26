// Copyright (c) 2026 Reliant Labs

/**
 * The shape every ML domain file answers in. A domain (fleet.ts,
 * training.ts, ...) implements the MlEarlyAccessService rpcs it owns, by the
 * generated client's method names, returning the response as an init shape
 * (`create(ResponseSchema, …)` input), or undefined when the request is not
 * about its entities (another subject, an unknown id).
 *
 * handlers/ml.ts asks every domain. For an entity read (GetMlRun,
 * GetMlNode, …) the first answer wins and none is NotFound. For a
 * SUBJECT-KEYED read (QueryMlSeries, ListMlEvents, ListMlLogs, ListMlTraces,
 * ListMlProfiles, GetMlLineage, QueryMlHistograms, ListMlAlerts) every
 * domain's answer is merged: their lists are concatenated, so the fleet adds
 * the device events of a run's nodes to the run's own training events.
 */

import type { DescMessage, MessageInitShape, MessageShape } from "@bufbuild/protobuf";

import type { MlEarlyAccessService } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

type Methods = (typeof MlEarlyAccessService)["method"];
export type MlMethodName = keyof Methods;

type InputOf<M extends MlMethodName> = Methods[M]["input"] extends DescMessage ? MessageShape<Methods[M]["input"]> : never;
type OutputOf<M extends MlMethodName> = Methods[M]["output"] extends DescMessage ? MessageInitShape<Methods[M]["output"]> : never;

export type MlAnswer<M extends MlMethodName> = (req: InputOf<M>, now: () => number) => OutputOf<M> | undefined;

/** A domain's answers: any subset of the rpcs. */
export type MlDomain = { [M in MlMethodName]?: MlAnswer<M> };

/** The rpcs whose answers are merged across domains (lists concatenated). */
export const MERGED_METHODS: ReadonlySet<MlMethodName> = new Set<MlMethodName>([
  "queryMlSeries",
  "listMlEvents",
  "listMlLogs",
  "listMlTraces",
  "listMlProfiles",
  "getMlLineage",
  "queryMlHistograms",
  "listMlAlerts",
]);
