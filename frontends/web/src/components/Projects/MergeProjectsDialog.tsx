// Copyright (c) 2025 Reliant Labs

/**
 * Merge one project into another: one repository that became two projects
 * (two machines added it before projects were matched by repository) folded
 * back into one (ProjectService.MergeProjects).
 *
 * A merge moves every chat, workspace and checkout of the source and leaves
 * it empty and scheduled for deletion, so the dialog:
 *
 * 1. reads the server's preview (PreviewProjectMerge) and says what moves,
 *    in counts and by machine name, not a generic warning;
 * 2. names what refuses the merge now, in the server's own sentences, and
 *    keeps Merge disabled while anything does;
 * 3. says what happens to the source afterwards (deleted after the restore
 *    window, nothing on disk touched).
 *
 * The rules are the server's: it checks the blockers again as it commits and
 * its error, if any, is shown as it said it.
 */
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";
import { AlertTriangle, ArrowDown, Loader2 } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { chatServiceKeys } from "@/hooks/chat-service-hooks_gen";
import { projectFromProto } from "@/hooks/project-queries";
import { projectServiceKeys, useMergeProjects, usePreviewProjectMerge } from "@/hooks/project-service-hooks_gen";
import { triggerServiceKeys } from "@/hooks/trigger-service-hooks_gen";
import { worktreeServiceKeys } from "@/hooks/worktree-service-hooks_gen";
import { logger } from "@/lib/logger";
import { toast } from "@/lib/toast-manager";
import type { ProjectCatalogRow } from "@/services/projects/catalog";
import { MERGE_RESTORE_DAYS, machinesOf, mergeMoveLines } from "@/services/projects/projectMerge";
import { useProjectStore } from "@/store/projectStore";

/** Mounted while open: the parent renders it for one chosen pair and unmounts it to close. */
export interface MergeProjectsDialogProps {
  /** The project folded into `target` and then scheduled for deletion. */
  source: ProjectCatalogRow;
  /** The project that stays, with everything of `source`. */
  target: ProjectCatalogRow;
  onClose: () => void;
}

