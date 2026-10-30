// Copyright (c) 2025 Reliant Labs

/**
 * One Inbox row (WORKFLOW_UI.md §8.2). The SECTION a row sits in already says
 * what kind of item it is and what to do ("Questions · 3"), so the row does not
 * repeat it: it is the run's (or automation's) name as a link, a short detail
 * (the tool and its arguments, the question, the failure), the project when the
 * Inbox spans projects, how long it has waited, and the inline action.
 *
 * A row is one line by default. Anything taller — an answer form, a failure's
 * full reason — opens on demand, so ten waiting items fit on one screen.
 *
 * The actions reuse the chat's own pieces, through their store-free cores:
 * ApprovalActions for approve/deny, QuestionPrompt + answerQuestion for an
 * answer, and the shared resume (useResumeDaemon + formatResumeError) for
 * "Wake <machine>". The row never reads the open chat's store, because the
 * Inbox holds items from every chat at once.
 *
 * On the mobile surface (`/m/inbox`) the same rows render with touch-sized
 * controls (the `pointer: coarse` variants below), and every link to a run
 * opens it as the chat it is (`/m/chats/$chatId`): the desktop Runs area does
 * not exist on a phone. Destinations that are desktop-only (editing an
 * automation, a workflow's setup) say so instead of linking somewhere a phone
 * would be redirected away from.
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
import { AlertTriangle, CalendarX, CheckCircle2, ChevronDown, HardDrive, HelpCircle, MonitorPause, PauseCircle, Settings2, Shield, X } from "lucide-react";
import { toast } from "sonner";

import { InboxItemKind, type InboxItem as InboxItemData, type InboxStorage as InboxStorageData } from "@/gen/reliant/v1/inbox_pb";
import { useApprove, useDeny } from "@/hooks/approval-service-hooks_gen";
import { answerQuestion } from "@/hooks/question-queries";
import { inboxServiceKeys } from "@/hooks/inbox-service-hooks_gen";
import { removeInboxItems } from "@/hooks/inbox-queries";
import { useResumeDaemon } from "@/hooks/useOnboardingQueries";
import { useDeleteTrigger, useSetTriggerEnabled, useTriggerById } from "@/hooks/trigger-queries";
import { TriggerHealthStatus } from "@/gen/reliant/v1/trigger_pb";
import ConfirmationDialog from "../forge-ui/confirmation_dialog";
import { automationHref, automationRedirectPath, workflowPageHref } from "@/lib/workflowsArea";
import { openAutomationDialog } from "../Automations/dialog/openAutomationDialog";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { isQuotaResumeError, resumeErrorMessage } from "@/lib/daemon-resume";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { relativeTime as compactAge } from "../Mobile/relativeTime";
import { cn } from "@/lib/utils";
import { useSurface } from "@/lib/surfaceContext";
import { CardInset } from "../forge-ui/card";
import { ApprovalActions } from "../Chat/ApprovalActions";
import { QuestionPrompt, type QuestionAnswer } from "../Chat/QuestionPrompt";
import { askUserQuestionItems } from "../Chat/askUserUtils";
import { formatBytes } from "@/lib/formatBytes";
import { consumerSummary, heavyRefused, pressuredVolume, reclaimSummary, volumeUsage } from "@/lib/machineDisk";
import { StorageAction, totalHeldBytes } from "./StorageAction";
import { RecoveryAction } from "./RecoveryAction";

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

/** "Failed 3 times in a row": one Inbox item counts the whole failure streak. */
export function failedTimesInARow(failures: number): string {
  return `Failed ${failures} times in a row.`;
}

