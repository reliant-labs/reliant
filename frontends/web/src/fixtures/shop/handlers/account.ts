// Copyright (c) 2025 Reliant Labs

/**
 * UserService for the signed-in app: who is signed in (workspace.ts
 * APP_USER, past onboarding). The org Alex acts in is organization.ts's
 * (OrganizationService/ListMyOrganizations, in the demo pack already); its
 * roster (ListMembers, answered here, never in the demo pack) is Alex and
 * the team that builds shop (DEMO_CAST).
 *
 * Part of the app layer only (../app.ts).
 */
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

import { DAY, DEMO_CAST } from "../shopStory";
import { APP_USER, tsAgo } from "../workspace";
import { serve } from "./serve";

const titleCase = (handle: string) => handle[0].toUpperCase() + handle.slice(1);

export function accountScenarioHandlers() {
  return {
    ...serve(OrganizationService, {
      listMembers: () => {
        const member = (userId: string, email: string, name: string, role: string, joinedAgo: number, isFounder = false) => ({
          userId, email, name, avatarUrl: "", role, joinedAt: tsAgo(joinedAgo), isFounder, externalId: userId,
        });
        // jon founded Acme; Alex joined 20 days ago, as an admin (organization.ts).
        const team = DEMO_CAST.map((p, i) => member(p.id, p.email, titleCase(p.handle), p.role, (90 - 20 * i) * DAY, i === 0));
        const alex = member(APP_USER.id, APP_USER.email, APP_USER.name, "admin", 20 * DAY);
        const members = [...team, alex];
        return { members, nextPageToken: "", seats: { members: members.length, pendingInvitations: 0, limit: 10 } };
      },
    }),
    ...serve(UserService, {
      getCurrentUser: () => ({
        user: {
          id: APP_USER.id, externalId: APP_USER.id, email: APP_USER.email, name: APP_USER.name, avatarUrl: "", role: "user",
          createdAt: tsAgo(20 * DAY), updatedAt: tsAgo(DAY), onboardingCompleted: true,
        },
      }),
    }),
  };
}
