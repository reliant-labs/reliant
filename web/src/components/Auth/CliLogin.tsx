import { useCallback, useEffect, useState } from "react";
import { Loader2, ShieldAlert, Terminal } from "lucide-react";
import { supabase } from "../../lib/supabase";
import { getControlPlaneURL } from "../../lib/constants";
import { Button } from "../ui/Button";
import { logger } from "../../lib/logger";

/**
 * CLI login consent: the human half of `forge login` / `reliant auth login`.
 *
 * The control plane's GET /oauth/authorize validates a CLI's PKCE request and
 * sends the browser HERE, with the request's query string unchanged, because
 * this app holds the end-user session and the control plane does not. The
 * page:
 *
 *   1. signs the user in if needed (bounces to /auth and back, keeping the
 *      query so the request survives the detour);
 *   2. shows which CLI is asking, on which machine, for what;
 *   3. POSTs the decision to the control plane's /oauth/approve with the
 *      session JWT, the same way ProxyAuth mints a proxy session;
 *   4. navigates to the loopback URL the control plane returns, which carries
 *      either a one-time code or an error for the waiting CLI.
 *
 * Nothing here is trusted: /oauth/approve re-validates the whole request and
 * decides which scopes the user may actually grant. The scope list shown below
 * is the REQUEST; the page reports the granted set only after approval.
 */

// What each scope lets a CLI do, in the words a person approving it needs.
const SCOPE_LABELS: Record<string, string> = {
  "deploy:read": "View your organization's releases and deployments",
  "deploy:write": "Deploy and promote releases for your organization",
  "secret:read": "Read your organization's managed secret names",
  "secret:write": "Set and delete your organization's managed secrets",
  "reliant:api": "Use the Reliant API as you",
  "daemon:connect": "Register and connect daemons as you",
};

const CLIENT_NAMES: Record<string, string> = {
  "forge-cli": "the forge CLI",
  "reliant-cli": "the Reliant CLI",
};

type Phase =
  | { kind: "loading" }
  | { kind: "ready"; email: string }
  | { kind: "submitting"; email: string; decision: "approve" | "deny" }
  | { kind: "done"; target: string; granted: string[]; approved: boolean }
  | { kind: "error"; message: string };

