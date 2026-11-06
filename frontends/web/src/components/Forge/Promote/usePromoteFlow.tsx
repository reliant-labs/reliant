// Copyright (c) 2025 Reliant Labs

/**
 * The promote flow every console page shares: the request the reader made,
 * and the plan dialog that carries it out (PromoteDialog). A rollback is a
 * promotion to an older release, so "Roll back prod to v1.4" on the Overview,
 * on a workload or on a release all land here, and all go through the same
 * plan and confirmation — never a direct write.
 *
 * The plan needs a daemon on the project's checkout, so a console opened on a
 * project no Reliant project here declares can read but not promote, and the
 * control says why.
 */

import { useMemo, useState, type ReactNode } from "react";

import { useProject } from "@/hooks/project-queries";
import { isForgeShell } from "@/lib/shell";

import { useForgeConsole } from "../Console/consoleContext";
import type { PromoteControl, PromoteRequest } from "../Releases/ReleasesList";
import { shortVersion } from "../Releases/releaseModel";
import { PromoteDialog } from "./PromoteDialog";
import { useObsReadOnly } from "../Observability/obsReadOnly";

export function usePromoteFlow(): { control: PromoteControl; dialog: ReactNode } {
  const { projectId } = useForgeConsole();
  const projectName = useProject(projectId)?.name;
  const [request, setRequest] = useState<PromoteRequest | null>(null);
  const lock = useObsReadOnly();
  const control = useMemo<PromoteControl>(
    () => ({
      onPromote: setRequest,
      blockedReason: lock.readOnly
        ? (lock.reason ?? "Read-only")
        : projectId
        ? null
        : isForgeShell()
          ? "Promotions are planned against a checkout of this project: run `forge env promote --to <env>` there."
          : "Promoting plans the change against this project's checkout. Open the project in Reliant, with its daemon running, to promote from here.",
    }),
    [projectId, lock.readOnly, lock.reason]
  );
  const dialog =
    request && projectId ? (
      <PromoteDialog
        isOpen
        onClose={() => setRequest(null)}
        projectId={projectId}
        env={request.env}
        release={request.version}
        projectName={projectName}
        title={
          request.intent === "rollback"
            ? `Roll back ${request.env} to ${shortVersion(request.version)}`
            : `Promote ${shortVersion(request.version)} to ${request.env}`
        }
      />
    ) : null;
  return { control, dialog };
}
