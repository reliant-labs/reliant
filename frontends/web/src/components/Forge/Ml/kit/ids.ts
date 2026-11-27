// Copyright (c) 2026 Reliant Labs

/**
 * Composite subject ids, as the contract defines them (ml_early_access.proto
 * "SUBJECT IDS"): built and split in one place.
 */

export const phaseId = (runId: string, phase: string) => `${runId}/${phase}`;
export const attemptId = (runId: string, phase: string, n: number) => `${runId}/${phase}/${n}`;
export const rankId = (runId: string, phase: string, n: number, rank: number) => `${runId}/${phase}/${n}/${rank}`;
export const checkpointId = (runId: string, phase: string, step: number | bigint) => `${runId}/${phase}/${step}`;
export const nodeId = (clusterId: string, node: string) => `${clusterId}/${node}`;
export const poolId = (clusterId: string, pool: string) => `${clusterId}/${pool}`;
export const versionId = (model: string, version: string) => `${model}/${version}`;
export const revisionId = (service: string, release: string) => `${service}/${release}`;

/** The last segment of a composite id: a node's name, a version's tag. */
export const tail = (id: string) => id.split("/").pop() ?? id;
