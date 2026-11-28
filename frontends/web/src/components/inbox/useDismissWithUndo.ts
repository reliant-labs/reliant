// Copyright (c) 2026 Reliant Labs

import type { InboxItem } from "@/gen/reliant/v1/inbox_pb";
import { useDismissInboxItems, useRestoreInboxItems } from "@/hooks/inbox-queries";
import { toast } from "@/lib/toast-manager";

/**
 * Dismiss items, with an Undo toast: `onDismiss(items, "“Nightly triage”")`.
 * They leave every cached Inbox list at once (the Inbox page's and a
 * machine page's housekeeping alike); Undo restores them on the server.
 */
export function useDismissWithUndo(): (dismissed: InboxItem[], what: string) => void {
  const dismiss = useDismissInboxItems();
  const restore = useRestoreInboxItems();
  return (dismissed, what) => {
    const ids = dismissed.map((item) => item.itemId);
    dismiss.mutate({ itemIds: ids }, {
      onSuccess: () =>
        toast.notify(`Dismissed ${what}`, {
          // Long enough to notice and reach for Undo; the default 4s is not.
          duration: 8000,
          action: { label: "Undo", onClick: () => restore.mutate({ itemIds: ids }) },
        }),
      onError: (error) => void toast.error(error),
    });
  };
}
