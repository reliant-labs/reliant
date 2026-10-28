// Copyright (c) 2025 Reliant Labs

/**
 * A webhook automation's URL, and its token when it was just created or
 * rotated (the only time the server returns it). For a webhook, the URL is
 * the real deliverable of creating it (report 03 §6.4), so it comes with a
 * copyable curl example too.
 */

import { useId, useState } from "react";
import { Check, Copy, KeyRound, RotateCw } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { triggerErrorMessage, webhookUrlForDisplay, type Trigger, type WebhookCredential } from "@/hooks/trigger-model";
import { useRotateWebhookToken } from "@/hooks/trigger-queries";
import { reliantApiBaseUrl } from "@/lib/connect";
import { TestEventPreview } from "./TestEventPreview";
import { cn } from "@/lib/utils";
import { fieldClass, hintClass, labelClass } from "../../workflow/run/runFormStyles";

/**
 * The API origin a bare `/hooks/…` webhook path is relative to.
 *
 * Not `typeof window !== "undefined" ? window.location.origin : ""`: that
 * minifies to `?window.location.origin:`, the exact text the release's
 * verify-bundle guard (.github/scripts/verify-bundle.mjs, "no dev-mode leak")
 * treats as a surviving same-origin transport branch, so it failed every
 * Electron release cut after this dialog landed.
 */
export function apiOrigin(): string {
  return reliantApiBaseUrl() ?? globalThis.location?.origin ?? "";
}

/**
 * A webhook trigger's URL, and its token when it was just created or
 * rotated (the only time the server returns it). Rotation invalidates the
 * old token at once.
 */
export function WebhookCredentialPanel({ trigger, credential: initial }: { trigger: Trigger; credential?: WebhookCredential }) {
  const rotateToken = useRotateWebhookToken();
  const [credential, setCredential] = useState(initial);
  const [rotating, setRotating] = useState(false);
  const [confirmRotate, setConfirmRotate] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const origin = apiOrigin();
  const url = webhookUrlForDisplay(trigger.webhookUrl, origin);
  const tokenUrl = credential ? webhookUrlForDisplay(credential.url, origin) : "";

  // Reliant wired the sender itself (a `source: alerting` webhook on a hosted
  // deployment): there is nothing to copy, and no token to rotate by hand.
  if (trigger.webhookManaged) {
    return (
      <p className="text-sm text-muted-foreground">
        Connected to your organization&apos;s alerts. There is no URL or token to set up; disabling this disconnects it.
      </p>
    );
  }

  const rotate = async () => {
    setRotating(true);
    setError(null);
    try {
      const result = await rotateToken.mutateAsync(trigger.id);
      setCredential(result.webhook);
      setConfirmRotate(false);
    } catch (err) {
      setError(triggerErrorMessage(err));
    } finally {
      setRotating(false);
    }
  };

  return (
    <div className="space-y-3">
      <CopyField label="Webhook URL" value={url} hint="POST here with Authorization: Bearer <token>, or sign the body with your HMAC secret." />
      {credential ? (
        <>
          <CopyField label="Token" value={credential.token} secret hint="Shown once. Store it in the sender now; it can't be shown again, only replaced." />
          {tokenUrl && <CopyField label="URL with token" value={tokenUrl} secret hint="For senders that can't set headers (Zapier). Treat it like the token." />}
          <CopyField
            label="Try it"
            value={curlExample(url, credential.token)}
            secret
            hint="Run this to send a test event: it starts a run right away."
          />
        </>
      ) : (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <KeyRound className="h-3.5 w-3.5" aria-hidden /> The token was shown when this was activated. Rotate it to get a new one.
        </p>
      )}
      {confirmRotate ? (
        <div role="alertdialog" aria-label="Rotate the token?" className="rounded-md border border-warning/40 bg-background px-3 py-2 text-sm">
          <p className="text-foreground">Rotate the token? Senders using the current one stop working immediately.</p>
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="ghost" onClick={() => setConfirmRotate(false)}>Keep it</Button>
            <Button size="sm" variant="danger" loading={rotating} onClick={() => void rotate()}>Rotate token</Button>
          </div>
        </div>
      ) : (
        <Button size="sm" variant="outline" leftIcon={<RotateCw className="h-3.5 w-3.5" />} onClick={() => setConfirmRotate(true)}>
          Rotate token
        </Button>
      )}
      <TestEventPreview trigger={trigger} />
      {error && <p role="alert" className="text-sm text-destructive-ink">{error}</p>}
    </div>
  );
}

function CopyField({ label, value, hint, secret = false }: { label: string; value: string; hint?: string; secret?: boolean }) {
  const id = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // The field stays selectable for a manual copy.
    }
  };
  return (
    <div>
      <label htmlFor={id} className={labelClass}>{label}</label>
      <div className="flex gap-2">
        <input id={id} readOnly value={value} onFocus={(e) => e.currentTarget.select()} className={cn(fieldClass, "font-mono text-xs", secret && "tracking-tight")} />
        <Button type="button" variant="outline" size="sm" onClick={() => void copy()} aria-label={`Copy ${label.toLowerCase()}`} leftIcon={copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}>
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
      {hint && <p className={hintClass}>{hint}</p>}
    </div>
  );
}

/** A copyable test delivery: the request body is attached to the run. */
export function curlExample(url: string, token: string): string {
  return `curl -X POST '${url}' -H 'Authorization: Bearer ${token}' -H 'Content-Type: application/json' -d '{"hello":"world"}'`;
}
