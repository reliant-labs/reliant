/**
 * The mobile chat screen's one top bar (mobile-chat-redesign §5.1).
 *
 * It replaces four bands that each cost a row: the screen header, the
 * workflow pill, `ChatHeader`'s row (a `⋯` and a ring), and the thread tabs.
 * Everything they held is either here, compressed into one optional status
 * line, or one tap away in the Chat sheet the title opens.
 *
 * Presentational: `MobileChatScreen` derives the status (workflow, thread,
 * machine, context) and owns what each control opens.
 */

import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronLeft, FolderGit2, Loader2 } from "lucide-react";
import { ContextUsageIndicator } from "../Chat/ContextUsageIndicator";

/**
 * The ring earns a slot in the bar only once compaction is close enough to
 * change what the user does next — the "it matters now" rule AccountUsageChip
 * applies at 80%. Below it, the Chat sheet still shows usage.
 */
export const CONTEXT_RING_THRESHOLD = 0.7;

export interface ContextUsage {
  threadTokenCount: number;
  compactionThreshold: number;
}

export function contextUsageRatio(usage: ContextUsage | null | undefined): number {
  if (!usage || usage.compactionThreshold <= 0) return 0;
  return usage.threadTokenCount / usage.compactionThreshold;
}

// Explicit px, not `h-11 w-11`: rem sizing resolves against the root
// font-size, and at the smallest Appearance step it measures under 44px.
const ICON_BUTTON =
  "flex min-h-[44px] min-w-[44px] shrink-0 items-center justify-center rounded-md text-muted-foreground active:bg-foreground/5";

interface MobileChatTopBarProps {
  title: string;
  onOpenSheet: () => void;
  /** Display name of a workflow that is running right now (ACTIVE or PENDING). */
  runningWorkflow?: string;
  /**
   * The thread in view when it is not the main one. Back then returns to
   * `onBackToThread`'s target before it leaves the chat.
   */
  subThread?: { name: string; backLabel: string };
  onBackToThread?: () => void;
  noMachine?: boolean;
  contextUsage?: ContextUsage | null;
  /** Present when the chat has a workspace to open. */
  onOpenWorkspace?: () => void;
}

export function MobileChatTopBar({
  title,
  onOpenSheet,
  runningWorkflow,
  subThread,
  onBackToThread,
  noMachine,
  contextUsage,
  onOpenWorkspace,
}: MobileChatTopBarProps) {
  // One line of status, and only status: a quiet chat gets a single-line bar.
  const status = [
    runningWorkflow ? `Running · ${runningWorkflow}` : null,
    subThread ? `Thread: ${subThread.name}` : null,
    noMachine ? "No machine" : null,
  ].filter(Boolean).join(" · ");
  const ratio = contextUsageRatio(contextUsage);

  return (
    // The hairline stays until the scroll-edge treatment (§4.3) lands and can
    // show it only while content is scrolled under the bar.
    <header
      className="flex min-h-[48px] shrink-0 items-center gap-0.5 border-b border-border bg-background px-1"
      data-testid="mobile-chat-top-bar"
    >
      {subThread && onBackToThread ? (
        <button type="button" onClick={onBackToThread} className={ICON_BUTTON} aria-label={subThread.backLabel}>
          <ChevronLeft className="h-5 w-5" />
        </button>
      ) : (
        <Link to="/m/chats" className={ICON_BUTTON} aria-label="Back to chats">
          <ChevronLeft className="h-5 w-5" />
        </Link>
      )}

      {/* The title is the Chat sheet's trigger; the chevron is the cue that
          it opens something (Claude's pattern). */}
      <button
        type="button"
        onClick={onOpenSheet}
        className="flex min-h-[44px] min-w-0 flex-1 flex-col justify-center rounded-md px-1 text-left active:bg-foreground/5"
        aria-label={`${title}, chat actions`}
        aria-haspopup="dialog"
      >
        <span className="flex min-w-0 items-center gap-1">
          {/* text-base is the spec's 15px at the default Appearance step,
              and still follows the font-size preference. */}
          <span className="truncate text-base font-semibold leading-5 text-foreground">{title}</span>
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
        </span>
        {status && (
          <span className="flex min-w-0 items-center gap-1 text-xs leading-4 text-muted-foreground" data-testid="mobile-chat-status">
            {runningWorkflow && <Loader2 className="h-3 w-3 shrink-0 animate-spin text-primary-ink" aria-hidden />}
            <span className="truncate">{status}</span>
          </span>
        )}
      </button>

      {ratio >= CONTEXT_RING_THRESHOLD && contextUsage && (
        <button
          type="button"
          onClick={onOpenSheet}
          className={ICON_BUTTON}
          aria-label={`Context ${Math.round(Math.min(ratio, 1) * 100)}% used`}
          aria-haspopup="dialog"
        >
          <ContextUsageIndicator
            threadTokenCount={contextUsage.threadTokenCount}
            compactionThreshold={contextUsage.compactionThreshold}
            compact
          />
        </button>
      )}

      {onOpenWorkspace && (
        <button type="button" onClick={onOpenWorkspace} className={ICON_BUTTON} aria-label="Open workspace">
          <FolderGit2 className="h-5 w-5" />
        </button>
      )}
    </header>
  );
}
