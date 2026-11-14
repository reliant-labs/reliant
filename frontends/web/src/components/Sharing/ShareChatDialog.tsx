// Copyright (c) 2025 Reliant Labs

/**
 * The owner's share toggle (control-plane docs/design/org-sharing.md §2, §4).
 * Sharing is a disclosure, so the dialog says exactly what the org's members
 * will and will not see before the owner confirms it. Stopping is one step
 * back.
 */

import { useEffect, useState } from "react";
import { Check, X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { ResourceVisibility } from "@/gen/reliant/v1/common_pb";
import { useChat } from "@/hooks/chat-queries";
import { useSetChatShared } from "@/hooks/sharing-queries";
import { useActiveOrg } from "@/hooks/useOrgs";
import { toast } from "@/lib/toast-manager";

/** What a member of the org can see of a shared chat, and what they cannot. */
export const SHARED_CHAT_SEES = [
  "The conversation, as it happens",
  "Each tool call and its input: commands, file paths and edits",
  "Plans, tasks and questions",
] as const;

export const SHARED_CHAT_DOES_NOT_SEE = [
  "Tool results: file contents and command output",
  "Your machine, workspace or local path",
  "Anything they could act with: they cannot send, approve or answer",
] as const;

interface ShareChatDialogProps {
  chatId: string;
  open: boolean;
  onClose: () => void;
}

export function ShareChatDialog({ chatId, open, onClose }: ShareChatDialogProps) {
  const chat = useChat(open ? chatId : undefined).data;
  const { orgs } = useActiveOrg();
  const org = orgs.find((o) => o.id === chat?.orgId);
  const orgName = org?.name ?? "your organization";
  const shared = chat?.visibility === ResourceVisibility.ORG;
  const sharing = useSetChatShared();
  const [error, setError] = useState("");

  useEffect(() => {
    if (open) setError("");
  }, [open]);

  const confirm = async () => {
    setError("");
    try {
      if (shared) {
        await sharing.unshare(chatId);
        toast.success("Stopped sharing");
      } else {
        await sharing.share(chatId);
        toast.success(`Shared with ${orgName}`);
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not change who can read this chat");
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={shared ? `Stop sharing with ${orgName}?` : `Share with ${orgName}`}
      description={
        shared
          ? `Members of ${orgName} will no longer see this chat. You can share it again at any time.`
          : `Every member of ${orgName} will be able to open this chat and follow it live, read-only. Anyone who leaves ${orgName} loses access.`
      }
      size="md"
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={sharing.isPending}>
            Cancel
          </Button>
          <Button
            variant={shared ? "outline" : "primary"}
            onClick={() => void confirm()}
            disabled={!chat || sharing.isPending}
            data-testid="share-chat-confirm"
          >
            {sharing.isPending ? "Saving…" : shared ? "Stop sharing" : `Share with ${orgName}`}
          </Button>
        </div>
      }
    >
      <div className="space-y-4 px-6 py-4 text-sm">
        {!shared && (
          <div className="grid gap-4 sm:grid-cols-2">
            <DisclosureList title="Members can see" items={SHARED_CHAT_SEES} kind="sees" />
            <DisclosureList title="Members cannot see" items={SHARED_CHAT_DOES_NOT_SEE} kind="hidden" />
          </div>
        )}
        {!shared && (
          <p className="text-xs text-muted-foreground">
            Tool inputs are shared as written, so a secret typed into a command is visible. An admin of {orgName} can
            make the chat private again.
          </p>
        )}
        {error && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive-ink">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}

function DisclosureList({ title, items, kind }: { title: string; items: readonly string[]; kind: "sees" | "hidden" }) {
  const Icon = kind === "sees" ? Check : X;
  return (
    <div className="rounded-md border border-border/60 bg-background p-3">
      <p className="mb-2 text-xs font-medium text-muted-foreground">{title}</p>
      <ul className="space-y-1.5">
        {items.map((item) => (
          <li key={item} className="flex gap-2 text-xs text-foreground">
            <Icon className="mt-0.5 h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
            <span>{item}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
