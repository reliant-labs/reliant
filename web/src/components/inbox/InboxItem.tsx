// Copyright (c) 2025 Reliant Labs

/**
 * One Inbox row (WORKFLOW_UI.md §8.2): a kind icon, a one-line title that
 * links to the run, a context line (project · workflow · how long it has
 * waited), then the item's inline action.
 *
 * The actions reuse the chat's own pieces, through their store-free cores:
 * ApprovalActions for approve/deny, QuestionPrompt + answerQuestion for an
 * answer, and the shared resume (useResumeDaemon + formatResumeError) for
 * "Wake <machine>". The row never reads the open chat's store, because the
 * Inbox holds items from every chat at once.
 *
 * When an action loses the race to another surface — approved in the chat
 * view, or on another device — the server says so with FAILED_PRECONDITION
 * or NOT_FOUND. That is not an error: the row shows "Already handled" for two
 * seconds and then leaves (§8.3).
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { AlertTriangle, CalendarX, CheckCircle2, HelpCircle, MonitorPause, Shield, X } from "lucide-react";
import { toast } from "sonner";

import { api } from "@/api/client";
import { InboxItemKind, type InboxItem as InboxItemData } from "@/gen/reliant/v1/inbox_pb";
import { ApprovalType } from "@/gen/reliant/v1/approval_pb";
import { answerQuestion } from "@/hooks/approval-queries";
import { inboxKeys, removeInboxItems, useDismissInboxItem } from "@/hooks/inbox-queries";
import { useResumeDaemon } from "@/hooks/useOnboardingQueries";
import { useSetTriggerEnabled } from "@/hooks/trigger-queries";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { isQuotaResumeError, resumeErrorMessage } from "@/lib/daemon-resume";
import { formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import { CardInset } from "../forge-ui/card";
import { ApprovalActions } from "../Chat/ApprovalActions";
import { QuestionPrompt, type QuestionAnswer } from "../Chat/QuestionPrompt";
import { askUserQuestionItems } from "../Chat/askUserUtils";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";

/** How long "Already handled" shows before the row leaves. */
export const ALREADY_HANDLED_MS = 2000;

/** True for the kinds that block a live run (and count in the badge). */
export function isBlockingKind(kind: InboxItemKind): boolean {
  return (
    kind === InboxItemKind.APPROVAL ||
    kind === InboxItemKind.QUESTION ||
    kind === InboxItemKind.WAITING_FOR_MACHINE
  );
}

/** Failure kinds are the only dismissable ones; the rest clear when resolved. */
export function isDismissableKind(kind: InboxItemKind): boolean {
  return kind === InboxItemKind.AUTOMATION_FAILING || kind === InboxItemKind.AUTOMATION_LAUNCH_FAILED;
}

/** The server reports an item someone else already resolved this way. */
function isAlreadyResolvedError(error: unknown): boolean {
  if (!(error instanceof ConnectError)) return false;
  return error.code === Code.FailedPrecondition || error.code === Code.NotFound;
}

function errorText(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  return error instanceof Error ? error.message : String(error);
}

/** The machine's name for copy; a hostname, else a short id. */
export function machineName(daemonName: string, daemonId: string): string {
  return daemonName || (daemonId ? `machine ${daemonId.slice(0, 8)}` : "this machine");
}

interface InboxItemProps {
  item: InboxItemData;
  /** Inside a run group the run's name is already the group's heading. */
  grouped?: boolean;
}