/** A server error as the server said it, without Connect's "[code]" framing. */
function messageOf(err: unknown, fallback: string): string {
  if (err instanceof ConnectError) return err.rawMessage || fallback;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

export function MergeProjectsDialog({ source, target, onClose }: MergeProjectsDialogProps) {
  const queryClient = useQueryClient();
  const [merging, setMerging] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // A preview is of one moment: the next opening asks again.
  useEffect(() => () => queryClient.removeQueries({ queryKey: projectServiceKeys.previewProjectMerge.all }), [queryClient]);

  // Off while merging: the merge's invalidation must not re-ask for a preview
  // of a source it just scheduled for deletion. A failed merge turns it back
  // on, and the fresh preview shows what refused it.
  const previewQuery = usePreviewProjectMerge(
    { sourceProjectId: source.key, targetProjectId: target.key },
    { enabled: !merging, staleTime: 0, retry: false },
  );
  const preview = previewQuery.data ?? null;
  const merge = useMergeProjects();

  const blockers = preview?.blockers ?? [];
  const blocked = blockers.length > 0;
  const lines = mergeMoveLines(preview?.counts, target.name);

  async function handleMerge() {
    if (!preview || blocked) return;
    setError(null);
    setMerging(true);
    try {
      // The hook invalidates every ProjectService query (the catalog included).
      const resp = await merge.mutateAsync({ sourceProjectId: source.key, targetProjectId: target.key });
      // The source's chats, workspaces and automations now belong to target.
      for (const queryKey of [chatServiceKeys.all, worktreeServiceKeys.all, triggerServiceKeys.all]) {
        void queryClient.invalidateQueries({ queryKey });
      }
      // The project open in the shell was the source: open the one it became.
      const store = useProjectStore.getState();
      if (store.currentProject?.id === source.key && resp.project) {
        store.selectProject(projectFromProto(resp.project)).catch((err: unknown) => {
          logger.warn("[MergeProjects] Could not open the merged project", { err });
        });
      }
      toast.success(`Merged ${source.name} into ${target.name}`);
      onClose();
    } catch (err) {
      logger.error("[MergeProjects] merge failed", err);
      setError(messageOf(err, "The projects were not merged. Please try again."));
      setMerging(false);
    }
  }

  return (
    <Modal
      open
      onClose={merging ? () => {} : onClose}
      title={`Merge ${source.name} into ${target.name}?`}
      description="Both are projects of one repository. Everything in the first moves into the second, which stays."
      size="md"
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose} disabled={merging}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="md"
            onClick={() => void handleMerge()}
            disabled={!preview || blocked || merging}
            loading={merging}
            data-testid="merge-projects-confirm"
          >
            Merge projects
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4" data-testid="merge-projects-dialog">
        <div className="flex flex-col items-stretch gap-1.5">
          <ProjectIdentity label="Moves out of" row={source} />
          <ArrowDown className="h-4 w-4 self-center text-muted-foreground" aria-hidden="true" />
          <ProjectIdentity label="Into" row={target} />
        </div>

        {previewQuery.isFetching && !preview && (
          <p className="flex items-center gap-2 text-ui text-muted-foreground" role="status">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Checking what the merge would move…
          </p>
        )}

        {previewQuery.error && !preview && (
          <p className="text-ui text-destructive-ink" role="alert" data-testid="merge-projects-preview-error">
            {messageOf(previewQuery.error, "Could not check this merge. Please try again.")}
          </p>
        )}

        {preview && (
          <>
            {blocked && (
              <div className="rounded-md border border-border/60 bg-background px-3 py-2.5" role="alert" data-testid="merge-projects-blockers">
                <p className="text-ui font-medium text-foreground">This merge can’t run yet</p>
                <ul className="mt-1.5 flex flex-col gap-1.5">
                  {blockers.map((b, i) => (
                    <li key={`${b.kind}-${b.daemonId ?? ""}-${b.chatId ?? ""}-${i}`} className="flex items-start gap-2 text-ui text-muted-foreground">
                      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning-ink" aria-hidden="true" />
                      <span>{b.message}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}

            <section aria-labelledby="merge-projects-moves">
              <h3 id="merge-projects-moves" className="mb-1.5 text-ui font-medium text-foreground">
                What moves to {target.name}
              </h3>
              {lines.length > 0 ? (
                <ul className="list-disc space-y-1 pl-5" data-testid="merge-projects-moves">
                  {lines.map((line) => (
                    <li key={line} className="text-ui text-muted-foreground">
                      {line}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-ui text-muted-foreground">Nothing: {source.name} has no chats, workspaces or checkouts.</p>
              )}
            </section>

            <p className="text-ui text-muted-foreground text-pretty" data-testid="merge-projects-after">
              {source.name} is then left empty and scheduled for deletion: it leaves your projects, and can be restored, empty,
              for {MERGE_RESTORE_DAYS} days. <span className="font-medium text-foreground">Nothing on disk is deleted</span>.
            </p>
          </>
        )}

        {error && (
          <p className="text-ui text-destructive-ink" role="alert" data-testid="merge-projects-error">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}

/** One side of the merge: the name, and the machines it is on, so two same-named projects are told apart. */
function ProjectIdentity({ label, row }: { label: string; row: ProjectCatalogRow }) {
  const machines = machinesOf(row);
  return (
    <div className="rounded-md border border-border/60 bg-background px-3 py-2.5">
      <div className="text-meta text-muted-foreground">{label}</div>
      <div className="truncate text-ui font-medium text-foreground">{row.name}</div>
      <div className="truncate text-meta text-muted-foreground">{machines ? `On ${machines}` : "Not on any machine"}</div>
    </div>
  );
}
