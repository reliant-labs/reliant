/**
 * The identity ask, in place, over the checkout that raised it.
 *
 * ── Why a modal and not the /upgrade route ────────────────────────────
 *
 * The rule that produces this state — no purchase against an anonymous browser
 * session — lives in the checkout mutation and is not negotiable. What WAS
 * negotiable is how the user is asked. Sending them to `/upgrade` meant a
 * route change, a `returnTo` round trip and a cold SPA boot on the way back,
 * in the middle of a purchase, to collect one email address.
 *
 * So the ask happens here, over the panel, and **dismissal is the return
 * path**: on success this closes and the caller re-runs checkout in the same
 * mounted panel. There is no navigation to get right because there is no
 * navigation.
 *
 * ── The one place that is still honest about redirecting ──────────────
 *
 * Email + password completes entirely in this modal. GitHub/Google take the
 * whole window by nature, so they keep `returnTo` and the user really does
 * leave. Email is the prominent path for exactly that reason. Pretending OAuth
 * is navigation-free would be a promise the browser breaks.
 *
 * ── No way back to being anonymous ────────────────────────────────────
 *
 * `LinkIdentityForm` has no "Skip for now" — that button calls
 * `signInAnonymously`, which mints the very session that blocked the purchase.
 * Offering it here would be a loop dressed as an escape hatch.
 */

import { Modal } from "@/components/ui/Modal";
import { LinkIdentityForm } from "@/components/LinkIdentityForm";

export interface LinkIdentityModalProps {
  /** The refusal from the checkout mutation, shown verbatim. */
  message: string;
  /**
   * Where an OAuth round-trip should land. The email path never uses it — it
   * does not leave the page.
   */
  returnTo?: string;
  /** The account now carries a real identity; checkout can be retried. */
  onLinked: () => void;
  /** The user backed out. Checkout stays refused. */
  onDismiss: () => void;
}

export function LinkIdentityModal({
  message,
  returnTo,
  onLinked,
  onDismiss,
}: LinkIdentityModalProps) {
  return (
    <Modal
      isOpen
      onClose={onDismiss}
      title="Finish setting up your account"
      size="md"
    >
      <div className="space-y-6">
        <div className="space-y-2">
          <p className="text-sm text-foreground">
            You skipped sign-in earlier, so this account is temporary. To set up
            billing we need a full account.
          </p>
          <p className="text-sm text-muted-foreground">{message}</p>
          {/* Same promise, same words as /upgrade. Two surfaces phrasing this
              differently is how a user starts wondering which is true. */}
          <p className="text-sm text-muted-foreground">
            Your chats and projects stay exactly as they are — this attaches an
            email to the account you already have.
          </p>
        </div>

        <LinkIdentityForm onLinked={onLinked} returnTo={returnTo} />
      </div>
    </Modal>
  );
}
