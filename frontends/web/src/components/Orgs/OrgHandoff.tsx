// Copyright (c) 2025 Reliant Labs

import { useEffect } from "react";
import { useRouterState } from "@tanstack/react-router";

import { AuthHeader, AuthLayout } from "@/components/AuthLayout";
import { forgeAppOrigin, reliantAppOrigin } from "@/lib/shell";

/** The host that serves an org page this one does not. */
export type OrgHandoffHost = "forge" | "reliant";

const LABELS: Record<OrgHandoffHost, string> = { forge: "Forge", reliant: "Reliant" };

/** The URL on `host` of the page at `href` (a path with its query and hash), or null when `host` is this origin. */
export function orgHandoffTarget(host: OrgHandoffHost, href: string, here: string): string | null {
  const target = new URL(href, host === "forge" ? forgeAppOrigin() : reliantAppOrigin());
  return target.origin === here ? null : target.href;
}

/**
 * An org page this host's route tree does not serve, handed to the host that
 * does (control-plane docs/design/unified-shell-and-projects.md §12: an org
 * URL always resolves). Until the one shell (U6) the two hosts mount
 * different pages: Sites and Usage are the forge host's, chats, the Inbox
 * and Workflows Reliant's. The same URL — org, path, query and hash — opens
 * on the other host, which resolves the org itself.
 *
 * Rendered only inside a member org (the `/o/$org` layout renders no child
 * otherwise), so a link to an org that is not the person's never leaves the
 * "no access" page. In the desktop app the main process opens the other host
 * in the browser and the window stays here, on the link.
 */
export function OrgHandoff({ to }: { to: OrgHandoffHost }) {
  const href = useRouterState({ select: (s) => s.location.href });
  const target = orgHandoffTarget(to, href, typeof window === "undefined" ? "" : window.location.origin);
  useEffect(() => {
    if (target) window.location.replace(target);
  }, [target]);
  const label = LABELS[to];
  return (
    <AuthLayout>
      <div className="space-y-6 p-6 sm:p-8" data-testid="org-handoff" data-host={to}>
        <AuthHeader
          title={target ? `Opening ${label}…` : `This page is on ${label}`}
          description={
            target
              ? `This page is part of ${label}. If it doesn't open, use the link below.`
              : `This page is part of ${label}, which isn't served at this address.`
          }
        />
        {target ? (
          <a
            href={target}
            data-testid="org-handoff-link"
            className="block text-center text-sm text-primary-ink underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring"
          >
            Open on {label}
          </a>
        ) : null}
      </div>
    </AuthLayout>
  );
}
