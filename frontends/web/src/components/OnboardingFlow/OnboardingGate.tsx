/**
 * Sends a signed-in user who has not finished onboarding to `/onboarding`
 * before any app route that needs it, carrying the page they asked for as
 * `returnTo` so the wizard's exit lands them back there (leaveOnboarding).
 * Which routes, and why the exemptions: ./onboardingGate.ts.
 *
 * Mounted inside the `_authenticated` layout's guard in both shells (Reliant's
 * AuthGuard, the forge site's ForgeAuthGuard), so it only ever sees a
 * signed-in user. It never completes onboarding: the wizard is the only
 * thing that does.
 */

import { useEffect, type ReactNode } from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";

import { LoadingSpinner } from "@/components/Layout/LoadingSpinner";
import { useCurrentUser } from "@/hooks/useOnboardingQueries";
import { logger } from "@/lib/logger";
import { useAuthStore } from "@/store/authStore";

import {
  claimLandingExcuses,
  forgeSetupSkippedBy,
  ONBOARDING_EXEMPT_PREFIXES,
  onboardingGateDecision,
  onboardingGateTarget,
} from "./onboardingGate";

export function OnboardingGate({
  children,
  exempt = ONBOARDING_EXEMPT_PREFIXES,
  honourForgeSkip = false,
}: {
  children: ReactNode;
  /** Path prefixes left alone (onboardingGate.ts): Reliant's by default. */
  exempt?: readonly string[];
  /** Let through a user who chose the wizard's forge-only skip (the forge site only). */
  honourForgeSkip?: boolean;
}) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const href = useRouterState({ select: (s) => s.location.href });
  const navigate = useNavigate();
  const { data: currentUser, isLoading } = useCurrentUser();
  const authUserId = useAuthStore((s) => s.user?.id);
  const decision = onboardingGateDecision({
    pathname,
    userLoading: isLoading,
    user: currentUser,
    exempt,
    excused: claimLandingExcuses(authUserId, pathname) || (honourForgeSkip && forgeSetupSkippedBy(authUserId)),
  });

  useEffect(() => {
    if (decision !== "onboard") return;
    logger.info("[Onboarding] gate: not finished, sending to /onboarding", { pathname });
    void navigate(onboardingGateTarget(href));
  }, [decision, href, pathname, navigate]);

  if (decision !== "allow") return <LoadingSpinner />;
  return <>{children}</>;
}
