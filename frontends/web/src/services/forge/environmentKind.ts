// Copyright (c) 2026 Reliant Labs

/**
 * What kind of environment a control-plane DeployEnvironment is, read the one
 * way both the env list (cloudEnvs.ts) and Live (live.ts) read it.
 *
 * control-plane#3095 removed `DeployEnvironment.kind` (tag 3 is reserved).
 * Since control-plane#1349 every environment it returns carries the three
 * fields that replace it: `lifetime`, `secret_posture` and `hosted_slot`. The
 * kind is derived from them, the inverse of the server's own mapping
 * (environmentToProto):
 *
 *   secret_posture READABLE           → local
 *   lifetime PREVIEW                  → preview
 *   hosted_slot                       → persistent
 *   otherwise (write-only, no slot)   → self_managed
 *
 * When neither posture nor lifetime is set the kind is `unknown`, so a row the
 * client cannot place is never offered cloud language.
 *
 * The derivation is lossy in one place: a self-managed environment with a
 * hosted part holds a hosted slot, so it reads as persistent.
 */

import {
  DeployEnvironmentLifetime,
  DeploySecretPosture,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";

export type EnvironmentKind = "persistent" | "preview" | "self_managed" | "local" | "unknown";

/** The fields the kind is read from. */
export interface EnvironmentKindFields {
  lifetime: DeployEnvironmentLifetime;
  secretPosture: DeploySecretPosture;
  hostedSlot: boolean;
}

export function environmentKindOf(env: EnvironmentKindFields): EnvironmentKind {
  if (env.secretPosture === DeploySecretPosture.UNSPECIFIED && env.lifetime === DeployEnvironmentLifetime.UNSPECIFIED) {
    return "unknown";
  }
  if (env.secretPosture === DeploySecretPosture.READABLE) return "local";
  if (env.lifetime === DeployEnvironmentLifetime.PREVIEW) return "preview";
  if (env.hostedSlot) return "persistent";
  return "self_managed";
}
