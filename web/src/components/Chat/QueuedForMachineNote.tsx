import { Clock } from "lucide-react";
import { QUEUED_UNTIL_MACHINE_IS_BACK } from "../../lib/daemon-wait";

/**
 * The transcript footer under a message whose run ended because its machine
 * never came up (ChatActivity.QUEUED_FOR_MACHINE). Nothing is running, so it
 * does not animate like the thinking indicator; but the message is not lost
 * either — the server sends it as soon as the machine connects — and the line
 * says so instead of leaving an unanswered message under an error card.
 */
export function QueuedForMachineNote() {
  return (
    <div data-testid="queued-for-machine-note" className="flex items-center gap-2 text-sm text-muted-foreground">
      <Clock className="h-3.5 w-3.5 flex-shrink-0" aria-hidden="true" />
      <span>{QUEUED_UNTIL_MACHINE_IS_BACK}</span>
    </div>
  );
}