export function InboxItem({ item, grouped }: InboxItemProps) {
  const [handled, setHandled] = useState(false);

  // "Already handled": show it, then take the row out (§8.3).
  useEffect(() => {
    if (!handled) return;
    const timer = window.setTimeout(() => removeInboxItems([item.itemId]), ALREADY_HANDLED_MS);
    return () => window.clearTimeout(timer);
  }, [handled, item.itemId]);

  const onRaceOrError = (verb: string) => (error: unknown) => {
    if (isAlreadyResolvedError(error)) {
      setHandled(true);
      return;
    }
    toast.error(`Could not ${verb}`, { description: errorText(error) });
  };

  const waitedFor = formatRelativeTime(item.waitingSince);
  const context = [
    item.projectName,
    item.workflowName ? getWorkflowDisplayName(item.workflowName, true) : undefined,
    waitedFor ? `waiting since ${waitedFor}` : undefined,
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <li className="px-4 py-3" data-testid={`inbox-item-${item.itemId}`}>
      <div className="flex items-start gap-3">
        <span className="mt-0.5 shrink-0 text-muted-foreground">
          <KindIcon kind={item.kind} />
        </span>
        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <ItemTitle item={item} grouped={grouped} />
              {context && <p className="mt-0.5 truncate text-xs text-muted-foreground">{context}</p>}
            </div>
            {isDismissableKind(item.kind) && !handled && <DismissButton itemId={item.itemId} />}
          </div>
          {handled ? (
            <p className="text-sm text-muted-foreground" role="status">
              Already handled
            </p>
          ) : (
            <ItemAction item={item} onRaceOrError={onRaceOrError} />
          )}
        </div>
      </div>
    </li>
  );
}

function KindIcon({ kind }: { kind: InboxItemKind }) {
  const className = "h-4 w-4";
  switch (kind) {
    case InboxItemKind.APPROVAL:
      return <Shield className={className} aria-label="Approval" />;
    case InboxItemKind.QUESTION:
      return <HelpCircle className={className} aria-label="Question" />;
    case InboxItemKind.WAITING_FOR_MACHINE:
      return <MonitorPause className={cn(className, "text-warning")} aria-label="Waiting for machine" />;
    case InboxItemKind.AUTOMATION_FAILING:
      return <AlertTriangle className={cn(className, "text-destructive")} aria-label="Automation failing" />;
    case InboxItemKind.AUTOMATION_LAUNCH_FAILED:
      return <CalendarX className={cn(className, "text-destructive")} aria-label="Automation launch failed" />;
    default:
      return <CheckCircle2 className={className} aria-hidden="true" />;
  }
}

/** The run's (or the automation's) display name. */
function subjectName(item: InboxItemData): string {
  return item.chatTitle || item.triggerName || "Untitled run";
}

/** The one-line title; the run's name links to run detail (§8.2). */
function ItemTitle({ item, grouped }: { item: InboxItemData; grouped?: boolean }) {
  const name = subjectName(item);
  const lead = titleLead(item);
  const runLink = item.chatId ? (
    <Link
      to="/runs/$runId"
      params={{ runId: item.chatId }}
      className="font-semibold text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
    >
      {name}
    </Link>
  ) : item.triggerId ? (
    <Link
      to="/automations/$triggerId"
      params={{ triggerId: item.triggerId }}
      className="font-semibold text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
    >
      {name}
    </Link>
  ) : (
    <span className="font-semibold text-foreground">{name}</span>
  );

  return (
    <p className="text-sm text-foreground">
      {lead}
      {/* Inside a run group the group's heading names and links the run. */}
      {(!grouped || !item.chatId) && (
        <>
          {" in "}
          {runLink}
        </>
      )}
    </p>
  );
}

function titleLead(item: InboxItemData): ReactNode {
  const { payload } = item;
  switch (payload.case) {
    case "approval": {
      const approval = payload.value;
      if (approval.approvalType === ApprovalType.TOOL && approval.toolName) {
        return (
          <>
            Approve <code className="rounded bg-background px-1 font-mono text-xs">{approval.toolName}</code>
          </>
        );
      }
      return <>Approve {approval.title ? `“${approval.title}”` : "a step"}</>;
    }
    case "question":
      return <>Answer a question</>;
    case "waitingForMachine":
      return <>{machineName(payload.value.daemonName, payload.value.daemonId)} is needed</>;
    case "automationFailing":
      return <>Automation failing</>;
    case "automationLaunchFailed":
      return <>Automation could not start</>;
    default:
      return <>Waiting on you</>;
  }
}

