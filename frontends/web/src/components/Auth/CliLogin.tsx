import { useCallback, useEffect, useState } from "react";
import { Loader2, Plug, ShieldAlert, Terminal } from "lucide-react";
import { supabase } from "../../lib/supabase";
import { getControlPlaneURL } from "../../lib/constants";
import Button from "@/components/forge-ui/button";
import { logger } from "../../lib/logger";
import { AuthorizeOrgPicker, useAuthorizeOrg } from "../Orgs/AuthorizeOrgPicker";
import { authPath, oauthCliPath } from "@/lib/paths";

/**
 * CLI login consent: the human half of `forge cloud login` / `reliant auth login`.
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
 *
 * A third-party MCP client (Claude, ChatGPT, Cursor…) that registered itself
 * with the control plane (RFC 7591) arrives here the same way, asking to act
 * on the person's Reliant MCP connectors, or in Reliant Forge when the resource
 * is the Reliant Forge connector (/forge/mcp). Its name is whatever it registered,
 * so the page shows it only as the control plane resolves it
 * (GET /oauth/client), labelled unverified, and never from the query string,
 * which anyone can write. A client the control plane cannot resolve, or one
 * it says is not third-party while this page does not know it, cannot be
 * approved.
 */

// isForgeMCPResource reports whether an RFC 8707 resource is the Reliant Forge
// MCP connector (`<api>/forge/mcp`), whose token acts in Reliant Forge
// (monitoring, releases, environments) rather than on a workspace connector.
// Copy only: the control plane decides what the token may do.
function isForgeMCPResource(resource: string): boolean {
  try {
    return new URL(resource).pathname.endsWith("/forge/mcp");
  } catch {
    return false;
  }
}

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

// Who is asking: one of Reliant's own CLIs, or a third-party MCP client whose
// name the control plane resolved.
type Client = { kind: "cli"; name: string } | { kind: "third_party"; name: string };

const UNKNOWN_CLIENT =
  "This request names an application Reliant cannot identify, so it cannot be approved. " +
  "Start the connection again from the application.";

/**
 * Resolves a client the page does not know through the control plane's
 * public GET /oauth/client. Anything but a 200 naming exactly this client as
 * third-party is null: the page then refuses rather than guessing a name.
 */
async function lookupThirdPartyClient(controlPlaneURL: string, clientId: string): Promise<Client | null> {
  try {
    const response = await fetch(`${controlPlaneURL}/oauth/client?client_id=${encodeURIComponent(clientId)}`);
    if (!response.ok) return null;
    const data = (await response.json().catch(() => null)) as {
      client_id?: unknown;
      client_name?: unknown;
      third_party?: unknown;
    } | null;
    if (!data || data.client_id !== clientId || data.third_party !== true) return null;
    const name = typeof data.client_name === "string" ? data.client_name.trim() : "";
    return { kind: "third_party", name: name || "MCP client" };
  } catch (err) {
    logger.error("[CliLogin] client lookup failed", err);
    return null;
  }
}

