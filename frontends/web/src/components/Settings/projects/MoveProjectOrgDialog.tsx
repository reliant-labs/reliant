// Copyright (c) 2025 Reliant Labs

/**
 * Move a project to another organization (control-plane
 * docs/design/org-project-move.md §4.1). A move is consequential and hard to
 * undo by hand: it moves every chat and run, pauses automations, unshares what
 * was shared and changes who pays for the next run. So the dialog:
 *
 * 1. reads the server's preview for the chosen org and says what will happen
 *    in counts, not a generic warning;
 * 2. names the running work that refuses the move, and offers to stop it
 *    ("Stop all and move") with the same stop the chat's own button uses;
 * 3. asks for the project's name, typed, before it moves anything.
 *
 * The move itself is the server's: MoveProjectToOrg with a request key made
 * once per opening, so a retry after a lost answer is the same move, and
 * GetProjectOrgMove until it settles. The typed name is a speed bump; the
 * server decides who may move what.
 */
import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowRight, Loader2 } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import { ProjectOrgMoveState, type ProjectOrgMove } from "@/gen/reliant/v1/project_pb";
import { cancelChat } from "@/hooks/chat-actions";
import type { Project } from "@/hooks/project-queries";
import {
  projectServiceKeys,
  useGetProjectOrgMove,
  useMoveProjectToOrg,
  usePreviewProjectOrgMove,
} from "@/hooks/project-service-hooks_gen";
import { useActiveOrg } from "@/hooks/useOrgs";
import { hasErrorReason } from "@/lib/errorReasons";
import { logger } from "@/lib/logger";
import { toast } from "@/lib/toast-manager";

import {
  activityLabel,
  blockerSentence,
  effectLines,
  hasBlockers,
  isMoveSettled,
  movedSentence,
  moveTargets,
} from "./projectOrgMove";

/** How many times "Stop all" stops the listed chats and reads the preview again. */
const STOP_ROUNDS = 3;
/** How often an unsettled move is read again. */
const MOVE_POLL_MS = 1500;

export interface MoveProjectOrgDialogProps {
  project: Project;
  isOpen: boolean;
  onClose: () => void;
}

