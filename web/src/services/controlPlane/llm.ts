/**
 * Cloud-only wrappers around `controlplane.v1.LLMGatewayService`. Onboarding
 * uses `createLLMKey` to provision a managed key for new cloud users.
 */

import { LLMGatewayService } from "@/gen/controlplane/v1/public/llm_gateway_service_pb";
import { getControlPlaneClient } from "./client";

export interface CreateLLMKeyArgs {
  name: string;
  models: string[];
  orgId?: string;
}

export async function createLLMKey(args: CreateLLMKeyArgs): Promise<void> {
  await getControlPlaneClient(LLMGatewayService).createLLMKey({
    name: args.name,
    models: args.models,
    orgId: args.orgId ?? "",
  });
}