// The host a third-party login returns to. The control plane checked the
// redirect URI against the client's registered ones before sending the
// browser here, so this names where the code goes, not a claim.
function redirectHost(raw: string | null): string {
  if (!raw) return "";
  try {
    const url = new URL(raw);
    return url.host || url.protocol.replace(/:$/, "");
  } catch {
    return "";
  }
}

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

  // `forge cloud login` asks for a human identity (credential_kind=human): a
  // person's session authorized per request against their current
  // memberships, bound to no org. The control plane refuses an org on such an
  // approval, so the page neither shows the picker nor sends one.
  const humanLogin = params.get("credential_kind") === "human";
  // RFC 8707: the MCP server a third-party client asks to act on. The control
  // plane accepts only a resource it is configured for.
  const resource = params.get("resource") ?? "";
  const forgeResource = isForgeMCPResource(resource);
  const returnsTo = redirectHost(params.get("redirect_uri"));

  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [client, setClient] = useState<Client | null>(null);
  // `org` is forge's `login --org`: where the picker starts, never the answer.
  const authorizeOrg = useAuthorizeOrg(params.get("org"));
  const orgReady = humanLogin || authorizeOrg.ready;

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      if (!params.get("redirect_uri") || !params.get("code_challenge")) {
        setPhase({
          kind: "error",
          message: "This page was opened without a login request. Run `forge cloud login` or `reliant auth login` again.",
        });
        return;
      }
      const { data: { session } } = await supabase.auth.getSession();
      if (cancelled) return;
      if (!session) {
        // Keep the whole request across sign-in, or the user comes back to a
        // page that no longer knows which CLI was waiting.
        window.location.href = `${authPath()}?redirect=${encodeURIComponent(`${oauthCliPath()}${query}`)}`;
        return;
      }
      const cliName = CLIENT_NAMES[clientId];
      let resolved: Client | null = cliName ? { kind: "cli", name: cliName } : null;
      if (!resolved && clientId) {
        const controlPlaneURL = getControlPlaneURL();
        resolved = controlPlaneURL ? await lookupThirdPartyClient(controlPlaneURL, clientId) : null;
        if (cancelled) return;
      }
      if (!resolved) {
        setPhase({ kind: "error", message: UNKNOWN_CLIENT });
        return;
      }
      setClient(resolved);
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
      if (phase.kind !== "ready" || !client || (decision === "approve" && !orgReady)) return;
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
          // The confirmed org wins over the request's `org` (docs/orgs.md).
          // Approval is disabled until this choice can be shown and confirmed.
          // A human login takes no org at all.
          body: JSON.stringify({
            query,
            approved: decision === "approve",
            ...(!humanLogin && authorizeOrg.orgId ? { org_id: authorizeOrg.orgId } : {}),
          }),
        });
        const data = (await response.json().catch(() => ({}))) as {
          redirect_to?: string;
          scopes?: string[];
          org_id?: string;
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
    [phase, client, query, humanLogin, orgReady, authorizeOrg.orgId]
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
          <h1 className="text-lg font-semibold">{CLIENT_NAMES[clientId] ? "CLI login failed" : "Connection failed"}</h1>
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
            {!phase.approved
              ? "Nothing was granted. You can close this tab."
              : client?.kind === "third_party"
                ? `${client.name} is finishing the connection. You can close this tab.`
                : "The CLI is finishing the login. You can close this tab."}
          </p>
        </div>
      </div>
    );
  }

  const busy = phase.kind === "submitting";
  // The effect sets the client before the page is ready.
  if (!client) return null;
  const clientName = client.name;

  const orgPicker = !humanLogin && (
    <AuthorizeOrgPicker
      orgs={authorizeOrg.orgs}
      error={authorizeOrg.failed}
      onRetry={authorizeOrg.retry}
      orgId={authorizeOrg.orgId}
      onChoose={authorizeOrg.choose}
      disabled={busy}
      noun={client.kind === "third_party" ? "this connection" : "this login"}
    />
  );

  const decisionButtons = (
    <div className="flex gap-3">
      <Button variant="secondary" className="flex-1" disabled={busy} onClick={() => void decide("deny")}>
        Deny
      </Button>
      <Button
        variant="primary"
        className="flex-1"
        disabled={busy || !orgReady}
        onClick={() => void decide("approve")}
      >
        {busy && phase.decision === "approve" ? <Loader2 className="w-4 h-4 animate-spin" /> : "Approve"}
      </Button>
    </div>
  );

  if (client.kind === "third_party") {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background px-6 py-10">
        <div className="max-w-md w-full space-y-6" data-testid="mcp-consent">
          <div className="text-center space-y-2">
            <div className="mx-auto w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center">
              <Plug className="w-6 h-6 text-primary-ink" />
            </div>
            <h1 className="text-xl font-semibold">
              Connect <span data-testid="mcp-consent-client">{clientName}</span> to Reliant?
            </h1>
            <p className="text-sm text-muted-foreground">
              An application is asking to use your Reliant account. Only continue if you are connecting it yourself.
            </p>
          </div>

          <div className="bg-card border border-border rounded-xl p-5 space-y-4">
            <div className="flex items-start gap-2 text-sm" data-testid="mcp-consent-unverified">
              <ShieldAlert className="w-4 h-4 mt-0.5 shrink-0 text-warning-ink" aria-hidden="true" />
              <p>
                <span className="font-medium">Unverified app.</span>{" "}
                <span className="text-muted-foreground">
                  It registered itself automatically, and Reliant has not reviewed it. The name above is the one it
                  chose.
                </span>
              </p>
            </div>
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground">Signing in as</span>
              <span className="font-medium truncate ml-3">{phase.email}</span>
            </div>
            {resource && (
              <div className="flex items-start justify-between gap-3 text-sm">
                <span className="text-muted-foreground shrink-0">Connects to</span>
                <span className="font-mono text-xs break-all text-right" data-testid="mcp-consent-resource">
                  {resource}
                </span>
              </div>
            )}
            {returnsTo && (
              <div className="flex items-start justify-between gap-3 text-sm">
                <span className="text-muted-foreground shrink-0">Returns you to</span>
                <span className="font-mono text-xs break-all text-right">{returnsTo}</span>
              </div>
            )}
            {orgPicker}
            <div className="space-y-2 pt-1">
              <p className="text-xs text-muted-foreground">It asks to</p>
              <ul className="space-y-1.5" data-testid="mcp-consent-scopes">
                <li className="text-sm flex items-start gap-2">
                  <span className="text-muted-foreground mt-0.5">•</span>
                  <span>
                    {forgeResource
                      ? "Act as you in Reliant Forge: monitoring, releases and environments"
                      : "Act as you on your Reliant MCP connectors"}
                  </span>
                </li>
              </ul>
              <p className="text-xs text-muted-foreground">
                {forgeResource
                  ? "It reaches only the organization you choose here, and which of its projects and tools it may use is the Reliant Forge connector you choose next. You can disconnect it at any time."
                  : "Which workspace it may use, and what it may do there, is the connector you choose. You can disconnect it at any time."}
              </p>
            </div>
          </div>

          {decisionButtons}
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-center justify-center min-h-screen bg-background px-6 py-10">
      <div className="max-w-md w-full space-y-6">
        <div className="text-center space-y-2">
          <div className="mx-auto w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center">
            <Terminal className="w-6 h-6 text-primary-ink" />
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
          {orgPicker}
          {requested.length > 0 && (
            <div className="space-y-2 pt-1">
              <p className="text-xs text-muted-foreground">It asks to</p>
              <ul className="space-y-1.5" data-testid="cli-login-scopes">
                {requested.map((scope) => (
                  <li key={scope} className="text-sm flex items-start gap-2">
                    <span className="text-muted-foreground mt-0.5">•</span>
                    <span>{SCOPE_LABELS[scope] ?? <span className="font-mono text-xs">{scope}</span>}</span>
                  </li>
                ))}
              </ul>
              <p className="text-xs text-muted-foreground">
                {humanLogin
                  ? "Each request is checked against your current role in the organization it acts in."
                  : "You are granted only what your role in your organization allows."}
              </p>
            </div>
          )}
        </div>

        {decisionButtons}
      </div>
    </div>
  );
}
