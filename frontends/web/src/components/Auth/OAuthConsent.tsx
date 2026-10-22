import { useEffect, useState, useCallback } from "react";
import { Loader2, ShieldAlert, ExternalLink } from "lucide-react";
import { supabase } from "../../lib/supabase";
import { Button } from "../ui/Button";
import { logger } from "../../lib/logger";
import { ConnectorConsent } from "../Settings/ConnectorConsent";

/**
 * OAuth consent screen.
 *
 * Supabase's OAuth 2.1 server does not host a consent UI — it validates the
 * authorization request, then redirects the user here with an
 * `authorization_id` and waits for this page to approve or deny. Without this
 * route the flow dead-ends: Supabase redirects to a path that does not exist,
 * the SPA falls through to its default view, and the third-party client sits
 * waiting for a code that never comes.
 *
 * The path is configured in the Supabase dashboard (Authentication → OAuth
 * Server → Authorization Path) and combined with the project's Site URL, so
 * this component's route must match it exactly.
 *
 * ## Two decisions, one flow, approved last
 *
 * Signing in and granting workspace access are genuinely different decisions:
 * an OAuth scope string cannot express "these tools, this directory, no
 * shell", so the connector grant stays a separate record. But presenting them
 * as separate *flows* stranded people. Approving identity redirects straight
 * back to the calling application, so the user left with a token, no
 * connector, and a client that fails its handshake with nothing to explain
 * why.
 *
 * So both decisions happen here, and the OAuth approval is LAST:
 *
 *   1. show who is asking and what they get                    (identity)
 *   2. choose the workspace, tools, path, shell access         (connector)
 *   3. AuthorizeClient — create the grant, bind it to this client   [our DB]
 *   4. approveAuthorization({ skipBrowserRedirect: true })     [Supabase]
 *   5. redirect back to the client, which now handshakes successfully
 *
 * Ordering the Supabase approval last makes the whole thing atomic from the
 * user's side: abandon halfway and nothing was granted. The reverse order —
 * approve, then configure — tells Supabase the user consented and then does
 * more work that can fail, which is how the stranded state happened.
 *
 * There is no deadline cost to waiting. GoTrue gives the pending authorization
 * a single TTL (`GOTRUE_OAUTH_SERVER_AUTHORIZATION_TTL`, default 10m) and
 * `Approve()` mints the code without extending it — the token exchange checks
 * that same `expires_at`. Approving early therefore buys nothing, and the
 * expiry is handled explicitly below.
 */

type AuthorizationDetails = {
  authorization_id?: string;
  redirect_url?: string;
  // `id` is the client's stable UUID, which is what a connector binding is
  // keyed on — the name is a display string the client can change.
  client: { id: string; name: string; uri?: string; logo_uri?: string };
  user: { id: string; email: string };
  scope: string;
};

// Human-readable descriptions for the scopes Supabase issues. An unrecognized
// scope is shown verbatim rather than hidden — a consent screen that silently
// drops something it does not recognize is worse than one that shows a raw
// string.
const SCOPE_LABELS: Record<string, string> = {
  openid: "Confirm your identity",
  profile: "See your basic profile information",
  email: "See your email address",
  phone: "See your phone number",
};

/**
 * Which half of the flow is on screen.
 *
 * `identity` → who is asking; `connector` → what they may touch. The user
 * moves forward only; going back would mean un-choosing a connector that
 * `AuthorizeClient` has already created.
 */
type Step = "identity" | "connector";

