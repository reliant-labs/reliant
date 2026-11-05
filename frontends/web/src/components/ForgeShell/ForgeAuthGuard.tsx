// Copyright (c) 2025 Reliant Labs

/**
 * The forge shell's guard: signed in, or off to sign in. Nothing else.
 *
 * Reliant's authenticated layouts also run onboarding, the machine and daemon
 * gates and the API-key check. The forge shell has none of those (lib/shell.ts):
 * an account is all a control-plane page needs. The same accounts and the same
 * GoTrue project as Reliant; the session is this origin's own, so a user signs
 * in once on the forge host.
 *
 * Unlike Reliant's AuthGuard it keeps where the user was going: a deep link to
 * a console page survives the sign-in (`/auth?redirect=<path>`, which the sign-in
 * screen honours for a same-origin path, through OAuth and magic links too).
 */

import { useEffect, type ReactNode } from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";

import { LoadingSpinner } from "@/components/Layout/LoadingSpinner";
import { isSafeReturnTo } from "@/lib/returnTo";
import { useAuthStore } from "@/store/authStore";

export type ForgeAuthDecision = "wait" | "sign-in" | "verify-email" | "allow";

interface GuardUser {
  is_anonymous?: boolean;
  email_confirmed_at?: string | null;
}

/** What the guard does for this auth state. Pure. */
export function forgeAuthDecision(state: { initialized: boolean; loading: boolean; user: GuardUser | null }): ForgeAuthDecision {
  if (!state.initialized || state.loading) return "wait";
  if (!state.user) return "sign-in";
  // An anonymous session (a claim in flight) is let through, as Reliant's
  // guard does; an email account must have confirmed its address.
  if (!state.user.is_anonymous && !state.user.email_confirmed_at) return "verify-email";
  return "allow";
}

/** The sign-in URL that returns to `returnTo` afterwards (the home page needs no return). */
export function forgeSignInHref(returnTo: string): string {
  if (!isSafeReturnTo(returnTo) || returnTo === "/" || returnTo.startsWith("/auth")) return "/auth";
  return `/auth?${new URLSearchParams({ redirect: returnTo })}`;
}

export function ForgeAuthGuard({ children }: { children: ReactNode }) {
  const user = useAuthStore((state) => state.user);
  const loading = useAuthStore((state) => state.loading);
  const initialized = useAuthStore((state) => state.initialized);
  const initialize = useAuthStore((state) => state.initialize);
  const navigate = useNavigate();
  const here = useRouterState({ select: (s) => s.location.href });
  const decision = forgeAuthDecision({ initialized, loading, user });

  useEffect(() => {
    if (!initialized) void initialize();
  }, [initialized, initialize]);

  useEffect(() => {
    if (decision === "sign-in") void navigate({ href: forgeSignInHref(here), replace: true });
    if (decision === "verify-email") void navigate({ href: "/verify-email", replace: true });
  }, [decision, here, navigate]);

  if (decision !== "allow") return <LoadingSpinner />;
  return <>{children}</>;
}
