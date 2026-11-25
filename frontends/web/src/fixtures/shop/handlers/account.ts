// Copyright (c) 2025 Reliant Labs

/**
 * UserService for the signed-in app: who is signed in (workspace.ts
 * APP_USER, past onboarding). The org Alex acts in is organization.ts's
 * (OrganizationService/ListMyOrganizations, in the demo pack already).
 *
 * Part of the app layer only (../app.ts).
 */
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

import { DAY } from "../shopStory";
import { APP_USER, tsAgo } from "../workspace";
import { serve } from "./serve";

export function accountScenarioHandlers() {
  return serve(UserService, {
    getCurrentUser: () => ({
      user: {
        id: APP_USER.id, externalId: APP_USER.id, email: APP_USER.email, name: APP_USER.name, avatarUrl: "", role: "user",
        createdAt: tsAgo(20 * DAY), updatedAt: tsAgo(DAY), onboardingCompleted: true,
      },
    }),
  });
}
