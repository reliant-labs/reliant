/**
 * Thread Tabs - Compact tab bar for switching between threads
 * 
 * Designed to integrate into ChatHeader as row 3.
 * Shows thread tabs with color indicators, message counts, activity state, and context usage.
 */

import { memo, useMemo } from "react";
import { GitBranch, HandMetal } from "lucide-react";
import { cn } from "../../../lib/utils";
import { Tooltip } from "../../ui/Tooltip";
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
  /** Callback to force-yield a running thread */
  onForceYieldThread?: (threadId: string) => void;
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
    <Tooltip content={tooltipContent} placement="bottom">
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
        <span
          className="absolute inset-0 flex items-center justify-center font-medium text-[6px]"
          style={{ color: color || "hsl(var(--primary))" }}
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
  onForceYieldThread,
}: ThreadTabsProps) {
  // Check if any thread is active (for "All" tab indicator)
  // Must be called before any early returns to satisfy hooks rules
  const isAnyActive = useMemo(() => {
    return threads.some(t => t.isActive);
  }, [threads]);
  
  // Don't show if only 1 thread
  if (threads.length <= 1) return null;
  
  const totalMessages = threads.reduce((sum, t) => sum + t.messageCount, 0);
  
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
  
  return (
    <div className="flex items-center gap-1.5 py-1.5 overflow-x-auto scrollbar-thin scrollbar-thumb-border scrollbar-track-transparent">
      {/* All threads option */}
      {showAllOption && (
        <button
          onClick={() => onSelectThread(null)}
          className={cn(
            "px-2.5 py-1 rounded-md text-xs font-medium transition-all whitespace-nowrap flex items-center gap-1.5",
            "border",
            selectedThreadId === null
              ? "bg-primary/10 text-primary border-primary/30"
              : "text-muted-foreground border-transparent hover:text-foreground hover:bg-muted/50"
          )}
        >
          {/* Activity indicator for "All" - shows when any thread is active */}
          {isAnyActive && <ActivityPulse />}
          <span>All</span>
          <span className="text-[10px] opacity-70 tabular-nums">{totalMessages}</span>
        </button>
      )}

      {/* Thread tabs */}
      {threads.map((thread) => {
        const isSelected = selectedThreadId === thread.id;
        const isSpawnedThread = !thread.isMain;

        return (
          <button
            key={thread.id}
            onClick={() => {
              if (isSpawnedThread) {
                window.dispatchEvent(new CustomEvent("contextual-tip-thread-interacted"));
              }
              onSelectThread(thread.id);
            }}
            className={cn(
              "px-2.5 py-1 rounded-md text-xs font-medium transition-all whitespace-nowrap flex items-center gap-1.5",
              "border",
              isSelected
                ? "border-current"
                : "border-transparent hover:bg-muted/50"
            )}
            style={{
              color: isSelected ? thread.color : undefined,
              backgroundColor: isSelected ? `${thread.color}10` : undefined,
            }}
            data-contextual-tip={isSpawnedThread ? "spawned-thread-item" : undefined}
          >
            {/* Thread indicator - show activity pulse if active, otherwise static indicator */}
            {thread.isActive ? (
              <ActivityPulse color={thread.color} threadId={thread.id} />
            ) : thread.isMain ? (
              <span
                className="w-2 h-2 rounded-full flex-shrink-0"
                style={{ backgroundColor: thread.color }}
              />
            ) : (
              <GitBranch 
                className="h-3.5 w-3.5 flex-shrink-0" 
                style={{ color: thread.color }}
              />
            )}
            
            {/* Name */}
            <span className={cn(!isSelected && "text-muted-foreground")}>
              {thread.name}
            </span>
            
            {/* Message count */}
            <span className="text-[10px] opacity-60 tabular-nums">
              {thread.messageCount}
            </span>
            
            {/* Context usage indicator */}
            {(() => {
              const usage = getContextUsage(thread.id, thread.isMain);
              return usage ? <MiniContextIndicator usage={usage} color={thread.color} /> : null;
            })()}

            {/* Force-yield button for active non-main threads */}
            {thread.isActive && !thread.isMain && onForceYieldThread && (
              <Tooltip content="Force yield this thread" placement="bottom">
                <span
                  role="button"
                  tabIndex={0}
                  className="inline-flex items-center justify-center w-4 h-4 rounded hover:bg-foreground/10 transition-colors cursor-pointer flex-shrink-0"
                  data-contextual-tip="spawned-thread-force-yield"
                  onClick={(e) => {
                    e.stopPropagation();
                    window.dispatchEvent(new CustomEvent("contextual-tip-thread-force-yield"));
                    onForceYieldThread(thread.id);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.stopPropagation();
                      e.preventDefault();
                      window.dispatchEvent(new CustomEvent("contextual-tip-thread-force-yield"));
                      onForceYieldThread(thread.id);
                    }
                  }}
                >
                  <HandMetal className="h-3 w-3" />
                </span>
              </Tooltip>
            )}
          </button>
        );
      })}
    </div>
  );
});