interface ItemActionProps {
  item: InboxItemData;
  onRaceOrError: (verb: string) => (error: unknown) => void;
}

function ItemAction({ item, onRaceOrError }: ItemActionProps) {
  const { payload } = item;
  switch (payload.case) {
    case "approval":
      return (
        <ApprovalRowAction
          approvalId={payload.value.approvalId}
          title={payload.value.title}
          argumentSummary={payload.value.argumentSummary}
          itemId={item.itemId}
          onFailure={onRaceOrError}
        />
      );
    case "question":
      return (
        <QuestionRowAction
          chatId={item.chatId}
          itemId={item.itemId}
          questionId={payload.value.questionId}
          prompt={payload.value.prompt}
          metadata={payload.value.metadata}
          onFailure={onRaceOrError("send the answer")}
        />
      );
    case "waitingForMachine":
      return <WakeMachineAction daemonId={payload.value.daemonId} daemonName={payload.value.daemonName} />;
    case "automationFailing":
      return (
        <FailingAutomationAction
          triggerId={item.triggerId}
          failures={payload.value.health?.consecutiveFailures ?? 0}
          detail={payload.value.health?.lastFailureDetail ?? ""}
          lastRunChatId={payload.value.lastRunChatId}
        />
      );
    case "automationLaunchFailed":
      return <LaunchFailedAction triggerId={item.triggerId} reason={payload.value.reason} />;
    default:
      return null;
  }
}

function ApprovalRowAction({
  approvalId,
  title,
  argumentSummary,
  itemId,
  onFailure,
}: {
  approvalId: string;
  title: string;
  argumentSummary: string;
  itemId: string;
  onFailure: (verb: string) => (error: unknown) => void;
}) {
  const [busy, setBusy] = useState(false);
  const act = async (verb: "approve" | "deny") => {
    setBusy(true);
    try {
      if (verb === "approve") await api.approvals.approve(approvalId, undefined);
      else await api.approvals.deny(approvalId, undefined, undefined);
      removeInboxItems([itemId]);
    } catch (error) {
      onFailure(verb)(error);
    } finally {
      setBusy(false);
    }
  };
  const summary = argumentSummary || title;
  return (
    <div className="space-y-2">
      {summary && (
        <CardInset className="truncate font-mono text-xs text-foreground" title={summary}>
          {summary}
        </CardInset>
      )}
      <ApprovalActions
        approveLabel="Approve"
        denyLabel="Deny"
        disabled={busy}
        onApprove={() => void act("approve")}
        onDeny={() => void act("deny")}
      />
    </div>
  );
}

function QuestionRowAction({
  chatId,
  itemId,
  questionId,
  prompt,
  metadata,
  onFailure,
}: {
  chatId: string;
  itemId: string;
  questionId: string;
  prompt: string;
  metadata?: string;
  onFailure: (error: unknown) => void;
}) {
  const questions = useMemo(() => askUserQuestionItems(metadata), [metadata]);
  // QuestionPrompt locks itself after submit; a remount (new key) re-arms it
  // if the reply failed for a reason worth retrying.
  const [attempt, setAttempt] = useState(0);

  if (!questions) {
    // Not an ask_user form (e.g. a workflow step question): show the prompt
    // and send the user to the run, where its own UI answers it.
    return prompt ? <p className="text-sm text-muted-foreground">{prompt}</p> : null;
  }

  const onSubmit = async (answers: { answers: QuestionAnswer[] }) => {
    try {
      await answerQuestion(chatId, questionId, answers);
      removeInboxItems([itemId]);
    } catch (error) {
      onFailure(error);
      setAttempt((n) => n + 1);
    }
  };

  return (
    <CardInset padding="none">
      <QuestionPrompt key={attempt} questions={questions} onSubmit={(answers) => void onSubmit(answers)} />
    </CardInset>
  );
}

