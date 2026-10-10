/**
 * Background Work Pill — ambient summary of async spawns and running commands.
 *
 * Sits directly above the chat input, in the same "wants your attention" band
 * as QueuedMessages and the permissions panel. Async spawns are launched from
 * a tool call that scrolls away, so without a fixed surface there is no way to
 * see what is still running, or to get back to it.
 *
 * Deliberately NOT merged into ThreadTabs: those are a navigation control with
 * per-thread context meters, and ThreadTabs filters spawn-origin threads out of
 * its own visible set. This is a transient activity readout.
 *
 * A spawned agent that FAILS shows here too, with Retry. Its failure stopped
 * that agent and nothing else, so this row — not a chat-wide banner — is where
 * the user sees it. Retry asks the agent's parent to resume it (the parent was
 * already told it failed and holds the plan the agent belongs to); dismissing
 * the row is remembered on this device.
 */

import { Tooltip } from "../ui/Tooltip";
import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, Bot, ChevronDown, Terminal, HelpCircle, RotateCw, Square, X } from "lucide-react";
import { cn } from "../../lib/utils";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";
import { chatGrpc } from "../../api/chat-grpc";
import { useChatStore } from "../../store/chatStore";
import {
  useBackgroundWork,
  failedAgentRetryMessage,
  type ActiveSpawn,
  type ActiveCommand,
  type FailedSpawn,
} from "./useBackgroundWork";

interface BackgroundWorkPillProps {
  chatId?: string;
  worktreeId?: string;
  /** Focus a spawned agent's thread in the timeline. */
  onSelectThread?: (threadId: string | null) => void;
  /** Reveal a running command in the Commands tab. */
  onSelectCommand?: (processId: string) => void;
}

/** Compact elapsed time: "8s", "4m", "1h12m". */
function useElapsed(startedAt: number | null): string {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (startedAt === null) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [startedAt]);

  if (startedAt === null) return "";
  const seconds = Math.max(0, Math.floor((now - startedAt) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  return `${Math.floor(minutes / 60)}h${minutes % 60}m`;
}

function Elapsed({ startedAt }: { startedAt: number | null }) {
  const elapsed = useElapsed(startedAt);
  if (!elapsed) return null;
  return (
    <span className="text-2xs tabular-nums text-muted-foreground">{elapsed}</span>
  );
}

function SpawnRow({
  chatId,
  spawn,
  onSelect,
}: {
  chatId?: string;
  spawn: ActiveSpawn;
  onSelect?: (threadId: string) => void;
}) {
  const [isCancelling, setIsCancelling] = useState(false);

  const handleSelect = () => onSelect?.(spawn.threadId);

  const handleCancel = async (event: React.MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    if (!chatId || !spawn.toolCallId || isCancelling) return;

    setIsCancelling(true);
    try {
      await useChatStore.getState().cancelToolCall(chatId, spawn.toolCallId);
    } catch (error) {
      logger.error("[BackgroundWorkPill] Failed to cancel background agent", {
        chatId,
        toolCallId: spawn.toolCallId,
        error,
      });
      setIsCancelling(false);
    }
  };

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={handleSelect}
      onKeyDown={(event) => {
        if (event.currentTarget !== event.target) return;
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          handleSelect();
        }
      }}
      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent focus:outline-none focus:ring-1 focus:ring-ring"
      data-testid={`background-work-spawn-${spawn.threadId}`}
    >
      {spawn.isBlocked ? (
        <HelpCircle className="h-3.5 w-3.5 flex-shrink-0 text-amber-500" />
      ) : (
        <Bot className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" />
      )}
      <span className="truncate font-medium text-foreground">{spawn.title}</span>
      <span
        className={cn(
          "truncate",
          spawn.isBlocked ? "text-amber-500" : "text-muted-foreground",
        )}
      >
        {spawn.isBlocked ? spawn.blockReason : spawn.activity}
      </span>
      <span className="ml-auto flex flex-shrink-0 items-center gap-1.5">
        <Elapsed startedAt={spawn.startedAt} />
        {/*
          Always rendered, never silently absent. cancelToolCall is the only
          route that stops a spawn, so a row without a tool call id genuinely
          cannot be cancelled — but hiding the control made a long-running
          agent look like it had no stop button at all, which is how this was
          reported. A disabled control that says why is honest; a live one that
          no-ops would be worse than either.
        */}
        <Tooltip content={
            spawn.toolCallId
              ? "Cancel background agent"
              : "This agent cannot be cancelled from here — it is still starting up, or its originating tool call is not yet recorded"
          } placement="bottom" delay={300} wrapperClassName="inline-flex">
<button
          type="button"
          onClick={handleCancel}
          aria-label={`Cancel background agent ${spawn.title}`}
          disabled={isCancelling || !spawn.toolCallId}
          className="rounded p-0.5 transition-colors hover:bg-muted disabled:opacity-60"
        >
          <Square className={cn("h-3.5 w-3.5", isCancelling ? "animate-pulse text-warning" : "text-destructive")} />
        </button>
</Tooltip>
      </span>
    </div>
  );
}

