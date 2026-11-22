import { useCallback, useEffect, useState } from "react";
import { Loader2, Monitor, ShieldAlert } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { hardNavigate } from "@/lib/hardNavigate";
import { logger } from "../../lib/logger";
import { authProvider, FORGE_AUTH_API_BASE } from "../../lib/desktop-forge-sign-in";

/**
 * Desktop sign-in consent (`/auth/handoff?req=…`): the browser half of forge's
 * sign-in handoff (lib/desktop-forge-sign-in.ts).
 *
 * The desktop app opens this page in the system browser. The page:
 *
 *   1. asks reliant-api which account this browser is signed in as and who is
 *      asking (GET /auth/handoff/authorize), and follows the server's sign-in
 *      link first when there is no session;
 *   2. shows the account and the app it would sign in;
 *   3. on Continue — never on its own: the click is the consent — POSTs the
 *      request id and hands the browser to the loopback URL the server returns,
 *      which carries the one-time code to the waiting app.
 *
 * The server decides everything; the page only refuses to navigate anywhere
 * but this origin (sign-in) or a loopback listener (the code), as defence in
 * depth behind the server's allowlist.
 */

interface HandoffAccount {
  subject?: string;
  email?: string;
  name?: string;
}

interface HandoffResponse {
  account?: HandoffAccount;
  destination?: string;
  redirect?: string;
  kind?: string;
  code?: string;
  sign_in?: string;
  error?: string;
}

type Phase =
  | { kind: "loading" }
  | { kind: "unsupported" }
  | { kind: "ready"; who: string; appName: string; submitting: boolean }
  | { kind: "done" }
  | { kind: "cancelled" }
  | { kind: "error"; title: string; message: string };

const AUTHORIZE_URL = `${FORGE_AUTH_API_BASE}/auth/handoff/authorize`;

const START_AGAIN = "Start signing in again from the desktop app.";
const START_AGAIN_INLINE = "start signing in again from the desktop app.";

/** A same-origin absolute URL for `raw`, or null for anything that leaves this origin. */
function sameOriginURL(raw: string): string | null {
  try {
    const url = new URL(raw, window.location.origin);
    return url.origin === window.location.origin ? url.href : null;
  } catch {
    return null;
  }
}

/** `raw` when it is a loopback listener (`http://127.0.0.1:<port>/…`, `http://[::1]:<port>/…`), else null. */
function loopbackURL(raw: string): string | null {
  try {
    const url = new URL(raw);
    if (url.protocol !== "http:") return null;
    if (url.hostname !== "127.0.0.1" && url.hostname !== "[::1]") return null;
    if (!url.port || url.username || url.password) return null;
    return url.href;
  } catch {
    return null;
  }
}

function appNameFor(data: HandoffResponse): string {
  if (data.kind === "native") return "the Reliant desktop app";
  return data.destination || "an app on this device";
}

/** The error phase for a refused authorize call, by the server's `code`. */
function errorPhase(status: number, data: HandoffResponse): Phase {
  switch (data.code) {
    case "reauth_required":
      return {
        kind: "error",
        title: "Sign in again",
        message: `Your session in this browser started before desktop sign-in was available. Sign out of Reliant here and sign in again, then ${START_AGAIN_INLINE}`,
      };
    case "handoff_invalid":
      return {
        kind: "error",
        title: "This sign-in link has expired",
        message: `The request is no longer valid. ${START_AGAIN}`,
      };
    case "not_signed_in":
      return {
        kind: "error",
        title: data.sign_in ? "Sign-in refused" : "You are not signed in",
        message: data.sign_in
          ? `The sign-in link pointed away from this site, so it was not followed. ${START_AGAIN}`
          : `Sign in to Reliant in this browser, then ${START_AGAIN_INLINE}`,
      };
    default:
      return {
        kind: "error",
        title: "Desktop sign-in failed",
        message: `Something went wrong (HTTP ${status}). ${START_AGAIN}`,
      };
  }
}

const NETWORK_ERROR: Phase = {
  kind: "error",
  title: "Could not reach Reliant",
  message: `Check your connection, then reload this page or ${START_AGAIN_INLINE}`,
};

async function readJSON(response: Response): Promise<HandoffResponse> {
  return ((await response.json().catch(() => ({}))) ?? {}) as HandoffResponse;
}

/**
 * Follow the server's sign-in link when it stays on this origin. Returns
 * whether it navigated; a cross-origin link is refused.
 */
function followSignIn(data: HandoffResponse): boolean {
  if (data.code !== "not_signed_in" || !data.sign_in) return false;
  const target = sameOriginURL(data.sign_in);
  if (!target) {
    logger.error("[HandoffConsent] refused a cross-origin sign-in link", { sign_in: data.sign_in });
    return false;
  }
  hardNavigate(target);
  return true;
}

