import { useCallback, useRef, useState, type ReactNode } from "react";
import { LinkIdentityModal } from "@/components/Billing/LinkIdentityModal";
import {
  ACCOUNT_REQUIRED_MACHINE_COPY,
  isAccountRequiredError,
} from "@/lib/accountRequired";
import { isAnonymousSession } from "@/lib/anonymousSession";

export interface UseAccountGate {
  /**
   * Run `action` now for a signed-in user. For an anonymous session, ask for an
   * email first and run it once the identity is linked.
   */
  run: (action: () => unknown, intro?: string) => Promise<void>;
  /**
   * For the server's `account_required` refusal: opens the same modal and
   * re-runs `retry` after linking. Returns false for any other error.
   */
  handleError: (err: unknown, retry: () => unknown, intro?: string) => boolean;
  /** Render this somewhere in the component's tree. Null when idle. */
  modal: ReactNode;
}

/**
 * The identity ask for actions the server refuses to an anonymous account
 * (creating a managed machine, redeeming a compute coupon). Dismissing leaves
 * the action undone; linking performs it.
 */
export function useAccountGate(
  returnTo?: () => string | undefined,
): UseAccountGate {
  const [intro, setIntro] = useState<string | null>(null);
  const [serverMessage, setServerMessage] = useState("");
  const pending = useRef<(() => unknown) | null>(null);

  const ask = useCallback(
    (action: () => unknown, introCopy: string, message = "") => {
      pending.current = action;
      setServerMessage(message);
      setIntro(introCopy);
    },
    [],
  );

  const run = useCallback(
    async (action: () => unknown, introCopy = ACCOUNT_REQUIRED_MACHINE_COPY) => {
      if (isAnonymousSession()) {
        ask(action, introCopy);
        return;
      }
      await action();
    },
    [ask],
  );

  const handleError = useCallback(
    (err: unknown, retry: () => unknown, introCopy = ACCOUNT_REQUIRED_MACHINE_COPY) => {
      if (!isAccountRequiredError(err)) return false;
      ask(retry, introCopy, err.rawMessage);
      return true;
    },
    [ask],
  );

  const close = useCallback(() => {
    pending.current = null;
    setIntro(null);
  }, []);

  const modal =
    intro === null ? null : (
      <LinkIdentityModal
        intro={intro}
        message={serverMessage}
        returnTo={returnTo?.()}
        onLinked={() => {
          const action = pending.current;
          close();
          void action?.();
        }}
        onDismiss={close}
      />
    );

  return { run, handleError, modal };
}
