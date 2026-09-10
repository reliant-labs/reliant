import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { supabase } from "@/lib/supabase";
import { useAuthStore } from "@/store/authStore";
import { isSafeReturnTo } from "@/lib/returnTo";
import { describeAuthError } from "@/lib/authErrors";
import { logger } from "@/lib/logger";
import { GradientBackground } from "./GradientBackground";
import { BrandMark } from "./icons/BrandMark";

/**
 * The single landing pad for every identity round-trip, of which there are now
 * two shapes — and which one arrives is NOT ours to choose.
 *
 * 1. `code`       — OAuth, and any email link sent under the PKCE flow.
 *                   Exchanged via exchangeCodeForSession.
 * 2. `token_hash` — a non-PKCE email confirmation link, i.e. a template built
 *    + `type`       on `{{ .TokenHash }}`. Verified via verifyOtp.
 *
 * Both must work because the Supabase email template lives in the hosted
 * dashboard, outside this repo: nobody here can see it, version it, or stop it
 * being reset to the stock `{{ .ConfirmationURL }}`. Handling only one shape
 * would leave a flow that breaks silently on a dashboard change nobody
 * connected to the outage.
 *
 * `code` is checked FIRST. GoTrue's own /verify redirect appends `code` under
 * PKCE, and a link can carry both; exchanging the code is the path that also
 * yields a session in one hop.
 */