export function HandoffConsent() {
  const req = new URLSearchParams(window.location.search).get("req") ?? "";
  const forgeMode = authProvider() === "forge";
  const [phase, setPhase] = useState<Phase>(() => {
    if (!forgeMode) return { kind: "unsupported" };
    if (!req) return errorPhase(400, { code: "handoff_invalid" });
    return { kind: "loading" };
  });

  useEffect(() => {
    if (!forgeMode || !req) return;
    let cancelled = false;
    void (async () => {
      try {
        const response = await fetch(`${AUTHORIZE_URL}?req=${encodeURIComponent(req)}`, {
          credentials: "include",
        });
        const data = await readJSON(response);
        if (cancelled) return;
        if (!response.ok) {
          // Signed out: sign in on this site, which returns here with `req`.
          if (followSignIn(data)) return;
          setPhase(errorPhase(response.status, data));
          return;
        }
        const account = data.account ?? {};
        setPhase({
          kind: "ready",
          who: account.email || account.name || "your account",
          appName: appNameFor(data),
          submitting: false,
        });
      } catch (err) {
        logger.error("[HandoffConsent] authorize lookup failed", err);
        if (!cancelled) setPhase(NETWORK_ERROR);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [forgeMode, req]);

  const approve = useCallback(async () => {
    if (phase.kind !== "ready" || phase.submitting) return;
    setPhase({ ...phase, submitting: true });
    try {
      const response = await fetch(AUTHORIZE_URL, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ req }),
      });
      const data = await readJSON(response);
      if (!response.ok) {
        // The session ended between loading and the click.
        if (followSignIn(data)) return;
        setPhase(errorPhase(response.status, data));
        return;
      }
      const target = data.redirect ? loopbackURL(data.redirect) : null;
      if (!target) {
        logger.error("[HandoffConsent] refused a non-loopback redirect", { redirect: data.redirect });
        setPhase({
          kind: "error",
          title: "Sign-in refused",
          message: `The app's address is not on this device, so the sign-in was not sent to it. ${START_AGAIN}`,
        });
        return;
      }
      setPhase({ kind: "done" });
      // Hand the one-time code to the desktop app's loopback listener.
      hardNavigate(target);
    } catch (err) {
      logger.error("[HandoffConsent] authorize failed", err);
      setPhase(NETWORK_ERROR);
    }
  }, [phase, req]);

  if (phase.kind === "loading") {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background">
        <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" aria-label="Loading" />
      </div>
    );
  }

  if (phase.kind === "unsupported" || phase.kind === "error") {
    const title = phase.kind === "unsupported" ? "Desktop sign-in is not available here" : phase.title;
    const message =
      phase.kind === "unsupported"
        ? "Signing in to the desktop app through the browser needs the new Reliant sign-in, which this site does not use yet. Sign in from inside the desktop app instead."
        : phase.message;
    return (
      <div className="flex items-center justify-center min-h-screen bg-background px-6">
        <div className="max-w-md w-full space-y-4 text-center">
          <div className="mx-auto w-12 h-12 rounded-full bg-destructive/10 flex items-center justify-center">
            <ShieldAlert className="w-6 h-6 text-destructive" />
          </div>
          <h1 className="text-section">{title}</h1>
          <p className="text-ui text-muted-foreground" data-testid="handoff-message">
            {message}
          </p>
        </div>
      </div>
    );
  }

  if (phase.kind === "done" || phase.kind === "cancelled") {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background px-6">
        <div className="max-w-md w-full space-y-4 text-center">
          <h1 className="text-section">
            {phase.kind === "done" ? "Return to the desktop app" : "Sign-in cancelled"}
          </h1>
          <p className="text-ui text-muted-foreground" data-testid="handoff-message">
            {phase.kind === "done"
              ? "The desktop app is finishing signing in. You can close this tab."
              : "Nothing was shared with the desktop app. You can close this tab."}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-center justify-center min-h-screen bg-background px-6 py-10">
      <div className="max-w-md w-full space-y-6">
        <div className="text-center space-y-2">
          <div className="mx-auto w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center">
            <Monitor className="w-6 h-6 text-primary-ink" />
          </div>
          <h1 className="text-section">
            Continue as {phase.who} to {phase.appName}
          </h1>
          <p className="text-ui text-muted-foreground">
            An app on this device is asking to sign in as you. Only continue if you started signing in from it.
          </p>
        </div>

        <div className="flex gap-3">
          <Button
            variant="secondary"
            className="flex-1"
            disabled={phase.submitting}
            onClick={() => setPhase({ kind: "cancelled" })}
          >
            Cancel
          </Button>
          <Button variant="primary" className="flex-1" disabled={phase.submitting} onClick={() => void approve()}>
            {phase.submitting ? <Loader2 className="w-4 h-4 animate-spin" aria-label="Continuing" /> : "Continue"}
          </Button>
        </div>
      </div>
    </div>
  );
}
