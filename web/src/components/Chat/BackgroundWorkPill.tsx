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
 */

import { useEffect, useMemo, useState } from "react";
import { Bot, ChevronDown, Terminal, HelpCircle, Square } from "lucide-react";
import { cn } from "../../lib/utils";
import { logger } from "../../lib/logger";
import { useChatStore } from "../../store/chatStore";
import { useBackgroundWork, type ActiveSpawn, type ActiveCommand } from "./useBackgroundWork";

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
        <button
          type="button"
          onClick={handleCancel}
          aria-label={`Cancel background agent ${spawn.title}`}
          disabled={isCancelling || !spawn.toolCallId}
          className="rounded p-0.5 transition-colors hover:bg-muted disabled:opacity-60"
          title={
            spawn.toolCallId
              ? "Cancel background agent"
              : "This agent cannot be cancelled from here — it is still starting up, or its originating tool call is not yet recorded"
          }
        >
          <Square className={cn("h-3.5 w-3.5", isCancelling ? "animate-pulse text-warning" : "text-destructive")} />
        </button>
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
  const { spawns, commands, blockedSpawns, hasWork } = useBackgroundWork(
    chatId,
    worktreeId,
  );
  const [isExpanded, setIsExpanded] = useState(false);

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
    if (!hasWork) setIsExpanded(false);
  }, [hasWork]);

  if (!hasWork) return null;

  const isBlocked = blockedSpawns.length > 0;

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
        ) : (
          <span className="relative flex h-2 w-2 flex-shrink-0">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-success opacity-75" />
            <span className="relative inline-flex h-2 w-2 rounded-full bg-success" />
          </span>
        )}

        <span className={cn("font-medium", isBlocked ? "text-amber-500" : "text-foreground")}>
          {isBlocked
            ? `${blockedSpawns.length === 1 ? blockedSpawns[0].title : `${blockedSpawns.length} agents`} waiting on you`
            : summary}
        </span>

        {isBlocked && summary && (
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