export function OAuthCallback() {
  const navigate = useNavigate();
  const search = useSearch({ from: "/auth/callback" });
  const { setUser, setSession } = useAuthStore();
  const [error, setError] = useState<string | null>(null);
  const [alreadyRegistered, setAlreadyRegistered] = useState(false);
  const exchanged = useRef(false);

  // Return the user to where they came from (their still-intact anon session),
  // honoring only same-origin relative paths to avoid open-redirect issues.
  //
  // Client-side navigation, matching UpgradeAccount.goToReturnTo. Every
  // returnTo we honor is a route in THIS app, and window.location.assign tore
  // down the SPA for a full cold boot — which is what made returning from
  // /upgrade look like a page refresh that did nothing. `href` takes an
  // already-built path, so a returnTo carrying a query string (?tab=plans&plan=…,
  // the intent the billing flow now threads through) round-trips unchanged.
  const goBack = () => {
    if (isSafeReturnTo(search.returnTo)) {
      void navigate({ href: search.returnTo });
      return;
    }
    navigate({ to: "/", search: {} });
  };

  useEffect(() => {
    if (exchanged.current) return;
    exchanged.current = true;

    const handleCallback = async () => {
      const {
        code,
        token_hash: tokenHash,
        type,
        error: errorParam,
        error_description: errorDescription,
        returnTo,
      } = search;

      if (errorParam) {
        let friendlyMessage = "Authentication failed";

        if (
          errorParam === "identity_already_exists" ||
          errorDescription?.includes("already linked") ||
          errorDescription?.includes("already registered") ||
          errorDescription?.includes("Identity is already linked")
        ) {
          // Anon user tried to link an identity that already belongs to another
          // account. Do NOT sign them in as that user — that discards the anon
          // session's chats/code. Just inform them; their session stays intact.
          friendlyMessage =
            "This account is already registered. You can either sign in and out, or link a different account";
          setAlreadyRegistered(true);
        } else if (
          errorDescription?.includes("Multiple accounts") ||
          errorDescription?.includes("same email")
        ) {
          friendlyMessage =
            "An account with this email already exists. Please sign in with your existing method first, then link your OAuth provider in settings.";
        } else if (
          errorDescription?.includes("denied") ||
          errorDescription?.includes("cancelled")
        ) {
          friendlyMessage = "Authorization cancelled. Please try again.";
        } else if (errorDescription) {
          friendlyMessage = errorDescription.replace(/\+/g, " ");
        }

        logger.error("[OAuthCallback] Error from provider", {
          error: errorParam,
          errorDescription,
        });
        setError(friendlyMessage);
        return;
      }

      // A confirmation link may carry a code, or a token_hash + type, and
      // which one depends on a dashboard template we cannot read. Accept
      // either; refuse only when neither is present.
      if (!code && !(tokenHash && type)) {
        logger.error(
          "[OAuthCallback] Callback URL carried neither an authorization code nor a token_hash",
        );
        setError(
          "Invalid authentication callback. Please try signing in again.",
        );
        return;
      }

      try {
        let user;
        let session;

        if (code) {
          logger.info(
            "[OAuthCallback] Exchanging authorization code for session",
          );
          const { data, error: exchangeError } =
            await supabase.auth.exchangeCodeForSession(code);

          if (exchangeError) {
            logger.error("[OAuthCallback] Code exchange failed", exchangeError);
            // A missing PKCE verifier is not a transient failure and not a
            // misconfiguration — it is structural. The verifier is stored by
            // the browser that STARTED the flow; an email link is opened by
            // the user's mail client, in a different browser context, where
            // no verifier exists. The exchange can never succeed there.
            //
            // The user saw the raw `pkce_code_verifier_not_found` for this.
            // Say what to do instead: the 6-digit code in the same email is
            // stateless and works in any browser. Confirmation links now
            // arrive as `token_hash` (the template builds them from
            // {{ .TokenHash }}), so reaching this branch means a STALE link
            // from before that change.
            setError(
              describeAuthError(
                exchangeError,
                "We couldn't complete that sign-in link.",
              ),
            );
            return;
          }
          user = data.user;
          session = data.session;
        } else {
          // Email confirmation link, non-PKCE template. The hash IS the
          // credential, so no email is sent alongside it — GoTrue rejects a
          // token_hash accompanied by an email, and looks the user up from the
          // hash itself.
          logger.info("[OAuthCallback] Verifying email confirmation link", {
            type,
          });
          const { data, error: verifyError } = await supabase.auth.verifyOtp({
            token_hash: tokenHash!,
            type: type!,
          });

          if (verifyError) {
            logger.error(
              "[OAuthCallback] Confirmation link verification failed",
              verifyError,
            );
            setError(
              describeAuthError(
                verifyError,
                "We couldn't confirm that email link.",
              ),
            );
            return;
          }
          user = data.user;
          session = data.session;
        }

        setUser(user);
        setSession(session);

        logger.info("[OAuthCallback] post-callback session state", {
          provider: user?.app_metadata?.provider,
          hasProviderToken: !!session?.provider_token,
          userId: user?.id,
          isAnonymous: user?.is_anonymous,
          hasEmail: !!user?.email,
        });

        if (isSafeReturnTo(returnTo)) {
          // Restore the originating URL. This is what makes an email link feel
          // fixed rather than merely successful: onboarding's ENTIRE state is
          // the `plan` search param, so landing on "/" drops the user at step
          // one having already answered everything. Same-origin relative paths
          // only — `isSafeReturnTo` is the app's single open-redirect guard,
          // and `//evil.com` is protocol-relative, so the `!startsWith('//')`
          // half of it is load-bearing. See goBack above for why this is a
          // client-side navigate and not window.location.assign.
          void navigate({ href: returnTo });
          return;
        }

        navigate({ to: "/", search: {} });
      } catch (err) {
        logger.error("[OAuthCallback] Unexpected callback error", err);
        setError(err instanceof Error ? err.message : "Authentication failed");
      }
    };

    void handleCallback();
  }, [navigate, search, setSession, setUser]);

  if (error) {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div
          className="drag-region h-12 flex-shrink-0"
          style={{ WebkitAppRegion: "drag" } as React.CSSProperties}
        />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <BrandMark className="h-8 w-8" />
              <h2
                className={`text-xl font-semibold ${alreadyRegistered ? "text-foreground" : "text-destructive"}`}
              >
                {alreadyRegistered
                  ? "Account already registered"
                  : "Authentication Failed"}
              </h2>
            </div>
            <div
              className={
                alreadyRegistered
                  ? "rounded-lg bg-muted border border-border p-4"
                  : "rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4"
              }
            >
              <p
                className={`text-sm ${alreadyRegistered ? "text-muted-foreground" : "text-red-800 dark:text-red-200"}`}
              >
                {error}
              </p>
            </div>
            <button
              onClick={
                alreadyRegistered
                  ? goBack
                  : () =>
                      navigate({ to: "/auth", search: { redirect: undefined } })
              }
              className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 transition-colors"
            >
              {alreadyRegistered ? "Continue" : "Back to Sign In"}
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
      <GradientBackground />
      <div
        className="drag-region h-12 flex-shrink-0"
        style={{ WebkitAppRegion: "drag" } as React.CSSProperties}
      />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8">
          <div className="flex flex-col items-center gap-4">
            <BrandMark className="h-8 w-8" />
            <h2 className="text-lg font-medium">Completing sign in...</h2>
            <div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
          </div>
        </div>
      </div>
    </div>
  );
}