/** localStorage key for failed agents the user dismissed, by workflow id. */
export const DISMISSED_FAILED_AGENTS_KEY = "reliant.backgroundWork.dismissedFailedAgents";
const MAX_DISMISSED_FAILED_AGENTS = 500;

function readDismissedFailedAgents(): Set<string> {
  try {
    const raw = localStorage.getItem(DISMISSED_FAILED_AGENTS_KEY);
    const ids: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(ids) ? ids.filter((id): id is string => typeof id === "string") : []);
  } catch {
    return new Set();
  }
}

function writeDismissedFailedAgents(ids: Set<string>) {
  try {
    // Oldest first, so the cap drops the dismissals least likely to matter.
    const kept = [...ids].slice(-MAX_DISMISSED_FAILED_AGENTS);
    localStorage.setItem(DISMISSED_FAILED_AGENTS_KEY, JSON.stringify(kept));
  } catch {
    // Storage full or unavailable: the row stays dismissed for this session.
  }
}

function FailedSpawnRow({
  chatId,
  spawn,
  onSelect,
  onDismiss,
}: {
  chatId?: string;
  spawn: FailedSpawn;
  onSelect?: (threadId: string) => void;
  onDismiss: (workflowId: string) => void;
}) {
  const [retry, setRetry] = useState<"idle" | "sending" | "requested">("idle");

  const handleSelect = () => onSelect?.(spawn.threadId);

  const handleRetry = async (event: React.MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    if (!chatId || !spawn.parentThreadId || retry !== "idle") return;

    setRetry("sending");
    try {
      // To the PARENT, not the agent: the parent was told this agent failed
      // and owns the plan it belongs to, so it is the one that resumes it.
      const response = await chatGrpc.sendAgentMessage(
        chatId,
        spawn.parentThreadId,
        failedAgentRetryMessage(spawn),
      );
      if (!response.success) {
        toast.error(response.message);
        setRetry("idle");
        return;
      }
      setRetry("requested");
    } catch (error) {
      logger.error("[BackgroundWorkPill] Failed to ask the parent to retry a failed agent", {
        chatId,
        threadId: spawn.threadId,
        error,
      });
      toast.error(error);
      setRetry("idle");
    }
  };

  const handleDismiss = (event: React.MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    onDismiss(spawn.workflowId);
  };

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={handleSelect}
      onKeyDown={(event) => {
        if (event.currentTarget !== event.target) return;
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          handleSelect();
        }
      }}
      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent focus:outline-none focus:ring-1 focus:ring-ring"
      data-testid={`background-work-failed-${spawn.threadId}`}
    >
      <AlertTriangle className="h-3.5 w-3.5 flex-shrink-0 text-destructive" />
      <span className="truncate font-medium text-foreground">{spawn.title}</span>
      <span className={cn("truncate", retry === "requested" ? "text-muted-foreground" : "text-destructive")}>
        {retry === "requested" ? "Retry requested" : "Failed"}
      </span>
      <span className="ml-auto flex flex-shrink-0 items-center gap-1.5">
        <Tooltip
          content={
            spawn.parentThreadId
              ? "Ask its parent to resume this agent where it stopped"
              : "This agent's parent is not known, so it cannot be asked to retry it"
          }
          placement="bottom"
          delay={300}
          wrapperClassName="inline-flex"
        >
          <button
            type="button"
            onClick={handleRetry}
            aria-label={`Retry failed agent ${spawn.title}`}
            disabled={retry !== "idle" || !spawn.parentThreadId}
            className="rounded p-0.5 transition-colors hover:bg-muted disabled:opacity-60"
          >
            <RotateCw className={cn("h-3.5 w-3.5 text-foreground", retry === "sending" && "animate-spin")} />
          </button>
        </Tooltip>
        <Tooltip content="Dismiss" placement="bottom" delay={300} wrapperClassName="inline-flex">
          <button
            type="button"
            onClick={handleDismiss}
            aria-label={`Dismiss failed agent ${spawn.title}`}
            className="rounded p-0.5 transition-colors hover:bg-muted"
          >
            <X className="h-3.5 w-3.5 text-muted-foreground" />
          </button>
        </Tooltip>
      </span>
    </div>
  );
}

