// Copyright (c) 2025 Reliant Labs

/**
 * services/controlPlane/onboarding/cloud.ts in the forge demo's bundle (a
 * demo twin, vite-plugin-demo-bundle.ts). The real module reads the current
 * user from the login broker's session when forge sign-in is on (lib/auth),
 * and provisions a managed LLM key for a new account. The demo has no
 * sign-in: the current user is whatever the demo's transport answers for
 * UserService/GetCurrentUser (the shop sample data's visitor), completing
 * onboarding is a write the demo's transport refuses, and there is no account
 * to provision a key for.
 */

import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

import { getControlPlaneClient } from "../client";
import type * as Real from "./cloud";

export const getCurrentUser: typeof Real.getCurrentUser = async () => {
  const res = await getControlPlaneClient(UserService).getCurrentUser({});
  if (!res.user) return null;
  return {
    onboardingCompleted: res.user.onboardingCompleted,
    id: res.user.id,
    email: res.user.email,
    name: res.user.name,
    createdAtMs: res.user.createdAt ? Number(res.user.createdAt.seconds) * 1000 : undefined,
  };
};

/** Nothing is cached: the demo transport answers in the page. */
export const resetUserCache: typeof Real.resetUserCache = () => {};

export const completeOnboarding: typeof Real.completeOnboarding = async (data) => {
  await getControlPlaneClient(UserService).completeOnboarding({ onboardingData: data as never });
};

export const provisionManagedKey: typeof Real.provisionManagedKey = async () => ({ synced: false });
