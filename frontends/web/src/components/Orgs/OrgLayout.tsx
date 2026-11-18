// Copyright (c) 2025 Reliant Labs

import { useEffect, type ComponentType, type ReactNode } from "react";
import { Outlet, useRouter } from "@tanstack/react-router";

import { AuthHeader, AuthLayout } from "@/components/AuthLayout";
import Button from "@/components/forge-ui/button";
import { LoadingSpinner } from "@/components/Layout/LoadingSpinner";
import type { OrgRouteState } from "@/lib/routeOrg";

import { OrgAccessDenied } from "./OrgAccessDenied";

export interface OrgLayoutProps {
  /** What the layout's beforeLoad decided (lib/orgRouting.ts, resolveOrgRoute). */
  state: OrgRouteState;
  /** The shell's sign-in guard: sends a signed-out visitor to sign in and back here. */
  Guard: ComponentType<{ children: ReactNode }>;
}

/**
 * The `/o/$org` layout (control-plane docs/design/unified-shell-and-projects.md
 * §3.2): the org's pages once its beforeLoad has resolved the org, and
 * nothing of them before. Every other answer is a page of its own.
 */
export function OrgLayout({ state, Guard }: OrgLayoutProps) {
  switch (state.kind) {
    case "member":
      return <Outlet />;
    case "not-member":
      return (
        <Guard>
          <OrgAccessDenied orgs={state.orgs} />
        </Guard>
      );
    case "signed-out":
      return (
        <Guard>
          <ResolveAgain />
        </Guard>
      );
    case "error":
      return (
        <Guard>
          <OrgLoadFailed error={state.error} />
        </Guard>
      );
  }
}

/**
 * Signed in after the layout resolved (a session restored late, an API key
 * entered inline): resolve again now that there is someone to resolve for.
 */
function ResolveAgain() {
  const router = useRouter();
  useEffect(() => {
    void router.invalidate();
  }, [router]);
  return <LoadingSpinner />;
}

/** The membership list could not be read: say so and offer a retry, never a guess at the org. */
function OrgLoadFailed({ error }: { error: Error }) {
  const router = useRouter();
  return (
    <AuthLayout>
      <div className="space-y-6 p-6 sm:p-8" data-testid="org-load-failed">
        <AuthHeader title="Couldn't load your organizations" description={error.message || "Check your connection and try again."} />
        <Button variant="primary" fullWidth onClick={() => void router.invalidate()} data-testid="org-load-retry">
          Try again
        </Button>
      </div>
    </AuthLayout>
  );
}
