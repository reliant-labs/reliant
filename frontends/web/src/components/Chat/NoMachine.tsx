// Copyright (c) 2025 Reliant Labs

/**
 * The surfaces of a chat with no machine (research/NO_MACHINE_CHATS.md §2.4):
 * the header pill, the one-line composer hint, and the "Continue without
 * machine" offer on a chat whose machine is unavailable.
 *
 * The pill and the hint both open ConnectMachineDialog: connecting is the one
 * thing the user can do about having no machine.
 */

import { useState } from "react";
import { CloudOff } from "lucide-react";
import { toast } from "sonner";
import { useChatStore } from "@/store/chatStore";
import { NO_MACHINE_COMPOSER_HINT, NO_MACHINE_EXPLAINER, NO_MACHINE_PILL } from "@/lib/chatMachine";
import { Tooltip } from "../ui/Tooltip";
import { ConnectMachineDialog } from "./ConnectMachineDialog";

/** Header pill: "No machine · web & integrations". Opens Connect a machine. */
export function NoMachinePill({ chatId }: { chatId: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Tooltip content={`${NO_MACHINE_EXPLAINER}. Click to connect a machine.`} placement="bottom" wrapperClassName="inline-flex">
        <button
          type="button"
          onClick={() => setOpen(true)}
          data-testid="no-machine-pill"
          aria-label={`${NO_MACHINE_PILL}. Connect a machine`}
          className="inline-flex flex-shrink-0 items-center gap-1 rounded-full border border-border/60 bg-background px-2 py-0.5 text-2xs font-medium text-muted-foreground hover:border-border hover:text-foreground transition-colors"
        >
          <CloudOff className="h-3 w-3" aria-hidden="true" />
          {NO_MACHINE_PILL}
        </button>
      </Tooltip>
      <ConnectMachineDialog chatId={chatId} open={open} onClose={() => setOpen(false)} />
    </>
  );
}

/** The composer's one-line hint, with the way out. */
export function NoMachineComposerHint({ chatId }: { chatId: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="flex-shrink-0 px-4 sm:px-6 lg:px-8">
      <div className="mx-auto max-w-[1200px]">
        <p
          role="note"
          data-testid="no-machine-composer-hint"
          className="flex flex-wrap items-center gap-x-2 gap-y-0.5 px-1 pb-1.5 text-xs text-muted-foreground"
        >
          <CloudOff className="h-3 w-3 flex-shrink-0" aria-hidden="true" />
          <span>{NO_MACHINE_COMPOSER_HINT}</span>
          <button
            type="button"
            onClick={() => setOpen(true)}
            className="font-medium text-primary hover:underline"
          >
            Connect a machine
          </button>
        </p>
      </div>
      <ConnectMachineDialog chatId={chatId} open={open} onClose={() => setOpen(false)} />
    </div>
  );
}

/**
 * "Continue without machine": branch this chat into one with no machine, at
 * its latest message, and go there. The original chat is left as it is, so the
 * user can come back to it when the machine is up.
 */
export function ContinueWithoutMachineButton({
  chatId,
  messageId,
  className,
}: {
  chatId: string;
  /** The branch point: the chat's latest message. */
  messageId?: string;
  className?: string;
}) {
  const [busy, setBusy] = useState(false);
  if (!messageId) return null;
  const branch = async () => {
    setBusy(true);
    try {
      await useChatStore.getState().branchChatWithoutMachine(chatId, messageId);
      toast.success("Continuing without a machine", {
        description: "A new branch carries the conversation. It can use the web and your integrations.",
      });
    } catch (err) {
      toast.error("Could not continue without a machine", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setBusy(false);
    }
  };
  return (
    <button
      type="button"
      onClick={() => void branch()}
      disabled={busy}
      data-testid="continue-without-machine"
      className={className ?? "font-medium text-primary hover:underline disabled:opacity-60"}
    >
      {busy ? "Branching…" : "Continue without machine"}
    </button>
  );
}
