// Copyright (c) 2025 Reliant Labs

/**
 * "Connect a machine" for a chat with no machine (research/NO_MACHINE_CHATS.md
 * §2.3). Picks one of the user's machines and calls SetChatDaemon, which clears
 * no_machine in the same write: the chat's next turn can use the project's
 * files, the shell and local tools.
 *
 * One-way by design. A chat on a machine never goes back to no machine; to
 * keep working without one, the user branches instead.
 */

import { useEffect, useState } from "react";
import { toast } from "sonner";
import Modal from "../forge-ui/modal";
import { Button } from "../ui/Button";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { useChatStore } from "@/store/chatStore";
import { chatMachineOptions } from "@/lib/chatMachine";
import { cn } from "@/lib/utils";
import { ConnectDaemonModal } from "../Layout/ConnectDaemonModal";

interface ConnectMachineDialogProps {
  chatId: string;
  open: boolean;
  onClose: () => void;
  /** Called with the chat once it is on a machine. */
  onConnected?: () => void;
}

export function ConnectMachineDialog({ chatId, open, onClose, onConnected }: ConnectMachineDialogProps) {
  const { data: daemons = [], isLoading } = useDaemonList();
  const options = chatMachineOptions(daemons);
  const firstUsable = options.find((o) => o.usable)?.value;
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const [connecting, setConnecting] = useState(false);
  const [error, setError] = useState("");
  const [showSetup, setShowSetup] = useState(false);

  // Preselect the best machine each time the dialog opens.
  useEffect(() => {
    if (open) {
      setSelected(undefined);
      setError("");
    }
  }, [open]);
  const choice = selected ?? firstUsable;

  const connect = async () => {
    if (!choice) return;
    setConnecting(true);
    setError("");
    try {
      await useChatStore.getState().connectChatToMachine(chatId, choice);
      const label = options.find((o) => o.value === choice)?.label ?? "your machine";
      toast.success(`Connected to ${label}`, {
        description: "Send a message to continue. The next turn can use this project's files.",
      });
      onConnected?.();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not connect a machine");
    } finally {
      setConnecting(false);
    }
  };

  return (
    <>
      <Modal
        open={open && !showSetup}
        onClose={onClose}
        title="Connect a machine"
        description="This chat has no machine, so it can't read or change files in this project. Connecting moves it onto a machine; it can't go back to no machine afterwards."
        size="sm"
        footer={
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose} disabled={connecting}>
              Cancel
            </Button>
            {options.length > 0 && (
              <Button onClick={() => void connect()} disabled={!choice || connecting} data-testid="connect-machine-confirm">
                {connecting ? "Connecting…" : "Connect"}
              </Button>
            )}
          </div>
        }
      >
        <div className="px-6 py-4">
          {isLoading ? (
            <p className="text-sm text-muted-foreground">Loading your machines…</p>
          ) : options.length === 0 ? (
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">You don&apos;t have a machine yet.</p>
              <Button variant="outline" onClick={() => setShowSetup(true)}>
                Set up a machine
              </Button>
            </div>
          ) : (
            <div role="radiogroup" aria-label="Machine" className="space-y-1.5">
              {options.map((option) => {
                const checked = choice === option.value;
                return (
                  <button
                    key={option.value}
                    type="button"
                    role="radio"
                    aria-checked={checked}
                    onClick={() => setSelected(option.value)}
                    className={cn(
                      "w-full rounded-md border px-3 py-2 text-left transition-colors",
                      "bg-background border-border/60 hover:border-border",
                      checked && "border-primary ring-1 ring-primary/40",
                    )}
                  >
                    <div className="text-sm font-medium text-foreground truncate">{option.label}</div>
                    <div className="text-xs text-muted-foreground">
                      {option.statusLabel}
                      {option.statusLabel === "suspended" && " · wakes when you send"}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
          {error && <p className="mt-3 text-xs text-destructive-ink">{error}</p>}
        </div>
      </Modal>
      <ConnectDaemonModal isOpen={open && showSetup} onClose={() => setShowSetup(false)} />
    </>
  );
}