export function OAuthConsent() {
  const params = new URLSearchParams(window.location.search);
  const authorizationId = params.get("authorization_id");

  const [details, setDetails] = useState<AuthorizationDetails | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState<"approve" | "deny" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [step, setStep] = useState<Step>("identity");

  // Set when the OAuth approval succeeded but the browser did not follow the
  // redirect. The grant already exists at that point, so the user needs the
  // link rather than an apology.
  const [pendingRedirect, setPendingRedirect] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!authorizationId) {
      setError(
        "This page was opened without an authorization request. Start the connection from the application you are trying to link."
      );
      setLoading(false);
      return;
    }

    try {
      const {
        data: { user },
      } = await supabase.auth.getUser();

      if (!user) {
        // Preserve the authorization id across login, or the user returns to a
        // page that no longer knows what it was approving.
        const back = encodeURIComponent(
          `/oauth/consent?authorization_id=${authorizationId}`
        );
        window.location.href = `/auth?redirect=${back}`;
        return;
      }

      const { data, error: detailsError } =
        await supabase.auth.oauth.getAuthorizationDetails(authorizationId);

      if (detailsError || !data) {
        setError(
          detailsError?.message ??
            "This authorization request is no longer valid. It may have expired — try connecting again."
        );
        setLoading(false);
        return;
      }

      // A request the user has already consented to comes back with a
      // redirect_url and no authorization_id. Re-prompting would be asking the
      // same question twice, so follow it straight through.
      if (!("authorization_id" in data) || !data.authorization_id) {
        if (data.redirect_url) {
          window.location.href = data.redirect_url;
          return;
        }
      }

      setDetails(data as AuthorizationDetails);
    } catch (err) {
      logger.error("[OAuthConsent] failed to load authorization details", err);
      setError("Could not load this authorization request.");
    } finally {
      setLoading(false);
    }
  }, [authorizationId]);

  useEffect(() => {
    load();
  }, [load]);

  /**
   * Finish the OAuth request and hand the user back to the client.
   *
   * Called only after the connector step has recorded a grant + binding (or
   * immediately, for a denial — there is nothing to configure when refusing).
   */
  const finish = useCallback(
    async (decision: "approve" | "deny") => {
      if (!authorizationId) return;
      setSubmitting(decision);
      setError(null);

      try {
        // skipBrowserRedirect keeps the navigation ours: the SDK would
        // otherwise leave for the client the instant it has a code, and this
        // function needs to survive long enough to report a failure.
        const { data, error: decisionError } =
          decision === "approve"
            ? await supabase.auth.oauth.approveAuthorization(authorizationId, {
                skipBrowserRedirect: true,
              })
            : await supabase.auth.oauth.denyAuthorization(authorizationId, {
                skipBrowserRedirect: true,
              });

        if (decisionError || !data?.redirect_url) {
          // Expiry is the failure worth naming: GoTrue rejects an approval
          // once the authorization's TTL has passed, and "could not record
          // your decision" would send someone hunting for a broken connector
          // that is in fact fine.
          const message = decisionError?.message ?? "";
          setError(
            /expire/i.test(message)
              ? "This sign-in request timed out. Your connector was saved — start the connection again from the application and it will use it."
              : message || "Could not record your decision."
          );
          setSubmitting(null);
          return;
        }

        const target = data.redirect_url;
        window.location.href = target;

        // A successful navigation unloads this page, so this timer never
        // fires. It only lands when the browser refused to leave (a blocked
        // cross-origin navigation, an extension), where the alternative is a
        // spinner sitting on top of a flow that actually succeeded. Delayed
        // rather than immediate so the fallback never flashes over a redirect
        // that is simply in progress.
        window.setTimeout(() => setPendingRedirect(target), 2000);
      } catch (err) {
        logger.error("[OAuthConsent] decision failed", err);
        setError("Could not record your decision.");
        setSubmitting(null);
      }
    },
    [authorizationId]
  );

  if (loading) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background">
        <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background px-6">
        <div className="max-w-md w-full space-y-4 text-center">
          <div className="mx-auto w-12 h-12 rounded-full bg-red-100 dark:bg-red-950/40 flex items-center justify-center">
            <ShieldAlert className="w-6 h-6 text-red-700 dark:text-red-400" />
          </div>
          <h1 className="text-lg font-semibold">Authorization failed</h1>
          <p className="text-sm text-muted-foreground">{error}</p>
        </div>
      </div>
    );
  }

  if (!details) return null;

  // The approval landed but the browser stayed put. The grant exists, so this
  // is a navigation problem, not an authorization one.
  if (pendingRedirect) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background px-6">
        <div className="max-w-md w-full space-y-4 text-center">
          <h1 className="text-lg font-semibold">You're all set</h1>
          <p className="text-sm text-muted-foreground">
            Return to {details.client.name} to finish connecting.
          </p>
          <Button
            variant="primary"
            onClick={() => {
              window.location.href = pendingRedirect;
            }}
          >
            Continue to {details.client.name}
          </Button>
        </div>
      </div>
    );
  }

  if (step === "connector") {
    return (
      <div className="min-h-screen bg-background overflow-y-auto px-6 py-10">
        {error && (
          <div className="max-w-2xl mx-auto mb-4 rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-3">
            <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
          </div>
        )}
        {/* The client's stable id, not its display name — a binding keyed on
            something the client controls would follow a rename. */}
        <ConnectorConsent
          clientId={details.client.id}
          clientName={details.client.name}
          submitLabel={`Allow ${details.client.name}`}
          busy={submitting === "approve"}
          onCancel={() => finish("deny")}
          onDone={() => finish("approve")}
        />
      </div>
    );
  }

  const scopes = details.scope
    .split(" ")
    .map((s) => s.trim())
    .filter(Boolean);

  return (
    <div className="flex items-center justify-center min-h-screen bg-background px-6 py-10">
      <div className="max-w-md w-full space-y-6">
        <div className="text-center space-y-2">
          <h1 className="text-xl font-semibold">
            Sign in to {details.client.name}?
          </h1>
          <p className="text-sm text-muted-foreground">
            {details.client.name} is asking to sign in using your Reliant
            account.
          </p>
        </div>

        <div className="border border-border/40 rounded-lg p-5 space-y-4">
          <div className="flex items-center justify-between text-sm">
            <span className="text-muted-foreground">Signing in as</span>
            <span className="font-medium truncate ml-3">
              {details.user.email}
            </span>
          </div>

          {details.client.uri && (
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground">Application</span>
              <a
                href={details.client.uri}
                target="_blank"
                rel="noreferrer noopener"
                className="font-medium truncate ml-3 inline-flex items-center gap-1 hover:underline"
              >
                {new URL(details.client.uri).hostname}
                <ExternalLink className="w-3 h-3" />
              </a>
            </div>
          )}

          {scopes.length > 0 && (
            <div className="space-y-2 pt-1">
              <p className="text-xs text-muted-foreground uppercase tracking-wide">
                It will be able to
              </p>
              <ul className="space-y-1.5">
                {scopes.map((scope) => (
                  <li key={scope} className="text-sm flex items-start gap-2">
                    <span className="text-muted-foreground mt-0.5">•</span>
                    <span>
                      {SCOPE_LABELS[scope] ?? (
                        <span className="font-mono text-xs">{scope}</span>
                      )}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>

        {/* Sets the expectation that one more decision follows, so "Continue"
            does not read as the end of the flow. */}
        <p className="text-xs text-muted-foreground">
          This confirms who you are. Next you will choose which workspace{" "}
          {details.client.name} may use, and what it may do there.
        </p>

        <div className="flex gap-3">
          <Button
            variant="primary"
            className="flex-1"
            onClick={() => setStep("connector")}
            disabled={submitting !== null}
          >
            Continue
          </Button>
          <Button
            variant="outline"
            className="flex-1"
            onClick={() => finish("deny")}
            disabled={submitting !== null}
            loading={submitting === "deny"}
          >
            Cancel
          </Button>
        </div>
      </div>
    </div>
  );
}
