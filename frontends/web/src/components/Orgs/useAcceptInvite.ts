// Copyright (c) 2025 Reliant Labs

/**
 * The /accept-invite flow, as states. The page (AcceptInvitePage.tsx) only
 * renders them.
 *
 *   no link in storage ................. no-link
 *   signed out, or anonymous ........... signed-out   "Sign in to accept" → /auth → back here
 *   signed in:
 *     GetInvitation …
 *       NotFound ....................... invalid      (unknown or wrong token: one answer)
 *       email_unverified ............... unverified
 *       other failure .................. unavailable  (retry)
 *       status not pending ............. refused      (expired / revoked / used)
 *       already_member ................. already-member
 *       email does not match ........... mismatch     (said BEFORE any button)
 *       otherwise ...................... ready → AcceptInvitation → joined
 *
 * The token lives in storage (lib/inviteLink.ts) from boot until the flow
 * reaches an end: joined, or an invitation that can never be accepted. It is
 * kept across sign-in on purpose. It is never logged.
 */

import { useCallback, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { hasErrorReason } from "@/lib/errorReasons";
import { hardNavigate } from "@/lib/hardNavigate";
import { ACCEPT_INVITE_PATH, clearPendingInvite, readPendingInvite } from "@/lib/inviteLink";
import { logger } from "@/lib/logger";
import {
  acceptInvitation,
  getInvitation,
  isTerminalInviteRefusal,
  memberErrorMessage,
  type InvitationPreview,
} from "@/services/controlPlane/members";
import { switchToJoinedOrg, type MyOrg } from "@/services/controlPlane/orgs";
import { useAuthStore } from "@/store/authStore";

/** Where signing in returns to: this page, which picks the invitation up from storage. */
export const SIGN_IN_TO_ACCEPT_URL = `/auth?redirect=${encodeURIComponent(ACCEPT_INVITE_PATH)}`;

export type InviteRefusal = "expired" | "revoked" | "used";

export type AcceptView =
  | { kind: "loading" }
  | { kind: "no-link" }
  | { kind: "signed-out" }
  | { kind: "unverified" }
  | { kind: "invalid" }
  | { kind: "unavailable"; message: string }
  | { kind: "refused"; refusal: InviteRefusal }
  | { kind: "already-member"; preview: InvitationPreview }
  | { kind: "mismatch"; preview: InvitationPreview; signedInAs: string }
  | { kind: "ready"; preview: InvitationPreview; working: boolean; error: string | null }
  | { kind: "joined"; org: MyOrg };

function refusalOfStatus(status: string): InviteRefusal | null {
  if (status === "expired") return "expired";
  if (status === "revoked") return "revoked";
  if (status === "accepted") return "used";
  return null;
}

function refusalOfError(err: unknown): InviteRefusal | null {
  if (hasErrorReason(err, "invitation_expired")) return "expired";
  if (hasErrorReason(err, "invitation_revoked")) return "revoked";
  if (hasErrorReason(err, "invitation_not_pending")) return "used";
  return null;
}

const isNotFound = (err: unknown) => err instanceof ConnectError && err.code === Code.NotFound;

export interface AcceptInviteFlow {
  view: AcceptView;
  accept: () => void;
  /** Sign out of whatever this browser holds, sign in, and come back here. */
  signIn: () => void;
  signingIn: boolean;
  retry: () => void;
}

export function useAcceptInvite(): AcceptInviteFlow {
  // Read once per mount: the boot capture wrote it, and a different link
  // pasted into this tab reloads the page (inviteLinkBoot).
  const [token] = useState(() => readPendingInvite()?.token ?? null);
  const user = useAuthStore((s) => s.user);
  const initialized = useAuthStore((s) => s.initialized);
  const signOut = useAuthStore((s) => s.signOut);
  const signedIn = !!user && user.is_anonymous !== true;

  const preview = useQuery({
    queryKey: ["orgs-invite", "preview", user?.id ?? ""],
    queryFn: () => getInvitation(token as string),
    enabled: !!token && initialized && signedIn,
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });

  const [working, setWorking] = useState(false);
  const [acceptError, setAcceptError] = useState<string | null>(null);
  const [terminal, setTerminal] = useState<InviteRefusal | "already-member" | "mismatch" | null>(null);
  const [joined, setJoined] = useState<MyOrg | null>(null);
  const [signingIn, setSigningIn] = useState(false);

  const accept = useCallback(() => {
    if (!token || working) return;
    setWorking(true);
    setAcceptError(null);
    void (async () => {
      try {
        const org = await acceptInvitation(token);
        clearPendingInvite();
        // The joined org is where the person wants to be: act in it now.
        await switchToJoinedOrg(org.id);
        setJoined(org);
      } catch (err) {
        if (isTerminalInviteRefusal(err)) {
          clearPendingInvite();
          setTerminal(hasErrorReason(err, "already_member") ? "already-member" : refusalOfError(err));
        } else if (hasErrorReason(err, "invite_email_mismatch")) {
          // The invitation stays pending for the right person.
          setTerminal("mismatch");
        } else {
          logger.warn("[AcceptInvite] accept failed", { code: err instanceof ConnectError ? err.code : "unknown" });
          setAcceptError(memberErrorMessage(err, "accept", "We couldn't add you to the organization. Try again."));
        }
      } finally {
        setWorking(false);
      }
    })();
  }, [token, working]);

  const signIn = useCallback(() => {
    setSigningIn(true);
    void (async () => {
      try {
        if (useAuthStore.getState().user) await signOut();
      } catch (err) {
        // Signing in replaces the session anyway; a failed sign-out must not trap them here.
        logger.warn("[AcceptInvite] sign-out before signing in failed", { message: err instanceof Error ? err.message : "unknown" });
      }
      hardNavigate(SIGN_IN_TO_ACCEPT_URL);
    })();
  }, [signOut]);

  const retry = useCallback(() => {
    setAcceptError(null);
    void preview.refetch();
  }, [preview]);

  const view = ((): AcceptView => {
    if (joined) return { kind: "joined", org: joined };
    if (!token) return { kind: "no-link" };
    if (!initialized) return { kind: "loading" };
    if (!signedIn) return { kind: "signed-out" };
    if (preview.isPending) return { kind: "loading" };
    if (preview.isError) {
      const err = preview.error;
      if (isNotFound(err)) return { kind: "invalid" };
      if (hasErrorReason(err, "email_unverified")) return { kind: "unverified" };
      const refusal = refusalOfError(err);
      if (refusal) return { kind: "refused", refusal };
      return { kind: "unavailable", message: memberErrorMessage(err, "accept", "Check your connection and try again.") };
    }
    const p = preview.data;
    if (terminal === "already-member" || p.alreadyMember) return { kind: "already-member", preview: p };
    if (terminal && terminal !== "mismatch") return { kind: "refused", refusal: terminal };
    const status = refusalOfStatus(p.status);
    if (status) return { kind: "refused", refusal: status };
    if (terminal === "mismatch" || !p.emailMatches) return { kind: "mismatch", preview: p, signedInAs: user?.email ?? "" };
    return { kind: "ready", preview: p, working, error: acceptError };
  })();

  // An invitation that can never be accepted is forgotten, so a later visit
  // to this page does not offer it again.
  const finished = view.kind === "refused" || view.kind === "invalid" || view.kind === "already-member";
  useEffect(() => {
    if (finished) clearPendingInvite();
  }, [finished]);

  return { view, accept, signIn, signingIn, retry };
}