function CommandRow({
  command,
  onSelect,
}: {
  command: ActiveCommand;
  onSelect?: (processId: string) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => onSelect?.(command.id)}
      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent"
      data-testid={`background-work-command-${command.id}`}
    >
      <Terminal className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" />
      <span className="truncate font-mono text-foreground">{command.command}</span>
      <span className="ml-auto flex-shrink-0">
        <Elapsed startedAt={command.startedAt} />
      </span>
    </button>
  );
}

export function BackgroundWorkPill({
  chatId,
  worktreeId,
  onSelectThread,
  onSelectCommand,
}: BackgroundWorkPillProps) {
  const { spawns, failedSpawns, commands, blockedSpawns, hasWork } = useBackgroundWork(
    chatId,
    worktreeId,
  );
  const [isExpanded, setIsExpanded] = useState(false);
  const [dismissed, setDismissed] = useState(readDismissedFailedAgents);

  const visibleFailed = useMemo(
    () => failedSpawns.filter((spawn) => !dismissed.has(spawn.workflowId)),
    [failedSpawns, dismissed],
  );
  const dismissFailed = (workflowId: string) => {
    setDismissed((current) => {
      const next = new Set(current);
      next.add(workflowId);
      writeDismissedFailedAgents(next);
      return next;
    });
  };
  const hasFailed = visibleFailed.length > 0;
  const hasAnything = hasWork || hasFailed;

  const summary = useMemo(() => {
    const parts: string[] = [];
    if (spawns.length > 0) {
      parts.push(`${spawns.length} ${spawns.length === 1 ? "agent" : "agents"}`);
    }
    if (commands.length > 0) {
      parts.push(
        `${commands.length} ${commands.length === 1 ? "command" : "commands"}`,
      );
    }
    return parts.join(" · ");
  }, [spawns.length, commands.length]);

  // Collapse when the work drains so the next batch starts collapsed rather
  // than reopening onto an empty list.
  useEffect(() => {
    if (!hasAnything) setIsExpanded(false);
  }, [hasAnything]);

  if (!hasAnything) return null;

  const isBlocked = blockedSpawns.length > 0;
  const failedLabel = `${visibleFailed.length === 1 ? visibleFailed[0].title : `${visibleFailed.length} agents`} failed`;

  return (
    <div className="flex-shrink-0 border-t border-border bg-muted/20 px-3 py-1.5">
      <button
        type="button"
        onClick={() => setIsExpanded((v) => !v)}
        aria-expanded={isExpanded}
        className="flex w-full items-center gap-2 text-xs"
        data-testid="background-work-pill"
      >
        {isBlocked ? (
          <span className="relative flex h-2 w-2 flex-shrink-0">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-amber-500 opacity-75" />
            <span className="relative inline-flex h-2 w-2 rounded-full bg-amber-500" />
          </span>
        ) : hasFailed ? (
          <span className="relative inline-flex h-2 w-2 flex-shrink-0 rounded-full bg-destructive" />
        ) : (
          <span className="relative flex h-2 w-2 flex-shrink-0">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-success opacity-75" />
            <span className="relative inline-flex h-2 w-2 rounded-full bg-success" />
          </span>
        )}

        <span
          className={cn(
            "font-medium",
            isBlocked ? "text-amber-500" : hasFailed ? "text-destructive" : "text-foreground",
          )}
        >
          {isBlocked
            ? `${blockedSpawns.length === 1 ? blockedSpawns[0].title : `${blockedSpawns.length} agents`} waiting on you`
            : hasFailed
              ? failedLabel
              : summary}
        </span>

        {isBlocked && hasFailed && (
          <span className="text-destructive">· {failedLabel}</span>
        )}
        {(isBlocked || hasFailed) && summary && (
          <span className="text-muted-foreground">· {summary} running</span>
        )}

        <ChevronDown
          className={cn(
            "ml-auto h-3.5 w-3.5 flex-shrink-0 text-muted-foreground transition-transform",
            isExpanded && "rotate-180",
          )}
        />
      </button>

      {isExpanded && (
        <div className="mt-1 flex flex-col gap-0.5">
          {visibleFailed.map((spawn) => (
            <FailedSpawnRow
              key={spawn.workflowId}
              chatId={chatId}
              spawn={spawn}
              onSelect={onSelectThread}
              onDismiss={dismissFailed}
            />
          ))}
          {spawns.map((spawn) => (
            <SpawnRow
              key={spawn.threadId}
              chatId={chatId}
              spawn={spawn}
              onSelect={onSelectThread}
            />
          ))}
          {commands.map((command) => (
            <CommandRow
              key={command.id}
              command={command}
              onSelect={onSelectCommand}
            />
          ))}
        </div>
      )}
    </div>
  );
}
