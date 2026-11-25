// Copyright (c) 2025 Reliant Labs

/**
 * Which RPCs a fixture transport may answer: the reads.
 *
 * Interim. Once every read rpc reliant's web app calls declares
 * `option idempotency_level = NO_SIDE_EFFECTS;`, the transports pass
 * `readOnly: true` (forge's mock transport then reads the proto alone) and
 * this file is deleted with its name heuristic.
 */

import type { DescMethod } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";

/**
 * Read-only verbs, for the methods whose proto declares no idempotency (most
 * of control-plane's today): reads that list, fetch, search, query, compare,
 * watch or stream, or that describe, preview or plan a change without making
 * it. Every other name is a write.
 */
const READ_VERB = /^(List|Get|Search|Query|Compare|Watch|Stream|Describe|Browse|Preview|Plan|Check|Resolve)/;

/**
 * The reads the demo may make. The proto's own word wins: a method declared
 * `idempotency_level = NO_SIDE_EFFECTS` is a read, and one declared
 * IDEMPOTENT (a set, a delete) is a write whatever its name. Otherwise the
 * verb decides (READ_VERB).
 */
export function isDemoRead(method: Pick<DescMethod, "name"> & Partial<Pick<DescMethod, "idempotency">>): boolean {
  if (method.idempotency === MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS) return true;
  if (method.idempotency === MethodOptions_IdempotencyLevel.IDEMPOTENT) return false;
  return READ_VERB.test(method.name);
}
