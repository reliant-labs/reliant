// Copyright (c) 2026 Reliant Labs

/**
 * The worktree-reclaim item's "Review" action. A machine's daemon proposes
 * removing idle worktrees that hold work only they have (uncommitted changes,
 * unpushed commits, commits only their reflog reaches), and removes none of
 * them until the person approves. This lists each one with what it holds, how
 * long it has been idle and a reviewing agent's note, and sends one decision
 * per worktree: remove it, or keep it.
 *
 * Both are recorded on the machine, bound to the state the person saw (its
 * fingerprint): a worktree that changes after it is proposed again. Keep stops
 * it being proposed until then. An approval goes to the machine as a command
 * from the server, the only approval it acts on: it saves everything git can
 * see of the worktree to a local ref (refs/reliant/reclaimed/...), checks it
 * all again, and only then removes it; `reliant worktree restore` brings it
 * back. What became of each removal comes back in the response
 * (removalSummary), and a refused one is proposed again with the reason.
 */

import { useState } from "react";
import { toast } from "sonner";

import type { InboxWorktreeReclaim } from "@/gen/reliant/v1/inbox_pb";
import type { DecideWorktreeReclaimResponse } from "@/gen/reliant/v1/worktree_reclaim_pb";
import type { WorktreeReclaimProposal } from "@/gen/reliant/v1/tools_daemon_pb";
import { useDecideWorktreeReclaim } from "@/hooks/inbox-queries";
import { formatBytes } from "@/lib/formatBytes";
import Modal from "../forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { CardInset } from "../forge-ui/card";
import { cn } from "@/lib/utils";

const REASON_LABEL: Record<string, string> = {
  dirty: "Uncommitted changes",
  unpushed: "Commits not pushed",
  unreachable_history: "Commits on no branch",
};

export function reclaimReasonLabel(reason: string): string {
  return REASON_LABEL[reason] ?? reason;
}

/** "6 days" from seconds. */
export function idleFor(seconds: bigint): string {
  const s = Number(seconds);
  if (s <= 0) return "";
  const days = Math.floor(s / 86400);
  if (days >= 1) return `${days} ${days === 1 ? "day" : "days"}`;
  const hours = Math.max(1, Math.floor(s / 3600));
  return `${hours} ${hours === 1 ? "hour" : "hours"}`;
}

/** The proposals still waiting on a decision. */
export function undecided(
  item: InboxWorktreeReclaim,
): WorktreeReclaimProposal[] {
  return item.proposals.filter((p) => p.decision === "");
}

/** "3 worktrees proposed for removal (1.2 GB) · 1 approved · offline". */
export function reclaimDetail(item: InboxWorktreeReclaim): string {
  const open = undecided(item);
  const size = open.reduce((sum, p) => sum + Number(p.sizeBytes), 0);
  const parts = [
    `${open.length} ${open.length === 1 ? "worktree" : "worktrees"} proposed for removal${size > 0 ? ` (${formatBytes(size)})` : ""}`,
  ];
  const approved = item.proposals.length - open.length;
  if (approved > 0) parts.push(`${approved} approved`);
  if (item.total > item.proposals.length)
    parts.push(`${item.total - item.proposals.length} more not shown`);
  if (item.mode === "dry-run")
    parts.push("removal is off on this machine (dry run)");
  if (!item.online) parts.push("offline");
  return parts.join(" · ");
}

type Choice = "approve" | "decline" | "";

/** The toast's description for a response: removals started, refused, unsupported. */
export function removalSummary(
  results: DecideWorktreeReclaimResponse["results"],
): string {
  const removing = results.filter(
    (r) => r.removal === "removing" || r.removal === "removed",
  ).length;
  const refused = results.filter((r) => r.removal === "refused");
  const unsupported = results.some((r) => r.removal === "not_supported");
  const changed = results.filter((r) => !r.recorded).length;
  return [
    removing > 0
      ? `${removing} ${removing === 1 ? "worktree is" : "worktrees are"} being saved to a local git ref, then removed.`
      : "",
    refused.length > 0
      ? `${refused.length} not removed: ${refused[0].removalDetail || refused[0].removalReason}${refused.length > 1 ? " (and others; they are proposed again)" : ""}.`
      : "",
    unsupported
      ? "This machine's Reliant is too old to remove approved worktrees; update it, then approve again."
      : "",
    changed > 0
      ? `${changed} changed since you saw ${changed === 1 ? "it" : "them"} and will be proposed again.`
      : "",
  ]
    .filter(Boolean)
    .join(" ");
}

