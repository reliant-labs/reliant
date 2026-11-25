"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Sign-in gating for a page or section, driven by the session forge already
// derives from the JWT. This gates RENDERING only — the backend remains the
// source of truth; a determined client can always call the RPC, and it will be
// rejected server-side. Wrap a page/section in <RouteGuard> to show it only to
// a signed-in caller.
import { useSession } from "./session.js";

import type { ReactElement, ReactNode } from "react";

export interface RouteGuardProps {
  children: ReactNode;
  /** Shown while the session is still loading. */
  loading?: ReactNode;
  /** Shown when nobody is signed in. Defaults to a simple notice. */
  fallback?: ReactNode;
}

export function RouteGuard({
  children,
  loading,
  fallback,
}: RouteGuardProps): ReactElement {
  const session = useSession();

  if (session.isLoading) {
    return <>{loading ?? null}</>;
  }
  if (!session.isAuthenticated) {
    return (
      <>
        {fallback ?? (
          <div className="flex min-h-[12rem] flex-col items-center justify-center gap-1 text-center">
            <p className="text-sm font-medium text-ink">Not signed in</p>
            <p className="text-sm text-ink-muted">Sign in to view this page.</p>
          </div>
        )}
      </>
    );
  }
  return <>{children}</>;
}
