// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's UserService.GetCurrentUser: the person reading the demo
 * (shopStory.ts DEMO_READER), as an issue names them (membersApi.ts
 * getCurrentIssueUser). Nobody is signed in to the demo; this is the story's
 * reader, the one the issue pages show as "you".
 */

import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createShopApi } from "../model/shopApi";
import { DEMO_CAST } from "../shopStory";
import { completed } from "./defaults";

export function userHandlers(fixtures: ObservabilityApi = createShopApi()): MockHandlers {
  return completed(UserService, mockService(UserService, {
    getCurrentUser: async () => {
      const reader = await fixtures.getCurrentIssueUser();
      const email = DEMO_CAST.find((p) => p.id === reader?.id)?.email ?? "";
      return { user: reader ? { id: reader.id, name: reader.name, email } : undefined };
    },
  }));
}
