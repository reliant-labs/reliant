// Copyright (c) 2025 Reliant Labs

/**
 * A project's Settings tab › Sharing: whether the project is shared with its
 * org, since when, and the share toggle (ShareProjectDialog). Shown to the
 * owner of a project that belongs to an org, and only with a control plane
 * (orgs live there); a viewer of a shared project has no Settings tab.
 */

import { useState } from "react";
import { Lock, Users } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { useActiveOrg } from "@/hooks/useOrgs";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { hasControlPlane } from "@/services/controlPlane/config";
import type { Project } from "@/store/projectStore";

import { ShareProjectDialog } from "./ShareProjectDialog";

export function ProjectSharingSection({ project }: { project: Project }) {
  const { orgs } = useActiveOrg();
  const [open, setOpen] = useState(false);
  if (!hasControlPlane || !project.org_id || project.viewer_access) return null;

  const orgName = orgs.find((o) => o.id === project.org_id)?.name ?? "your organization";
  const shared = project.visibility === "org";
  const Icon = shared ? Users : Lock;

  return (
    <section
      aria-labelledby="project-sharing-heading"
      className="flex flex-col gap-3 rounded-xl border border-border bg-card px-4 py-3"
      data-testid="project-sharing"
    >
      <h2 id="project-sharing-heading" className="text-section text-foreground">
        Sharing
      </h2>
      <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-start gap-2">
          <Icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          <div className="min-w-0">
            <p className="text-ui font-medium text-foreground" data-testid="project-sharing-state">
              {shared ? `Shared with ${orgName}` : "Private"}
            </p>
            <p className="text-meta text-muted-foreground">
              {shared ? (
                <>
                  Members of {orgName} can see it, read-only, and list the chats in it you share
                  {project.shared_at && (
                    <>
                      {" · since "}
                      <time dateTime={project.shared_at} title={formatAbsoluteTime(project.shared_at)}>
                        {formatRelativeTime(project.shared_at)}
                      </time>
                    </>
                  )}
                  .
                </>
              ) : (
                "Only you can see it."
              )}
            </p>
          </div>
        </div>
        <Button variant="outline" size="sm" onClick={() => setOpen(true)} data-testid="project-sharing-toggle">
          {shared ? "Stop sharing…" : `Share with ${orgName}…`}
        </Button>
      </div>
      <ShareProjectDialog project={project} orgName={orgName} open={open} onClose={() => setOpen(false)} />
    </section>
  );
}
