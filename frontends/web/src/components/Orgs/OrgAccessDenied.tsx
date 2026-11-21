// Copyright (c) 2025 Reliant Labs

import { Building2, ChevronRight } from "lucide-react";
import { Link } from "@tanstack/react-router";

import { AuthHeader, AuthLayout } from "@/components/AuthLayout";
import { homePath, orgHomePath } from "@/lib/paths";
import { orgSegment, type RouteOrgChoice } from "@/lib/routeOrg";

/**
 * What `/o/<org>` shows when the org is not one of the person's (control-plane
 * docs/design/unified-shell-and-projects.md §3.2): one page for an org they
 * are not in and for one that does not exist, worded so it never says which,
 * with the orgs they do have.
 */
export function OrgAccessDenied({ orgs }: { orgs: readonly RouteOrgChoice[] }) {
  return (
    <AuthLayout>
      <div className="space-y-6 p-6 sm:p-8" data-testid="org-access-denied">
        <AuthHeader
          title="You don't have access to this organization"
          description="This link is to an organization you're not a member of, or to one that doesn't exist. An owner of an organization can invite you to it."
        />
        {orgs.length > 0 ? (
          <nav aria-labelledby="org-access-denied-yours" className="space-y-2">
            <h2 id="org-access-denied-yours" className="text-sm font-medium text-muted-foreground">
              Your organizations
            </h2>
            <ul className="divide-y divide-border/60 overflow-hidden rounded-lg border border-border/60 bg-background">
              {orgs.map((org) => (
                <li key={org.id}>
                  <Link
                    to={orgHomePath(orgSegment(org))}
                    data-testid={`org-access-denied-${orgSegment(org)}`}
                    className="flex min-h-[44px] items-center gap-3 px-4 py-2 text-sm text-foreground transition-colors hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <Building2 className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                    <span className="min-w-0 flex-1 truncate">{org.name || orgSegment(org)}</span>
                    <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  </Link>
                </li>
              ))}
            </ul>
          </nav>
        ) : (
          <p className="text-center text-sm text-muted-foreground">
            You aren't a member of any organization yet.{" "}
            <Link to={homePath()} className="text-primary-ink underline-offset-4 hover:underline">
              Go to your home page
            </Link>
          </p>
        )}
      </div>
    </AuthLayout>
  );
}
