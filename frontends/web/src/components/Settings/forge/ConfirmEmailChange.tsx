// `/auth/account/email/confirm?code=…` — the link the broker mails to a NEW
// address (forge#2663 AccountRoutesConfig.EmailConfirmURL's default). It posts
// the code to POST /auth/account/email/confirm, signed in: the code proves the
// address, the cookie names the account it was sent for. Signed out, it says
// so and offers sign-in, returning here with the code intact.
//
// It confirms on arrival — opening the link is the person's intent, and mail
// scanners that prefetch links run no script — and offers a retry on failure.

import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { MailCheck } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { AccountError, confirmEmailChange } from "@/lib/auth/account";
import { authPath, settingsPath } from "@/lib/paths";
import { useAuthStore } from "@/store/authStore";
import { InlineError, accountErrorMessage } from "./parts";

type State =
  | { kind: "confirming" }
  | { kind: "done"; email: string }
  | { kind: "signed-out" }
  | { kind: "error"; message: string; retry: boolean };

function codeFromLocation(): string {
  if (typeof window === "undefined") return "";
  return new URLSearchParams(window.location.search).get("code")?.trim() ?? "";
}

export function ConfirmEmailChange({ code: codeProp }: { code?: string } = {}) {
  const navigate = useNavigate();
  const code = (codeProp ?? codeFromLocation()).trim();
  const [state, setState] = useState<State>(
    code ? { kind: "confirming" } : { kind: "error", message: "This link has no code. Open the link from the email again.", retry: false },
  );
  const started = useRef(false);

  const confirm = useCallback(async () => {
    setState({ kind: "confirming" });
    try {
      const done = await confirmEmailChange(code);
      setState({ kind: "done", email: done.email });
      // The app shows the signed-in address: re-read it.
      void useAuthStore.getState().refreshSession?.().catch(() => {});
    } catch (err) {
      if (err instanceof AccountError && err.code === "not_signed_in") {
        setState({ kind: "signed-out" });
      } else {
        setState({
          kind: "error",
          message: accountErrorMessage(err, "Couldn't confirm your new address."),
          // A refused code is final; an unreachable service is worth another go.
          retry: !(err instanceof AccountError) || err.status === 0 || err.status >= 500,
        });
      }
    }
  }, [code]);

  useEffect(() => {
    if (!code || started.current) return;
    started.current = true;
    void confirm();
  }, [code, confirm]);

  const here = typeof window === "undefined" ? undefined : `${window.location.pathname}${window.location.search}`;

  return (
    <div className="flex min-h-full items-center justify-center bg-background px-4 py-12">
      <div className="w-full max-w-md space-y-4 rounded-xl border border-border bg-card p-6" data-testid="confirm-email-change">
        <div className="flex items-center gap-3">
          <div className="rounded-md border border-border/60 bg-background p-2">
            <MailCheck className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
          </div>
          <h1 className="text-section text-foreground">Confirm your new address</h1>
        </div>

        {state.kind === "confirming" && (
          <p className="text-ui text-muted-foreground" role="status">
            Confirming…
          </p>
        )}

        {state.kind === "done" && (
          <>
            <p className="text-ui text-foreground" role="status">
              {state.email ? `Your address is now ${state.email}.` : "Your new address is confirmed."}
            </p>
            <Button variant="primary" size="sm" onClick={() => void navigate({ to: settingsPath("account") })}>
              Go to account settings
            </Button>
          </>
        )}

        {state.kind === "signed-out" && (
          <>
            <p className="text-ui text-muted-foreground">
              Sign in to the account you changed, and the address is confirmed when you come back here.
            </p>
            <Button variant="primary" size="sm" onClick={() => void navigate({ to: authPath(), search: { redirect: here } })}>
              Sign in
            </Button>
          </>
        )}

        {state.kind === "error" && (
          <>
            <InlineError message={state.message} />
            <div className="flex gap-2">
              {state.retry && (
                <Button variant="secondary" size="sm" onClick={() => void confirm()}>
                  Try again
                </Button>
              )}
              <Button variant="ghost" size="sm" onClick={() => void navigate({ to: settingsPath("account") })}>
                Account settings
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