export function CliLogin() {
  const query = window.location.search;
  const params = new URLSearchParams(query);
  const clientId = params.get("client_id") ?? "";
  const device = params.get("device") ?? "";
  const requested = (params.get("scope") ?? "").split(" ").filter(Boolean);

  const [phase, setPhase] = useState<Phase>({ kind: "loading" });

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      if (!params.get("redirect_uri") || !params.get("code_challenge")) {
        setPhase({
          kind: "error",
          message: "This page was opened without a login request. Run `forge login` or `reliant auth login` again.",
        });
        return;
      }
      const { data: { session } } = await supabase.auth.getSession();
      if (cancelled) return;
      if (!session) {
        // Keep the whole request across sign-in, or the user comes back to a
        // page that no longer knows which CLI was waiting.
        window.location.href = `/auth?redirect=${encodeURIComponent(`/oauth/cli${query}`)}`;
        return;
      }
      setPhase({ kind: "ready", email: session.user.email ?? "your account" });
    })();
    return () => {
      cancelled = true;
    };
    // The query is fixed for the life of this page.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const decide = useCallback(
    async (decision: "approve" | "deny") => {
      if (phase.kind !== "ready") return;
      setPhase({ kind: "submitting", email: phase.email, decision });
      const controlPlaneURL = getControlPlaneURL();
      if (!controlPlaneURL) {
        setPhase({ kind: "error", message: "Control plane API URL not configured." });
        return;
      }
      try {
        const { data: { session } } = await supabase.auth.getSession();
        if (!session) {
          setPhase({ kind: "error", message: "Your session ended. Sign in and run the login again." });
          return;
        }
        const response = await fetch(`${controlPlaneURL}/oauth/approve`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${session.access_token}`,
          },
          body: JSON.stringify({ query, approved: decision === "approve" }),
        });
        const data = (await response.json().catch(() => ({}))) as {
          redirect_to?: string;
          scopes?: string[];
          error_description?: string;
        };
        if (!response.ok || !data.redirect_to) {
          logger.error("[CliLogin] approve failed", { status: response.status, data });
          setPhase({ kind: "error", message: data.error_description ?? `Login failed (HTTP ${response.status}).` });
          return;
        }
        const target = data.redirect_to;
        setPhase({ kind: "done", target, granted: data.scopes ?? [], approved: decision === "approve" });
        // Hand the code (or the refusal) to the CLI's loopback listener.
        window.location.href = target;
      } catch (err) {
        logger.error("[CliLogin] approve threw", err);
        setPhase({ kind: "error", message: err instanceof Error ? err.message : "Login failed." });
      }
    },
    [phase, query]
  );

  if (phase.kind === "loading") {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background">
        <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (phase.kind === "error") {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background px-6">
        <div className="max-w-md w-full space-y-4 text-center">
          <div className="mx-auto w-12 h-12 rounded-full bg-destructive/10 flex items-center justify-center">
            <ShieldAlert className="w-6 h-6 text-destructive" />
          </div>
          <h1 className="text-lg font-semibold">CLI login failed</h1>
          <p className="text-sm text-muted-foreground" data-testid="cli-login-error">
            {phase.message}
          </p>
        </div>
      </div>
    );
  }

  if (phase.kind === "done") {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background px-6">
        <div className="max-w-md w-full space-y-4 text-center">
          <h1 className="text-lg font-semibold">
            {phase.approved ? "Return to your terminal" : "Login declined"}
          </h1>
          <p className="text-sm text-muted-foreground">
            {phase.approved
              ? "The CLI is finishing the login. You can close this tab."
              : "Nothing was granted. You can close this tab."}
          </p>
        </div>
      </div>
    );
  }

  const clientName = CLIENT_NAMES[clientId] ?? clientId;
  const busy = phase.kind === "submitting";

  return (
    <div className="flex items-center justify-center min-h-screen bg-background px-6 py-10">
      <div className="max-w-md w-full space-y-6">
        <div className="text-center space-y-2">
          <div className="mx-auto w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center">
            <Terminal className="w-6 h-6 text-primary" />
          </div>
          <h1 className="text-xl font-semibold">Sign in {clientName}?</h1>
          <p className="text-sm text-muted-foreground">
            A command-line login is waiting{device ? ` on ${device}` : ""}. Only continue if you started it.
          </p>
        </div>

        <div className="bg-card border border-border rounded-lg p-5 space-y-4">
          <div className="flex items-center justify-between text-sm">
            <span className="text-muted-foreground">Signing in as</span>
            <span className="font-medium truncate ml-3">{phase.email}</span>
          </div>
          {requested.length > 0 && (
            <div className="space-y-2 pt-1">
              <p className="text-xs text-muted-foreground uppercase tracking-wide">It asks to</p>
              <ul className="space-y-1.5" data-testid="cli-login-scopes">
                {requested.map((scope) => (
                  <li key={scope} className="text-sm flex items-start gap-2">
                    <span className="text-muted-foreground mt-0.5">•</span>
                    <span>{SCOPE_LABELS[scope] ?? <span className="font-mono text-xs">{scope}</span>}</span>
                  </li>
                ))}
              </ul>
              <p className="text-xs text-muted-foreground">
                You are granted only what your role in your organization allows.
              </p>
            </div>
          )}
        </div>

        <div className="flex gap-3">
          <Button variant="secondary" className="flex-1" disabled={busy} onClick={() => void decide("deny")}>
            Deny
          </Button>
          <Button variant="primary" className="flex-1" disabled={busy} onClick={() => void decide("approve")}>
            {busy && phase.decision === "approve" ? <Loader2 className="w-4 h-4 animate-spin" /> : "Approve"}
          </Button>
        </div>
      </div>
    </div>
  );
}
