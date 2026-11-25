// The "confirm it's you" step every account action that needs a recent proof
// shares (lib/auth/account.ts, "Recent proof").
//
// An action runs through `withRecentProof(action)`. When the broker answers
// `reauth_required`, a dialog asks for the password (when the account has one),
// proves it (POST /auth/reauth) and runs the action again; an account with no
// password can only sign out and in again, which the dialog offers too. The
// action runs FIRST and asks only when refused, so a person who signed in a
// minute ago is never asked twice.

import { createContext, useCallback, useContext, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import Modal from "@/components/forge-ui/modal";
import Input from "@/components/forge-ui/input";
import Label from "@/components/forge-ui/label";
import Button from "@/components/forge-ui/button";
import { AccountError, isReauthRequired, reauthenticate, type ReauthMethod } from "@/lib/auth/account";
import { useAuth } from "@/lib/auth/use-auth";
import { authPath, settingsPath } from "@/lib/paths";
import { InlineError } from "./parts";

/** Runs `action`; on `reauth_required` asks for a proof and runs it again. Resolves undefined when the person cancels. */
export type WithRecentProof = <T>(action: () => Promise<T>) => Promise<T | undefined>;

const RecentProofContext = createContext<WithRecentProof | null>(null);

/** The proof runner of the nearest RecentProofProvider. */
export function useRecentProof(): WithRecentProof {
  const run = useContext(RecentProofContext);
  if (!run) throw new Error("useRecentProof needs a RecentProofProvider");
  return run;
}

interface Pending {
  methods: ReauthMethod[];
  action: () => Promise<unknown>;
  resolve: (value: unknown) => void;
  reject: (err: unknown) => void;
}

export function RecentProofProvider({ children, returnTo }: { children: ReactNode; returnTo?: string }) {
  const [pending, setPending] = useState<Pending | null>(null);
  const pendingRef = useRef<Pending | null>(null);

  const run = useCallback<WithRecentProof>(async <T,>(action: () => Promise<T>) => {
    try {
      return await action();
    } catch (err) {
      if (!isReauthRequired(err)) throw err;
      return new Promise<T | undefined>((resolve, reject) => {
        const next: Pending = {
          methods: err.methods,
          action,
          resolve: resolve as (value: unknown) => void,
          reject,
        };
        // A second refusal while one dialog is open settles the first as cancelled.
        pendingRef.current?.resolve(undefined);
        pendingRef.current = next;
        setPending(next);
      });
    }
  }, []);

  const close = (settle: (p: Pending) => void) => {
    const p = pendingRef.current;
    pendingRef.current = null;
    setPending(null);
    if (p) settle(p);
  };

  return (
    <RecentProofContext.Provider value={run}>
      {children}
      {pending && (
        <ReauthDialog
          methods={pending.methods}
          returnTo={returnTo ?? settingsPath("account")}
          onCancel={() => close((p) => p.resolve(undefined))}
          onProved={async () => {
            const p = pendingRef.current;
            if (!p) return;
            // Close first: the retried action shows its own progress and errors.
            close(() => {});
            try {
              p.resolve(await p.action());
            } catch (err) {
              p.reject(err);
            }
          }}
        />
      )}
    </RecentProofContext.Provider>
  );
}

function ReauthDialog({
  methods,
  returnTo,
  onCancel,
  onProved,
}: {
  methods: ReauthMethod[];
  returnTo: string;
  onCancel: () => void;
  onProved: () => void | Promise<void>;
}) {
  const navigate = useNavigate();
  const { signOut } = useAuth();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const offersPassword = methods.includes("password");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!password) return;
    setBusy(true);
    setError(null);
    try {
      await reauthenticate(password);
      await onProved();
    } catch (err) {
      setError(
        err instanceof AccountError && err.code === "wrong_password"
          ? "That password was not accepted."
          : err instanceof Error
            ? err.message
            : "Could not confirm it's you.",
      );
      setBusy(false);
    }
  };

  const signInAgain = async () => {
    setBusy(true);
    try {
      await signOut();
    } catch {
      // Signed out or not, the sign-in screen is where this goes next.
    }
    void navigate({ to: authPath(), search: { redirect: returnTo } });
  };

  return (
    <Modal
      open
      onClose={busy ? () => {} : onCancel}
      size="sm"
      title="Confirm it's you"
      description={
        offersPassword
          ? "This change needs a recent sign-in. Enter your password to continue."
          : "This change needs a recent sign-in. Sign out and sign in again, then try it once more."
      }
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          {offersPassword ? (
            <Button variant="primary" size="sm" type="submit" form="reauth-form" loading={busy} disabled={!password}>
              Continue
            </Button>
          ) : (
            <Button variant="primary" size="sm" onClick={() => void signInAgain()} loading={busy}>
              Sign in again
            </Button>
          )}
        </>
      }
    >
      {offersPassword ? (
        <form id="reauth-form" onSubmit={(e) => void submit(e)} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="reauth-password">Password</Label>
            <Input
              id="reauth-password"
              type="password"
              autoComplete="current-password"
              autoFocus
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              invalid={error !== null}
            />
          </div>
          <InlineError message={error} />
          {methods.includes("sign_in") && (
            <button
              type="button"
              onClick={() => void signInAgain()}
              disabled={busy}
              className="text-meta text-muted-foreground underline-offset-2 hover:text-foreground hover:underline disabled:opacity-50"
            >
              Sign out and sign in again instead
            </button>
          )}
        </form>
      ) : (
        <InlineError message={error} />
      )}
    </Modal>
  );
}