export function MoveProjectOrgDialog({ project, isOpen, onClose }: MoveProjectOrgDialogProps) {
  const queryClient = useQueryClient();
  const { orgs, select } = useActiveOrg();
  const targets = useMemo(() => moveTargets(orgs, project.org_id), [orgs, project.org_id]);
  const [toOrgId, setToOrgId] = useState("");
  const [typedName, setTypedName] = useState("");
  const [phase, setPhase] = useState<"idle" | "stopping" | "moving">("idle");
  const [error, setError] = useState<string | null>(null);
  const [moveId, setMoveId] = useState<string | null>(null);
  // One key per opening: a retry after a lost answer is the same move.
  const requestKey = useRef("");

  useEffect(() => {
    if (isOpen) {
      requestKey.current = crypto.randomUUID();
      return;
    }
    queryClient.removeQueries({ queryKey: projectServiceKeys.previewProjectOrgMove.all });
    setToOrgId("");
    setTypedName("");
    setPhase("idle");
    setError(null);
    setMoveId(null);
  }, [isOpen, queryClient]);

  // The only other org is the obvious choice.
  useEffect(() => {
    if (isOpen && !toOrgId && targets.length === 1) setToOrgId(targets[0].id);
  }, [isOpen, toOrgId, targets]);

  const previewQuery = usePreviewProjectOrgMove(
    { projectId: project.id, toOrgId },
    { enabled: isOpen && !!toOrgId && !moveId, staleTime: 0, retry: false },
  );
  const preview = previewQuery.data ?? null;
  const move = useMoveProjectToOrg();
  const moveQuery = useGetProjectOrgMove(
    { moveId: moveId ?? "" },
    {
      enabled: !!moveId,
      staleTime: 0,
      refetchInterval: (q) => {
        const state = q.state.data?.move?.state;
        return state !== undefined && isMoveSettled(state) ? false : MOVE_POLL_MS;
      },
    },
  );

  const nameOf = (id: string | undefined) => orgs.find((o) => o.id === id)?.name ?? "the other organization";
  const fromName = nameOf(preview?.fromOrgId ?? project.org_id);
  const target = targets.find((o) => o.id === toOrgId) ?? null;
  const toName = target?.name ?? "the organization";

  // A move that settled ends the dialog: a toast says where the project went.
  const settled = moveQuery.data?.move;
  useEffect(() => {
    if (!settled || !isMoveSettled(settled.state)) return;
    finish(settled);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per settled read
  }, [settled?.state]);

  function finish(m: ProjectOrgMove) {
    if (m.state === ProjectOrgMoveState.ABORTED) {
      setPhase("idle");
      setMoveId(null);
      requestKey.current = crypto.randomUUID();
      setError(m.detail ? `The project was not moved: ${m.detail}` : "The project was not moved. Nothing changed.");
      void previewQuery.refetch();
      return;
    }
    const to = targets.find((o) => o.id === m.toOrgId);
    toast.notify(movedSentence(project.name, to?.name ?? toName, m.state), {
      description: "Its chats, runs and automations are now in that organization.",
      action: to ? { label: `Switch to ${to.name}`, onClick: () => select(to.id) } : undefined,
    });
    onClose();
  }

  /** Stop every chat the preview lists, reading it again, until none blocks or the rounds run out. */
  async function stopBlockers(): Promise<boolean> {
    let current = preview;
    for (let round = 0; round < STOP_ROUNDS && current && hasBlockers(current.blockers); round++) {
      const chats = current.blockers?.chats ?? [];
      if (chats.length === 0) break; // background processes alone: nothing here stops them
      await Promise.allSettled(chats.map((c) => cancelChat(c.chatId)));
      const next = await previewQuery.refetch();
      current = next.data ?? null;
    }
    return !!current && !hasBlockers(current.blockers);
  }

  async function handleMove() {
    if (!preview || !target) return;
    setError(null);
    try {
      if (hasBlockers(preview.blockers)) {
        setPhase("stopping");
        if (!(await stopBlockers())) {
          setPhase("idle");
          setError("Some work is still running. Stop it from its chat, then move the project.");
          return;
        }
      }
      setPhase("moving");
      const resp = await move.mutateAsync({
        projectId: project.id,
        toOrgId: target.id,
        expectedFromOrgId: preview.fromOrgId,
        requestKey: requestKey.current,
      });
      const m = resp.move;
      if (!m) throw new Error("The server did not say what happened to the move.");
      if (isMoveSettled(m.state)) finish(m);
      else setMoveId(m.id);
    } catch (err) {
      logger.error("[MoveProjectOrg] move failed", err);
      setPhase("idle");
      if (hasErrorReason(err, ["project_busy", "org_changed"])) void previewQuery.refetch();
      setError(err instanceof Error && err.message ? err.message : "The project was not moved. Please try again.");
    }
  }

  const busy = phase !== "idle";
  const blocked = hasBlockers(preview?.blockers);
  const nameMatches = typedName.trim() === project.name.trim();
  const lines = effectLines(preview?.effects, fromName, toName);

  return (
    <Modal open={isOpen} onClose={busy ? () => {} : onClose} title={`Move ${project.name} to another organization`} size="md">
      <div className="space-y-5 p-1" data-testid="move-project-org-dialog">
        {targets.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            You belong to no other organization. Join or create one, then move the project there.
          </p>
        ) : (
          <>
            <div>
              <label htmlFor="move-project-org-target" className="mb-1.5 block text-sm font-medium">
                Move to
              </label>
              <select
                id="move-project-org-target"
                className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                value={toOrgId}
                disabled={busy}
                onChange={(e) => {
                  setToOrgId(e.target.value);
                  setError(null);
                }}
              >
                <option value="" disabled>
                  Choose an organization
                </option>
                {targets.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.name}
                  </option>
                ))}
              </select>
            </div>

            {toOrgId && previewQuery.isFetching && !preview && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                Checking what the move would do…
              </div>
            )}

            {previewQuery.error && !preview && (
              <p className="text-sm text-destructive-ink" role="alert">
                {previewQuery.error.message || "Could not check this move. Please try again."}
              </p>
            )}

            {preview && (
              <>
                <div className="flex items-center gap-2 text-sm font-medium">
                  <span className="truncate">{fromName}</span>
                  <ArrowRight className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span className="truncate">{toName}</span>
                </div>

                {blocked && (
                  <div className="rounded-lg border border-warning/30 bg-warning/10 p-3" data-testid="move-project-org-blockers">
                    <div className="flex items-start gap-2">
                      <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0 text-warning" aria-hidden="true" />
                      <p className="text-sm">{blockerSentence(preview.blockers)}</p>
                    </div>
                    {(preview.blockers?.chats.length ?? 0) > 0 && (
                      <ul className="mt-2 space-y-1">
                        {preview.blockers?.chats.map((c) => (
                          <li key={c.chatId} className="flex justify-between gap-3 text-xs">
                            <span className="truncate">{c.title || "Untitled chat"}</span>
                            <span className="flex-shrink-0 text-muted-foreground">{activityLabel(c.activity)}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                )}

                <div>
                  <h4 className="mb-2 text-sm font-medium">What the move does</h4>
                  <ul className="list-disc space-y-1.5 pl-5">
                    {lines.map((line) => (
                      <li key={line} className="text-sm text-muted-foreground">
                        {line}
                      </li>
                    ))}
                  </ul>
                  {!preview.effects?.controlPlaneEffectsKnown && (
                    <p className="mt-2 text-xs text-muted-foreground">
                      Secret access and hosted deployments in {fromName} could not be checked; the move handles them
                      either way.
                    </p>
                  )}
                </div>

                <div>
                  <label htmlFor="move-project-org-confirm" className="mb-1.5 block text-sm font-medium">
                    Type <span className="font-mono">{project.name}</span> to confirm
                  </label>
                  <Input
                    id="move-project-org-confirm"
                    value={typedName}
                    onChange={(e) => setTypedName(e.target.value)}
                    placeholder={project.name}
                    autoComplete="off"
                    spellCheck={false}
                    disabled={busy}
                    className="forge-ui"
                  />
                </div>
              </>
            )}
          </>
        )}

        {error && (
          <div className="rounded-lg border border-destructive/20 bg-destructive/10 p-3" role="alert">
            <p className="text-sm text-destructive-ink">{error}</p>
          </div>
        )}

        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          {targets.length > 0 && (
            <Button
              variant="danger"
              onClick={() => void handleMove()}
              disabled={!preview || !nameMatches || busy}
              leftIcon={busy ? <Loader2 className="h-3 w-3 animate-spin" /> : undefined}
              data-testid="move-project-org-confirm-button"
            >
              {phase === "stopping"
                ? "Stopping…"
                : phase === "moving"
                  ? "Moving…"
                  : blocked
                    ? "Stop all and move"
                    : "Move project"}
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}
