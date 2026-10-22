// Copyright (c) 2025 Reliant Labs

/**
 * The one place a password-set code is shown: a link (or, when the
 * environment declares nothing to build a link from, the bare code) an admin
 * copies and hands to the person themselves — what an email would have
 * carried, for an identity that sends none.
 *
 * IT IS A CREDENTIAL. Whoever holds it can set that person's password until it
 * is redeemed or expires. So it is shown once, in this dialog only: never in a
 * toast, never logged, never kept after the dialog closes (the parent drops
 * its only copy on close, and the mutation that fetched it is not cached).
 *
 * A refusal lands here too, in context: returning a code needs the
 * identity:manage permission on top of the admin role, so an admin without it
 * is told so, with the server's own sentence, instead of a bare toast.
 */

import { useEffect, useRef, useState } from "react";
import { Check, Copy, KeyRound, ShieldOff } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { Button } from "@/components/ui/Button";
import type { IdentityPasswordCode } from "@/services/controlPlane/identity/api";
import { identityErrorKind, identityErrorMessage } from "@/services/controlPlane/identity/errors";

import { formatIn } from "../Observability/format";

export type IssuedCode =
  | { email: string; purpose: "invite" | "reset"; code: IdentityPasswordCode }
  | { email: string; purpose: "invite" | "reset"; error: unknown };

export function IdentityPasswordCodeDialog({ issued, onClose }: { issued: IssuedCode | null; onClose: () => void }) {
  if (!issued) return null;
  if ("error" in issued) return <Refused issued={issued} onClose={onClose} />;
  const { email, purpose, code } = issued;
  const link = code.resetUrl;
  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      closeOnOverlay={false}
      title={purpose === "invite" ? `Sign-up link for ${email}` : `Password reset link for ${email}`}
      description={
        link
          ? "Send this link to them yourself. Opening it lets them set a password."
          : "Send this code to them yourself. They enter it on your app's password-reset page."
      }
      footer={
        <Button size="sm" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="space-y-3 text-sm" data-testid="identity-code-dialog">
        <SecretField label={link ? "Link" : "Code"} value={link || code.code} testId={link ? "identity-code-link" : "identity-code-value"} />
        {!link && (
          <p className="text-xs text-muted-foreground" data-testid="identity-code-no-link">
            This environment&apos;s identity has no sign-in address to build a link from, so here is the bare code. The page
            that redeems it is <code className="font-mono text-foreground">/auth/reset</code> on your app.
          </p>
        )}
        <ul className="list-disc space-y-1 pl-4 text-xs text-muted-foreground">
          <li>
            It works once{code.expiresAtMs ? <>, and expires {formatIn(code.expiresAtMs)}</> : null}. Their current password,
            if they have one, keeps working until they use it.
          </li>
          <li>Anyone holding it can set their password, so send it only to them.</li>
          <li>It is shown only now. Reliant doesn&apos;t keep it, and the audit log records only that one was issued.</li>
        </ul>
      </div>
    </Modal>
  );
}

function SecretField({ label, value, testId }: { label: string; value: string; testId: string }) {
  const [copy, setCopy] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopy("copied");
    } catch {
      setCopy("failed"); // No clipboard (insecure context, denied): they can still select it.
    }
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopy("idle"), 2000);
  };
  return (
    <div className="space-y-1">
      <span className="text-xs font-medium text-foreground">{label}</span>
      <div className="flex items-stretch gap-2">
        <input
          readOnly
          value={value}
          aria-label={label}
          data-testid={testId}
          spellCheck={false}
          autoComplete="off"
          onFocus={(event) => event.currentTarget.select()}
          className="min-w-0 flex-1 rounded-md border border-border/60 bg-background px-2.5 py-1.5 font-mono text-xs text-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
        />
        <Button
          variant="outline"
          size="sm"
          onClick={() => void onCopy()}
          leftIcon={copy === "copied" ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        >
          {copy === "copied" ? "Copied" : "Copy"}
        </Button>
      </div>
      {copy === "failed" && (
        <p role="alert" className="text-xs text-destructive-ink">
          Couldn&apos;t reach the clipboard. Select the text and copy it by hand.
        </p>
      )}
    </div>
  );
}

function Refused({ issued, onClose }: { issued: Extract<IssuedCode, { error: unknown }>; onClose: () => void }) {
  const denied = identityErrorKind(issued.error) === "denied";
  const said = identityErrorMessage(issued.error);
  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      title={denied ? "You can't hand out password links here" : `Couldn't get a link for ${issued.email}`}
      footer={
        <Button size="sm" variant="outline" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="flex items-start gap-3 text-sm" data-testid="identity-code-refused">
        <span className="mt-0.5 shrink-0 text-muted-foreground" aria-hidden="true">
          {denied ? <ShieldOff className="h-4 w-4" /> : <KeyRound className="h-4 w-4" />}
        </span>
        <div className="min-w-0 space-y-2 text-xs text-muted-foreground">
          {denied && (
            <p className="text-foreground">
              A password link lets whoever holds it set someone&apos;s password, so it needs the{" "}
              <code className="font-mono">identity:manage</code> permission as well as the admin role. Ask an organization
              owner to grant it to you.
            </p>
          )}
          {said && <p className="break-words font-mono">{said}</p>}
          {issued.purpose === "invite" && (
            <p>If the account was created before the refusal, it shows in Users; get its link from there.</p>
          )}
        </div>
      </div>
    </Modal>
  );
}