export function WorktreeReclaimAction({
  item,
  label,
}: {
  item: InboxWorktreeReclaim;
  label: string;
}) {
  const [open, setOpen] = useState(false);
  // Frozen at open: a refetch while the person reads cannot change what a
  // click on Submit decides about.
  const [shown, setShown] = useState<WorktreeReclaimProposal[]>([]);
  const [choices, setChoices] = useState<Record<string, Choice>>({});
  const decide = useDecideWorktreeReclaim();

  const openDialog = () => {
    setShown(undecided(item));
    setChoices({});
    setOpen(true);
  };
  const set = (key: string, c: Choice) =>
    setChoices((prev) => ({ ...prev, [key]: prev[key] === c ? "" : c }));
  const setAll = (c: Choice) =>
    setChoices(Object.fromEntries(shown.map((p) => [p.key, c])));
  const chosen = shown.filter((p) => choices[p.key]);
  const approving = chosen.filter((p) => choices[p.key] === "approve");

  const submit = () => {
    decide.mutate(
      {
        daemonId: item.daemonId,
        decisions: chosen.map((p) => ({
          key: p.key,
          fingerprint: p.fingerprint,
          approve: choices[p.key] === "approve",
        })),
      },
      {
        onSuccess: (response) => {
          const recorded = response.results.filter((r) => r.recorded).length;
          toast.success(
            `${recorded} ${recorded === 1 ? "decision" : "decisions"} recorded`,
            { description: removalSummary(response.results) },
          );
          setOpen(false);
        },
        onError: (error) =>
          toast.error("Could not send the decisions", {
            description: error instanceof Error ? error.message : String(error),
          }),
      },
    );
  };

  const count = undecided(item).length;
  return (
    <>
      <Button
        size="sm"
        variant="outline"
        onClick={openDialog}
        disabled={!item.online || count === 0}
        title={!item.online ? `${label} is offline` : undefined}
      >
        Review
      </Button>
      <Modal
        open={open}
        onClose={() => !decide.isPending && setOpen(false)}
        title={`Worktrees on ${label}`}
        size="lg"
      >
        <div className="flex flex-col gap-4">
          <p className="text-pretty text-sm text-muted-foreground">
            These worktrees have been idle for days and hold work that exists
            only in them. Nothing is removed unless you approve it. An approved
            worktree is first saved, with its uncommitted, staged and untracked
            files and unpushed commits, to a local git ref on that machine;{" "}
            <code className="font-mono">reliant worktree restore</code> brings
            it back. Nothing is pushed and no branch is deleted. Keep stops a
            worktree being proposed until it changes.
          </p>
          <div className="flex justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setAll("decline")}
              disabled={decide.isPending}
            >
              Keep all
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setAll("approve")}
              disabled={decide.isPending}
            >
              Remove all
            </Button>
          </div>
          <ul
            className="flex max-h-[28rem] flex-col gap-2 overflow-y-auto"
            aria-label="Worktrees proposed for removal"
          >
            {shown.map((p) => (
              <ProposalRow
                key={p.key}
                p={p}
                choice={choices[p.key] ?? ""}
                onChoose={(c) => set(p.key, c)}
                disabled={decide.isPending}
              />
            ))}
          </ul>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setOpen(false)}
              disabled={decide.isPending}
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={submit}
              disabled={decide.isPending || chosen.length === 0}
            >
              {decide.isPending
                ? "Sending…"
                : `Submit ${chosen.length} ${chosen.length === 1 ? "decision" : "decisions"}${approving.length > 0 ? ` (${approving.length} to remove)` : ""}`}
            </Button>
          </div>
        </div>
      </Modal>
    </>
  );
}

function ProposalRow({
  p,
  choice,
  onChoose,
  disabled,
}: {
  p: WorktreeReclaimProposal;
  choice: Choice;
  onChoose: (c: Choice) => void;
  disabled: boolean;
}) {
  const idle = idleFor(p.idleSeconds);
  return (
    <li>
      <CardInset padding="sm" className="space-y-1">
        <div className="flex items-baseline justify-between gap-3">
          <span
            className="min-w-0 truncate font-mono text-xs text-foreground"
            title={p.path}
          >
            {p.path}
          </span>
          <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
            {p.sizeBytes > 0n ? formatBytes(p.sizeBytes) : "size unknown"}
          </span>
        </div>
        <p className="text-xs text-muted-foreground">
          <span className="text-foreground/80">
            {reclaimReasonLabel(p.reason)}
          </span>
          {p.branch ? ` · ${p.branch}` : " · detached HEAD"}
          {idle ? ` · idle ${idle}` : ""}
          {p.owner === "unregistered" ? " · not a Reliant workspace" : ""}
        </p>
        {p.note && (
          <p className="text-pretty text-xs text-foreground/90">
            <span className="font-medium">Reviewer:</span> {p.note}
          </p>
        )}
        {p.workSummary && (
          <pre className="max-h-24 overflow-auto whitespace-pre-wrap rounded-sm bg-background px-2 py-1 font-mono text-2xs text-muted-foreground">
            {p.workSummary}
          </pre>
        )}
        <div
          className="flex justify-end gap-2"
          role="group"
          aria-label={`Decision for ${p.path}`}
        >
          <ChoiceButton
            active={choice === "decline"}
            onClick={() => onChoose("decline")}
            disabled={disabled}
          >
            Keep
          </ChoiceButton>
          <ChoiceButton
            active={choice === "approve"}
            onClick={() => onChoose("approve")}
            disabled={disabled}
            destructive
          >
            Remove
          </ChoiceButton>
        </div>
      </CardInset>
    </li>
  );
}

function ChoiceButton({
  active,
  onClick,
  disabled,
  destructive,
  children,
}: {
  active: boolean;
  onClick: () => void;
  disabled: boolean;
  destructive?: boolean;
  children: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "rounded-sm border px-2 py-0.5 text-xs focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50",
        active
          ? destructive
            ? "border-destructive bg-destructive text-destructive-foreground"
            : "border-primary bg-primary text-primary-foreground"
          : "border-border text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}