/** A server error string ("daemon x is offline") shown on its own reads as a sentence. */
function sentenceCase(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

interface InboxItemProps {
  item: InboxItemData;
  /** Show the project beside the name: the Inbox is spanning projects. */
  showProject: boolean;
  /** Hide this row; the page owns the mutation and the Undo toast. */
  onDismiss: (item: InboxItemData) => void;
}

export function InboxItem({ item, showProject, onDismiss }: InboxItemProps) {
  const [handled, setHandled] = useState(false);
  const [expanded, setExpanded] = useState(false);

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

  const detail = rowDetail(item);
  const expandable = hasExpansion(item);
  // "2h" in the row (a dense list wants a narrow, fixed column); the long
  // form and the exact time are the tooltip and the accessible name.
  const waited = compactAge(item.waitingSince);
  const waitedLong = formatRelativeTime(item.waitingSince);

  return (
    <li
      className="group/row relative px-3 py-2 transition-colors hover:bg-muted/40 focus-within:bg-muted/40"
      data-testid={`inbox-item-${item.itemId}`}
    >
      {/* One line on a desktop. On a narrow screen the controls wrap to a
          second line rather than squeezing the name to nothing: the subject
          block holds a minimum width, and the controls group moves as one. */}
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <div className="flex min-w-0 flex-[1_1_16rem] items-center gap-2.5">
          <span className="shrink-0 text-muted-foreground">
            <KindIcon kind={item.kind} />
          </span>
          <div className="flex min-w-0 flex-1 items-baseline gap-2">
            <SubjectLink item={item} />
            {detail && (
              <span className="min-w-0 truncate text-xs text-muted-foreground" title={detail.title}>
                {detail.node}
              </span>
            )}
          </div>
        </div>
        <div className="ml-auto flex min-h-7 shrink-0 items-center gap-2.5">
          {showProject && item.projectName && (
            <span className="max-w-36 truncate text-xs text-muted-foreground" title={item.projectName}>
              {item.projectName}
            </span>
          )}
          {handled ? (
            <span className="text-xs text-muted-foreground" role="status">
              Already handled
            </span>
          ) : (
            <InlineAction
              item={item}
              expanded={expanded}
              onToggle={expandable ? () => setExpanded((open) => !open) : undefined}
              onRaceOrError={onRaceOrError}
            />
          )}
          <time
            dateTime={item.waitingSince}
            title={`Waiting since ${waitedLong} · ${formatAbsoluteTime(item.waitingSince)}`}
            className="w-8 whitespace-nowrap text-right text-xs tabular-nums text-muted-foreground"
          >
            <span aria-hidden="true">{waited}</span>
            <span className="sr-only">waiting since {waitedLong}</span>
          </time>
          {!handled && (
            <button
              type="button"
              onClick={() => onDismiss(item)}
              aria-label={`Dismiss ${subjectName(item)}`}
              title="Dismiss"
              className={cn(
                "inline-flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition",
                // A finger needs 44px; explicit px because rem renders at 87.5% here.
                "[@media(pointer:coarse)]:h-[44px] [@media(pointer:coarse)]:w-[44px]",
                "hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
                // Hidden until the row is hovered or anything in it has focus;
                // always shown where there is no hover (touch).
                "opacity-0 group-hover/row:opacity-100 group-focus-within/row:opacity-100 focus-visible:opacity-100",
                "[@media(hover:none)]:opacity-100",
              )}
            >
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          )}
        </div>
      </div>
      {item.payload.case === "recovery" && !handled && (
        <div className="mt-2 pl-7 max-sm:pl-0">
          <RecoveryAction key={item.payload.value.recoveryId} recovery={item.payload.value} />
        </div>
      )}
      {expanded && !handled && (
        <div className="mt-2 pl-7 max-sm:pl-0">
          <Expansion item={item} onRaceOrError={onRaceOrError} />
        </div>
      )}
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
      return <MonitorPause className={cn(className, "text-warning-ink")} aria-label="Waiting for machine" />;
    case InboxItemKind.AUTOMATION_FAILING:
      return <AlertTriangle className={cn(className, "text-destructive-ink")} aria-label="Automation failing" />;
    case InboxItemKind.AUTOMATION_LAUNCH_FAILED:
      return <CalendarX className={cn(className, "text-destructive")} aria-label="Automation could not start" />;
    case InboxItemKind.AUTOMATION_NEEDS_SETUP:
      return <Settings2 className={cn(className, "text-warning-ink")} aria-label="Automation needs setup" />;
    case InboxItemKind.RUN_FINISHED:
      return <CheckCircle2 className={cn(className, "text-success-ink")} aria-label="Run finished" />;
    case InboxItemKind.AUTOMATION_RUN_PAUSED:
      return <PauseCircle className={cn(className, "text-warning-ink")} aria-label="Automation run paused" />;
    case InboxItemKind.RECOVERY:
      return <HardDrive className={className} aria-label="Saved workspace recovery" />;
    case InboxItemKind.STORAGE:
      return <HardDrive className={cn(className, "text-warning-ink")} aria-label="Storage" />;
    default:
      return <CheckCircle2 className={className} aria-hidden="true" />;
  }
}

/** The run's (or the automation's) display name. */
export function subjectName(item: InboxItemData): string {
  if (item.payload.case === "recovery") return item.payload.value.sourceWorktreeName || "Saved workspace recovery";
  if (item.payload.case === "storage") {
    return `${machineName(item.payload.value.daemonName, item.payload.value.daemonId)} storage`;
  }
  return item.chatTitle || item.triggerName || "Untitled run";
}

const subjectClass =
  "min-w-0 max-w-[60%] shrink-0 truncate text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 rounded-sm [@media(pointer:coarse)]:py-3";

/**
 * A link to a run. On the mobile surface a run opens as the chat it is: the
 * desktop Runs area is not on a phone, and a link into it is redirected to
 * the chat list, losing the run.
 */
function RunLink({
  chatId,
  className,
  title,
  children,
}: {
  chatId: string;
  className: string;
  title?: string;
  children: ReactNode;
}) {
  const mobile = useSurface() === "mobile";
  if (mobile) {
    return (
      <Link to="/m/chats/$chatId" params={{ chatId }} className={className} title={title}>
        {children}
      </Link>
    );
  }
  return (
    <Link to="/workflows/runs/$runId" params={{ runId: chatId }} className={className} title={title}>
      {children}
    </Link>
  );
}

/** Where a phone cannot follow (automation editing, workflow setup): say so rather than link. */
function OnDesktopNote({ children }: { children: ReactNode }) {
  return <span className="shrink-0 text-xs text-muted-foreground">{children}</span>;
}

/**
 * The name links to what the item is about: the run for anything that has one,
 * otherwise the automation.
 */
function SubjectLink({ item }: { item: InboxItemData }) {
  const name = subjectName(item);
  if (item.chatId) {
    return (
      <RunLink chatId={item.chatId} className={subjectClass} title={name}>
        {name}
      </RunLink>
    );
  }
  if (item.triggerId) return <AutomationSubjectLink triggerId={item.triggerId} name={name} />;
  return <span className={subjectClass}>{name}</span>;
}

/**
 * An automation item names the trigger, and opens it on its workflow's page
 * (where every trigger is configured). The trigger is read from the list
 * cache for its workflow and project; until it has loaded, the link resolves
 * through the automation redirect, which looks it up.
 */
function AutomationSubjectLink({ triggerId, name }: { triggerId: string; name: string }) {
  const trigger = useTriggerById(triggerId);
  // Automations are configured on desktop only; on a phone the name is a label.
  const mobile = useSurface() === "mobile";
  if (mobile) return <span className={subjectClass} title={name}>{name}</span>;
  return (
    <a href={trigger ? automationHref(trigger) : automationRedirectPath(triggerId)} className={subjectClass} title={name}>
      {name}
    </a>
  );
}

/** "Open settings" for a declared trigger that can't start; a hand-made one opens on its workflow's page. */
function LaunchFailedAction({ triggerId }: { triggerId: string }) {
  const trigger = useTriggerById(triggerId);
  const mobile = useSurface() === "mobile";
  if (mobile) return <OnDesktopNote>Fix on desktop</OnDesktopNote>;
  if (trigger?.workflowTrigger) {
    return (
      <>
        <button
          type="button"
          className={actionButtonClass}
          onClick={() => openAutomationDialog({ workflowRef: trigger.workflow, projectId: trigger.projectId, mode: "settings", showOpenWorkflow: true })}
        >
          Open settings
        </button>
      </>
    );
  }
  return (
    <a href={trigger ? automationHref(trigger) : automationRedirectPath(triggerId)} className={actionButtonClass}>
      Open trigger
    </a>
  );
}

/** The one-line detail after the name, and its full text for the tooltip. */
function rowDetail(item: InboxItemData): { node: ReactNode; title: string } | null {
  const { payload } = item;
  switch (payload.case) {
    case "approval": {
      const title = payload.value.title;
      return title ? { node: title, title } : null;
    }
    case "question": {
      const prompt = payload.value.prompt;
      // An async question (ask_user without blocking: true): the agent kept working,
      // so it waits on the user without blocking a run.
      if (payload.value.async) {
        const title = prompt ? `${prompt} (not blocking: the agent kept working)` : "Not blocking: the agent kept working";
        return {
          node: (
            <>
              <span className="text-muted-foreground">Not blocking · </span>
              {prompt}
            </>
          ),
          title,
        };
      }
      return prompt ? { node: prompt, title: prompt } : null;
    }
    case "waitingForMachine": {
      const text = `${machineName(payload.value.daemonName, payload.value.daemonId)} is asleep`;
      return { node: text, title: text };
    }
    case "automationFailing": {
      const health = payload.value.health;
      const failures = health?.consecutiveFailures ?? 0;
      const text = [failures > 1 ? `${failures} in a row` : "", health?.lastFailureDetail ?? ""]
        .filter(Boolean)
        .join(" · ");
      return text ? { node: text, title: text } : null;
    }
    case "recovery": {
      const text = `${payload.value.checkoutCount} repositories · ${payload.value.phase}${payload.value.lastError ? ` · Held: ${payload.value.lastError}` : ""}`;
      return { node: text, title: text };
    }
    case "storage": {
      const text = storageDetail(payload.value);
      return text ? { node: text, title: text } : null;
    }
    case "automationNeedsSetup": {
      const text = payload.value.issues.map((issue) => sentenceCase(issue.message)).join(" · ");
      return text ? { node: text, title: text } : null;
    }
    case "automationLaunchFailed": {
      const { reason, consecutiveFailures } = payload.value;
      const text = [consecutiveFailures > 1 ? `${consecutiveFailures} in a row` : "", reason ? sentenceCase(reason) : ""]
        .filter(Boolean)
        .join(" · ");
      return text ? { node: text, title: text } : null;
    }
    case "automationRunPaused": {
      // Why it paused; while it waits it holds its automation's slot.
      const reason = payload.value.reason;
      const text = reason ? `Paused: ${reason}` : "Paused";
      return { node: text, title: `${text} — later runs of this automation wait until it resumes or ends.` };
    }
    default:
      return null;
  }
}

/**
 * "build caches & docker: 38 of 40 GB used — Docker 18 GB · Build caches 15 GB
 * · builds paused · freed 3.2 GB automatically (Go build cache 3.2 GB) · 3
 * workspaces kept (8.4 GB)". Names the volume that is full: on a cloud
 * machine it is not the home volume, and "12 GB free of 157 GB" there said
 * nothing about why every build was failing.
 */
export function storageDetail(storage: InboxStorageData): string {
  const parts: string[] = [];
  const full = pressuredVolume(storage);
  if (full) {
    const using = consumerSummary(full, 2);
    parts.push(using ? `${volumeUsage(full)} — ${using}` : volumeUsage(full));
    if (heavyRefused(full)) parts.push("builds paused");
    const reclaimed = reclaimSummary(full);
    if (reclaimed) parts.push(reclaimed.charAt(0).toLowerCase() + reclaimed.slice(1));
  }
  if (storage.held.length > 0) {
    const size = totalHeldBytes(storage.held);
    const noun = storage.held.length === 1 ? "workspace" : "workspaces";
    parts.push(`${storage.held.length} ${noun} kept${size > 0 ? ` (${formatBytes(size)})` : ""}`);
  }
  if (!storage.online) parts.push("offline");
  return parts.join(" · ");
}

/** A row opens downward only when its action needs more than a line. */
function hasExpansion(item: InboxItemData): boolean {
  return item.payload.case === "question" && askUserQuestionItems(item.payload.value.metadata) !== null;
}

const actionButtonClass =
  "inline-flex h-7 shrink-0 items-center gap-1 rounded-md border border-border px-2.5 text-xs font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:opacity-60 [@media(pointer:coarse)]:h-[44px] [@media(pointer:coarse)]:px-3.5 [@media(pointer:coarse)]:text-sm";

interface InlineActionProps {
  item: InboxItemData;
  expanded: boolean;
  onToggle?: () => void;
  onRaceOrError: (verb: string) => (error: unknown) => void;
}

function InlineAction({ item, expanded, onToggle, onRaceOrError }: InlineActionProps) {
  const { payload } = item;
  const mobile = useSurface() === "mobile";
  switch (payload.case) {
    case "approval":
      return <ApprovalRowAction approvalId={payload.value.approvalId} itemId={item.itemId} onFailure={onRaceOrError} />;
    case "question":
      if (onToggle) {
        return (
          <button type="button" className={actionButtonClass} aria-expanded={expanded} onClick={onToggle}>
            Answer
            <ChevronDown className={cn("h-3 w-3 transition-transform", expanded && "rotate-180")} aria-hidden="true" />
          </button>
        );
      }
      // Not an ask_user form (e.g. a workflow step question): its own UI in
      // the run answers it.
      return item.chatId ? (
        <RunLink chatId={item.chatId} className={actionButtonClass}>
          Open
        </RunLink>
      ) : null;
    case "waitingForMachine":
      return <WakeMachineAction daemonId={payload.value.daemonId} daemonName={payload.value.daemonName} />;
    case "automationFailing":
      return (
        <FailingAutomationAction
          triggerId={item.triggerId}
          triggerName={item.triggerName}
          lastRunChatId={payload.value.lastRunChatId}
          broken={payload.value.health?.status === TriggerHealthStatus.BROKEN}
        />
      );
    case "automationLaunchFailed":
      return <LaunchFailedAction triggerId={item.triggerId} />;
    case "automationNeedsSetup":
      // The workflow page's automation is where "Finish setup" renders; the
      // item carries everything the link needs, so it needs no list lookup.
      // That page is desktop-only.
      if (mobile) return <OnDesktopNote>Finish on desktop</OnDesktopNote>;
      return (
        <a href={workflowPageHref(item.workflowName, item.projectId, { automation: true })} className={actionButtonClass}>
          Finish setup
        </a>
      );
    case "recovery":
      // The full-width action lives below the row, not inside its nonwrapping controls.
      return null;
    case "storage":
      return (
        <StorageAction storage={payload.value} label={machineName(payload.value.daemonName, payload.value.daemonId)} />
      );
    case "runFinished":
    case "automationRunPaused":
      return item.chatId ? (
        <RunLink chatId={item.chatId} className={actionButtonClass}>
          Open
        </RunLink>
      ) : null;
    default:
      return null;
  }
}

function Expansion({
  item,
  onRaceOrError,
}: {
  item: InboxItemData;
  onRaceOrError: (verb: string) => (error: unknown) => void;
}) {
  if (item.payload.case !== "question") return null;
  return (
    <QuestionAnswerForm
      chatId={item.chatId}
      itemId={item.itemId}
      questionId={item.payload.value.questionId}
      metadata={item.payload.value.metadata}
      onFailure={onRaceOrError("send the answer")}
    />
  );
}

function ApprovalRowAction({
  approvalId,
  itemId,
  onFailure,
}: {
  approvalId: string;
  itemId: string;
  onFailure: (verb: string) => (error: unknown) => void;
}) {
  const [busy, setBusy] = useState(false);
  const approve = useApprove();
  const deny = useDeny();
  const act = async (verb: "approve" | "deny") => {
    setBusy(true);
    try {
      if (verb === "approve") await approve.mutateAsync({ requestId: approvalId });
      else await deny.mutateAsync({ requestId: approvalId });
      removeInboxItems([itemId]);
    } catch (error) {
      onFailure(verb)(error);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="shrink-0">
      <ApprovalActions
        compact
        approveLabel="Approve"
        denyLabel="Deny"
        disabled={busy}
        onApprove={() => void act("approve")}
        onDeny={() => void act("deny")}
      />
    </div>
  );
}

function QuestionAnswerForm({
  chatId,
  itemId,
  questionId,
  metadata,
  onFailure,
}: {
  chatId: string;
  itemId: string;
  questionId: string;
  metadata?: string;
  onFailure: (error: unknown) => void;
}) {
  const questions = useMemo(() => askUserQuestionItems(metadata), [metadata]);
  // QuestionPrompt locks itself after submit; a remount (new key) re-arms it
  // if the reply failed for a reason worth retrying.
  const [attempt, setAttempt] = useState(0);
  if (!questions) return null;

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
      <QuestionPrompt key={attempt} inline questions={questions} onSubmit={(answers) => void onSubmit(answers)} />
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
  const goToBilling = useGoToBilling("inbox");
  // The error itself, not its message: its reason decides whether Billing is
  // the fix (lib/daemon-resume); the copy is shared.
  const [error, setError] = useState<unknown>(null);
  const resume = useResumeDaemon({
    onError: (err) => setError(err ?? new Error("Failed to resume environment")),
  });
  const busy = resume.isPending && resume.variables === daemonId;
  const name = machineName(daemonName, daemonId);

  if (!daemonId) return null;

  return (
    <span className="flex shrink-0 items-center gap-2">
      {error != null && (
        <span className="max-w-64 truncate text-xs text-destructive-ink" role="alert" title={resumeErrorMessage(error)}>
          {resumeErrorMessage(error)}
          {isQuotaResumeError(error) ? (
            <button type="button" onClick={goToBilling} className="ml-1.5 font-medium underline-offset-2 hover:underline">
              Billing
            </button>
          ) : null}
        </span>
      )}
      <button
        type="button"
        disabled={busy}
        onClick={() => {
          setError(null);
          resume.mutate(daemonId);
        }}
        className={actionButtonClass}
      >
        {busy ? "Waking…" : (label ?? `Wake ${name}`)}
      </button>
    </span>
  );
}

/**
 * Pause for a failing automation, after Open last run. A BROKEN one (its
 * workflow changed so that it cannot fire: gone, unparseable, the trigger
 * renamed) has no run worth opening; it gets Remove beside Pause, since
 * fixing the workflow file is the only other way out.
 */
function FailingAutomationAction({
  triggerId,
  triggerName,
  lastRunChatId,
  broken,
}: {
  triggerId: string;
  triggerName: string;
  lastRunChatId?: string;
  broken: boolean;
}) {
  const setEnabled = useSetTriggerEnabled();
  const remove = useDeleteTrigger();
  const queryClient = useQueryClient();
  const [confirmingRemove, setConfirmingRemove] = useState(false);
  const name = triggerName || "this automation";
  return (
    <span className="flex shrink-0 items-center gap-1.5">
      {broken && (
        <>
          <button type="button" className={actionButtonClass} disabled={remove.isPending} onClick={() => setConfirmingRemove(true)}>
            Remove
          </button>
          <ConfirmationDialog
            open={confirmingRemove}
            title={`Remove ${name}?`}
            description="It stops listening for this trigger. The workflow and the chats it already started are kept."
            confirmLabel="Remove trigger"
            loading={remove.isPending}
            onConfirm={() =>
              remove.mutate(
                { id: triggerId },
                {
                  onSuccess: () => {
                    setConfirmingRemove(false);
                    toast.success(`Removed ${name}`);
                    void queryClient.invalidateQueries({ queryKey: inboxServiceKeys.all });
                  },
                  onError: (error) => {
                    setConfirmingRemove(false);
                    toast.error(`Could not remove ${name}`, { description: errorText(error) });
                  },
                },
              )
            }
            onCancel={() => setConfirmingRemove(false)}
          />
        </>
      )}
      {!broken && lastRunChatId && (
        <RunLink chatId={lastRunChatId} className={actionButtonClass}>
          Open last run
        </RunLink>
      )}
      <button
        type="button"
        className={actionButtonClass}
        disabled={setEnabled.isPending}
        onClick={() =>
          setEnabled.mutate(
            { id: triggerId, enabled: false },
            {
              onSuccess: () => {
                toast.success("Automation paused");
                // A paused automation is no longer listed as failing.
                void queryClient.invalidateQueries({ queryKey: inboxServiceKeys.all });
              },
              onError: (error) => toast.error("Could not pause the automation", { description: errorText(error) }),
            },
          )
        }
      >
        Pause
      </button>
    </span>
  );
}
