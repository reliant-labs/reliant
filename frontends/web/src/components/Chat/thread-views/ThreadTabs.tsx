/**
 * The thread switcher in the chat's top bar: one quiet control naming the
 * thread in view, opening a menu of every thread with its colour, message
 * count, activity and context usage. (Named ThreadTabs for its history: it
 * was a row of tabs under the title.)
 */

import { memo, useMemo } from "react";
import { ChevronDown, GitBranch } from "lucide-react";
import DropdownMenu, { type MenuItem } from "../../forge-ui/dropdown_menu";
import { cn } from "../../../lib/utils";
import Tooltip from "../../forge-ui/tooltip";
import type { ThreadInfo } from "./useThreads";

interface ContextUsageData {
  threadTokenCount: number;
  compactionThreshold: number;
}

interface ThreadTabsProps {
  threads: ThreadInfo[];
  selectedThreadId: string | null; // null means "all threads"
  onSelectThread: (threadId: string | null) => void;
  showAllOption?: boolean;
  /** Context usage per thread (threadId -> usage) */
  contextUsageByThread?: Record<string, ContextUsageData>;
  /** Chat ID for main thread lookup */
  chatId?: string;

}

/**
 * Pulsing activity indicator for active threads
 */
function ActivityPulse({ color, className, threadId }: { color?: string; className?: string; threadId?: string }) {
  return (
    <span 
      className={cn(
        "relative flex h-2 w-2",
        className
      )}
      data-testid={threadId ? `thread-activity-${threadId}` : "thread-activity-all"}
      data-active="true"
    >
      {/* Ping animation */}
      <span 
        className="animate-ping absolute inline-flex h-full w-full rounded-full opacity-75"
        style={{ backgroundColor: color || 'hsl(var(--primary))' }}
      />
      {/* Static dot */}
      <span 
        className="relative inline-flex rounded-full h-2 w-2"
        style={{ backgroundColor: color || 'hsl(var(--primary))' }}
      />
    </span>
  );
}

/**
 * Mini circular context usage indicator for thread tabs
 */
function MiniContextIndicator({ usage, color }: { usage: ContextUsageData; color?: string }) {
  const percent = usage.compactionThreshold > 0
    ? Math.min((usage.threadTokenCount / usage.compactionThreshold) * 100, 100)
    : 0;
  
  if (usage.threadTokenCount === 0) return null;
  
  const size = 14;
  const strokeWidth = 2;
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const strokeDashoffset = circumference - (percent / 100) * circumference;
  
  const formatTokens = (count: number) => {
    if (count >= 1000) {
      return `${(count / 1000).toFixed(1)}k`;
    }
    return count.toString();
  };
  
  const tooltipContent = `Context: ${percent.toFixed(0)}% (${formatTokens(usage.threadTokenCount)}/${formatTokens(usage.compactionThreshold)})`;
  
  return (
    <Tooltip content={tooltipContent} side="bottom" triggerClassName="block">
      <div className="relative flex items-center justify-center w-4 h-4 flex-shrink-0">
        <svg width={size} height={size} className="transform -rotate-90">
          {/* Background circle */}
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke="currentColor"
            strokeWidth={strokeWidth}
            className="opacity-20"
          />
          {/* Progress circle */}
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke={color || "hsl(var(--primary))"}
            strokeWidth={strokeWidth}
            strokeLinecap="round"
            strokeDasharray={circumference}
            strokeDashoffset={strokeDashoffset}
            className="transition-all duration-300"
          />
        </svg>
        {/* px, not a scale step: this label is centred inside a fixed-size SVG
            ring, so it must not grow with the font-size preference or it
            overflows the circle. */}
        <span
          // eslint-disable-next-line no-restricted-syntax
          className="absolute inset-0 flex items-center justify-center font-medium text-[6px]"
          style={{ color: color || "hsl(var(--primary-ink))" }}
        >
          {Math.round(percent)}
        </span>
      </div>
    </Tooltip>
  );
}

