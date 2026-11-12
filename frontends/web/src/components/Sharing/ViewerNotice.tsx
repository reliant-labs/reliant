// Copyright (c) 2025 Reliant Labs

/**
 * What a viewer of a shared chat sees where the composer would be
 * (control-plane docs/design/org-sharing.md §4): whose chat it is, that it is
 * read-only, and what that means. Sending, approving and answering stay with
 * the owner, because they act on the owner's machine with the owner's
 * credentials. An org admin or owner may also make the chat private here
 * (moderation); they can never share one.
 */

import { useState } from "react";
import { Eye } from "lucide-react";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import type { Chat } from "@/api/client";
import { useActiveOrg } from "@/hooks/useOrgs";
import { useOrgMemberNames, useSetChatShared } from "@/hooks/sharing-queries";
import { toast } from "@/lib/toast-manager";
import { useChatStore } from "@/store/chatStore";

/** Whether a role in an org may make a member's shared chat private. */
export function canModerateShares(role: string | undefined): boolean {
  return role === "admin" || role === "owner";
}

export function ViewerNotice({ chat }: { chat: Chat }) {
  const { orgs } = useActiveOrg();
  const org = orgs.find((o) => o.id === chat.orgId);
  const names = useOrgMemberNames(chat.orgId ?? null);
  // The owner's name when the roster resolves it; else words that still say
  // who acts ("a member" would read as any member of the org).
  const ownerName = names.get(chat.userId);
  const sharedBy = ownerName ? `Shared by ${ownerName}` : "Shared";
  const actor = ownerName ?? "the person who shared it";
  const moderate = canModerateShares(org?.role);
  const [confirming, setConfirming] = useState(false);
  const unshare = useSetChatShared();

  const makePrivate = async () => {
    try {
      await unshare.unshare(chat.id);
      setConfirming(false);
      toast.success("Made private");
      // An admin who is not the owner can no longer read it.
      if (useChatStore.getState().activeChatId === chat.id) {
        useChatStore.getState().clearCurrentChat(null);
      }
    } catch (error) {
      setConfirming(false);
      void toast.error(error);
    }
  };

  return (
    <div
      className="mx-auto w-full max-w-[1200px] px-4 pb-3 sm:px-6 lg:px-8"
      data-testid="viewer-notice"
      role="note"
      aria-label="Read-only chat"
    >
      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-card px-4 py-3 text-sm">
        <Eye className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="font-medium text-foreground">
            {sharedBy}
            {org ? ` with ${org.name}` : ""} · Read only
          </p>
          <p className="text-xs text-muted-foreground">
            You can read along as it runs. Only {actor} can send messages, approve tool calls or answer questions. Tool
            results stay on their machine.
          </p>
        </div>
        {moderate && (
          <Button variant="outline" size="sm" onClick={() => setConfirming(true)} data-testid="viewer-make-private">
            Make private
          </Button>
        )}
      </div>
      <ConfirmationDialog
        open={confirming}
        onCancel={() => setConfirming(false)}
        onConfirm={() => void makePrivate()}
        loading={unshare.isPending}
        variant="warning"
        title="Make this chat private?"
        description={`Members of ${org?.name ?? "the organization"} will no longer see it, and neither will you. ${ownerName ?? "The person who shared it"} keeps it and can share it again. This is recorded in the organization's share history.`}
        confirmLabel="Make private"
      />
    </div>
  );
}
