// Copyright (c) 2026 Reliant Labs

/**
 * A machine's housekeeping, on its page (Settings → Agent sandboxes → one
 * sandbox): held worktrees and disk pressure, worktree-reclaim proposals,
 * worktrees marked to be reclaimed. The Inbox keeps these out unless the
 * person turns its housekeeping filter on (Settings → Notifications), so this
 * card is where they are decided.
 *
 * Each row is the Inbox's own row (inbox/InboxItem), so Clean up, approving
 * or declining a reclaim, Keep, Reclaim now and dismiss (with Undo) are the
 * same code path as there. With nothing waiting the card is not drawn.
 */

import { Brush } from "lucide-react";

import { useMachineHousekeepingItems } from "@/hooks/inbox-queries";
import { InboxItem, subjectName } from "@/components/inbox/InboxItem";
import { useDismissWithUndo } from "@/components/inbox/useDismissWithUndo";
import { Card, CardHeader, CardTitle } from "./ui";

export function MachineHousekeepingCard({
  daemonId,
  title = "Housekeeping",
  className,
}: {
  /** The machine; "" lists the workspaces no machine is on record for. */
  daemonId: string;
  title?: string;
  className?: string;
}) {
  const items = useMachineHousekeepingItems(daemonId);
  const onDismiss = useDismissWithUndo();
  if (items.length === 0) return null;
  const headingId = `machine-housekeeping-${daemonId || "unowned"}`;
  return (
    <Card className={className} data-testid="machine-housekeeping-card">
      <CardHeader>
        <CardTitle id={headingId} className="inline-flex items-center gap-2">
          <Brush className="h-4 w-4 text-muted-foreground" aria-hidden="true" /> {title}
          <span className="text-xs font-normal tabular-nums text-muted-foreground">{items.length}</span>
        </CardTitle>
      </CardHeader>
      <ul aria-labelledby={headingId} className="divide-y divide-border-subtle">
        {items.map((item) => (
          <InboxItem
            key={item.itemId}
            item={item}
            showProject={false}
            onDismiss={(dismissed) => onDismiss([dismissed], `“${subjectName(dismissed)}”`)}
          />
        ))}
      </ul>
    </Card>
  );
}