/**
 * "Wake <machine>": the same resume ResumeDaemonPill calls, with the same copy
 * when it is refused. A quota refusal opens the global upgrade modal from the
 * hook; any other refusal shows inline with a way to Billing.
 */
export function WakeMachineAction({
  daemonId,
  daemonName,
  label,
}: {
  daemonId: string;
  daemonName: string;
  label?: string;
}) {
  const goToBilling = useGoToBilling();
  // The raw message decides whether Billing is the fix; the copy is shared.
  const [error, setError] = useState("");
  const resume = useResumeDaemon({
    onError: (err) => setError(err instanceof Error ? err.message : "Failed to resume environment"),
  });
  const busy = resume.isPending && resume.variables === daemonId;
  const name = machineName(daemonName, daemonId);

  if (!daemonId) {
    return <p className="text-sm text-muted-foreground">The machine for this run is not known.</p>;
  }

  return (
    <div className="space-y-2">
      <button
        type="button"
        disabled={busy}
        onClick={() => {
          setError("");
          resume.mutate(daemonId);
        }}
        className="inline-flex h-8 items-center rounded-md border border-border px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:opacity-60"
      >
        {busy ? "Waking…" : (label ?? `Wake ${name}`)}
      </button>
      {error && (
        <p className="text-sm text-destructive" role="alert">
          {resumeErrorMessage(new Error(error))}
          {isQuotaResumeError(error) ? (
            <button
              type="button"
              onClick={goToBilling}
              className="ml-2 font-medium underline-offset-2 hover:underline"
            >
              Billing
            </button>
          ) : null}
        </p>
      )}
    </div>
  );
}

const secondaryLinkClass =
  "inline-flex h-8 items-center rounded-md border border-border px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:opacity-60";

function FailingAutomationAction({
  triggerId,
  failures,
  detail,
  lastRunChatId,
}: {
  triggerId: string;
  failures: number;
  detail: string;
  lastRunChatId?: string;
}) {
  const setEnabled = useSetTriggerEnabled();
  const queryClient = useQueryClient();
  const description =
    failures > 1 ? `It has failed ${failures} times in a row.` : failures === 1 ? "Its last run failed." : "It is failing.";
  return (
    <div className="space-y-2">
      <p className="text-sm text-muted-foreground">
        {description}
        {detail ? ` ${detail}` : ""}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        {lastRunChatId && (
          <Link to="/runs/$runId" params={{ runId: lastRunChatId }} className={secondaryLinkClass}>
            Open last run
          </Link>
        )}
        <button
          type="button"
          className={secondaryLinkClass}
          disabled={setEnabled.isPending}
          onClick={() =>
            setEnabled.mutate(
              { id: triggerId, enabled: false },
              {
                onSuccess: () => {
                  toast.success("Automation paused");
                  // A paused automation is no longer listed as failing.
                  void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
                },
                onError: (error) => toast.error("Could not pause the automation", { description: errorText(error) }),
              },
            )
          }
        >
          Pause automation
        </button>
      </div>
    </div>
  );
}

function LaunchFailedAction({ triggerId, reason }: { triggerId: string; reason: string }) {
  return (
    <div className="space-y-2">
      {reason && <CardInset className="text-sm text-foreground">{reason}</CardInset>}
      <Link to="/automations/$triggerId" params={{ triggerId }} className={secondaryLinkClass}>
        Edit automation
      </Link>
    </div>
  );
}

function DismissButton({ itemId }: { itemId: string }) {
  const dismiss = useDismissInboxItem();
  return (
    <button
      type="button"
      onClick={() =>
        dismiss.mutate(itemId, {
          onError: (error) => toast.error("Could not dismiss", { description: errorText(error) }),
        })
      }
      disabled={dismiss.isPending}
      aria-label="Dismiss"
      className="inline-flex h-7 shrink-0 items-center gap-1 rounded-md px-2 text-xs text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
    >
      <X className="h-3.5 w-3.5" aria-hidden="true" />
      Dismiss
    </button>
  );
}