export const ThreadTabs = memo(function ThreadTabs({
  threads,
  selectedThreadId,
  onSelectThread,
  showAllOption = true,
  contextUsageByThread,
  chatId,
}: ThreadTabsProps) {
  const visibleThreads = useMemo(
    () => threads.filter((thread) => !thread.isSpawn),
    [threads],
  );

  // Check if any visible thread is active (for "All" indicator)
  // Must be called before any early returns to satisfy hooks rules
  const isAnyActive = useMemo(() => {
    return visibleThreads.some(t => t.isActive);
  }, [visibleThreads]);

  // Don't show if only 1 visible thread
  if (visibleThreads.length <= 1) return null;

  const totalMessages = visibleThreads.reduce((sum, t) => sum + t.messageCount, 0);

  // Helper to get context usage for a thread
  const getContextUsage = (threadId: string, isMain: boolean): ContextUsageData | undefined => {
    if (!contextUsageByThread) return undefined;
    // Try the thread ID directly
    if (contextUsageByThread[threadId]) return contextUsageByThread[threadId];
    // For main thread, also check common main thread identifiers
    if (isMain || threadId === chatId) {
      return contextUsageByThread[chatId || ""] || contextUsageByThread["0"];
    }
    return undefined;
  };

  const threadMark = (thread: ThreadInfo) =>
    thread.isActive ? (
      <ActivityPulse color={thread.color} threadId={thread.id} />
    ) : thread.isMain ? (
      <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: thread.color }} />
    ) : (
      <GitBranch className="size-3.5 shrink-0" style={{ color: thread.color }} />
    );

  const selected = visibleThreads.find((thread) => thread.id === selectedThreadId);
  const items: MenuItem[] = [
    ...(showAllOption
      ? [{
          label: "All",
          icon: isAnyActive ? <ActivityPulse /> : <span className="size-2 shrink-0 rounded-full bg-muted-foreground/50" />,
          checked: selectedThreadId === null,
          hint: <span className="tabular-nums">{totalMessages}</span>,
          onClick: () => onSelectThread(null),
        }]
      : []),
    ...visibleThreads.map((thread): MenuItem => {
      const usage = getContextUsage(thread.id, thread.isMain);
      return {
        label: thread.name,
        icon: threadMark(thread),
        checked: selectedThreadId === thread.id,
        testId: `thread-option-${thread.id}`,
        hint: (
          <span className="inline-flex items-center gap-1.5 tabular-nums">
            {thread.messageCount}
            {usage && <MiniContextIndicator usage={usage} color={thread.color} />}
          </span>
        ),
        onClick: () => {
          if (!thread.isMain) {
            window.dispatchEvent(new CustomEvent("contextual-tip-thread-interacted"));
          }
          onSelectThread(thread.id);
        },
      };
    }),
  ];

  // One quiet control in the top bar: the thread in view, and a menu of the
  // others. Never a row of tabs of its own.
  return (
    <DropdownMenu
      align="right"
      label="Threads"
      groups={[{ items, selection: "single" }]}
      menuClassName="min-w-[240px]"
      trigger={(props) => (
        <button
          {...props}
          ref={props.ref as React.Ref<HTMLButtonElement>}
          type="button"
          aria-label={`Thread: ${selected?.name ?? "All"}`}
          data-contextual-tip="spawned-thread-item"
          // Narrower than xl it shares the app bar with the chat title: a count, or a short name.
          className="inline-flex h-8 max-w-[8rem] shrink-0 items-center gap-1.5 rounded-lg px-2 text-ui text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring xl:max-w-[14rem]"
        >
          {selected ? threadMark(selected) : isAnyActive ? <ActivityPulse /> : <GitBranch className="size-3.5 shrink-0" />}
          <span className="truncate">
            {selected ? selected.name : (
              <>
                {visibleThreads.length}
                <span className="hidden xl:inline"> threads</span>
              </>
            )}
          </span>
          <ChevronDown className="size-3.5 shrink-0 opacity-70" aria-hidden="true" />
        </button>
      )}
    />
  );
});
