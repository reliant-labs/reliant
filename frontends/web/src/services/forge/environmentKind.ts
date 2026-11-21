// Copyright (c) 2026 Reliant Labs

/**
 * What kind of environment a control-plane DeployEnvironment is, read the one
 * way both the env list (cloudEnvs.ts) and Live (live.ts) read it.
 *
 * `DeployEnvironment.kind` is deprecated on the control plane and is going
 * away (control-plane#3095 reserves its tag). Since control-plane#1349 every
 * environment it returns also carries the three fields that replace it:
 * `lifetime`, `secret_posture` and `hosted_slot`. So:
 *
 *   - a recognised `kind` wins, so the answer against a server that still
 *     sends it is exactly what it always was;
 *   - otherwise the kind is derived from the replacement fields, the inverse
 *     of the server's own mapping (environmentToProto):
 *
 *       secret_posture READABLE           → local
 *       lifetime PREVIEW                  → preview
 *       hosted_slot                       → persistent
 *       otherwise (write-only, no slot)   → self_managed
 *
 *   - and when neither `kind` nor the replacement fields are set (a server
 *     older than both) it is `unknown`, as an unrecognised kind always was.
 *
 * The derivation is lossy in one place: a self-managed environment with a
 * hosted part holds a hosted slot, so it reads as persistent.
 */

import {
  DeployEnvironmentKind,
  DeployEnvironmentLifetime,
  DeploySecretPosture,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";

export type EnvironmentKind = "persistent" | "preview" | "self_managed" | "local" | "unknown";

/**
 * The fields the kind is read from. `kind` is optional so this keeps
 * type-checking once the re-vendored DeployEnvironment no longer has it.
 */
export interface EnvironmentKindFields {
  kind?: DeployEnvironmentKind;
  lifetime: DeployEnvironmentLifetime;
  secretPosture: DeploySecretPosture;
  hostedSlot: boolean;
}

function fromLegacyKind(kind: DeployEnvironmentKind | undefined): EnvironmentKind {
  switch (kind) {
    case DeployEnvironmentKind.PERSISTENT:
      return "persistent";
    case DeployEnvironmentKind.PREVIEW:
      return "preview";
    case DeployEnvironmentKind.SELF_MANAGED:
      return "self_managed";
    case DeployEnvironmentKind.LOCAL:
      return "local";
    default:
      return "unknown";
  }
}

export function environmentKindOf(env: EnvironmentKindFields): EnvironmentKind {
  const legacy = fromLegacyKind(env.kind);
  if (legacy !== "unknown") return legacy;
  if (env.secretPosture === DeploySecretPosture.UNSPECIFIED && env.lifetime === DeployEnvironmentLifetime.UNSPECIFIED) {
    return "unknown";
  }
  if (env.secretPosture === DeploySecretPosture.READABLE) return "local";
  if (env.lifetime === DeployEnvironmentLifetime.PREVIEW) return "preview";
  if (env.hostedSlot) return "persistent";
  return "self_managed";
}
