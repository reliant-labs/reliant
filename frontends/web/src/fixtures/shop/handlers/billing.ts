// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's ObservabilityBillingService: the demo has no billing, so
 * every read answers Unimplemented, as a control plane without the billing API
 * would, and the usage panels show their "unavailable" state rather than the
 * generated defaults' made-up figures (handlers/defaults.ts would answer
 * otherwise). Writes never get here: the read-only guard refuses them first.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { ObservabilityBillingService } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";

const notInDemo = () => {
  throw new ConnectError("The demo has no observability billing.", Code.Unimplemented);
};

export function billingHandlers(): MockHandlers {
  return Object.fromEntries(ObservabilityBillingService.methods.map((m) => [`${ObservabilityBillingService.typeName}/${m.name}`, notInDemo]));
}
