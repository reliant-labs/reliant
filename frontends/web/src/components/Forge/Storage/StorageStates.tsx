// Copyright (c) 2025 Reliant Labs

/**
 * The storage browser's states, each worded for what it means to the person
 * reading rather than for the Connect code behind it
 * (storageBrowser/errors.ts storageErrorKind). Drawn in the observability
 * states' frame (StateFrame), so a refusal here looks like one anywhere in
 * the console, the data browser's included.
 */

import type { ReactNode } from "react";
import { AlertTriangle, FileX, HardDrive, Link2Off, Lock, PlugZap, RefreshCw, SearchX, ShieldOff, Timer } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { storageErrorKind, type StorageErrorKind } from "@/services/controlPlane/storageBrowser/errors";

import { StateFrame } from "../Observability/ObsStates";

const KIND_ICON: Record<StorageErrorKind, ReactNode> = {
  "not-configured": <HardDrive className="h-4 w-4" />,
  denied: <Lock className="h-4 w-4" />,
  confirmation: <Lock className="h-4 w-4" />,
  "shape-unknown": <HardDrive className="h-4 w-4" />,
  unreachable: <PlugZap className="h-4 w-4" />,
  "not-previewable": <FileX className="h-4 w-4" />,
  "too-large": <FileX className="h-4 w-4" />,
  refused: <AlertTriangle className="h-4 w-4" />,
  "bad-token": <Link2Off className="h-4 w-4" />,
  invalid: <AlertTriangle className="h-4 w-4" />,
  "not-found": <SearchX className="h-4 w-4" />,
  busy: <Timer className="h-4 w-4" />,
  "too-slow": <Timer className="h-4 w-4" />,
  unavailable: <AlertTriangle className="h-4 w-4" />,
};

const code = (text: string) => <code className="font-mono text-ink">{text}</code>;

function errorCopy(kind: StorageErrorKind, env: string, subject: string): { title: string; body: ReactNode } {
  switch (kind) {
    case "not-configured":
      return {
        title: "This control plane can't browse storage yet",
        body: "It has no storage browser, or runs without the reader behind it. It will once it is upgraded and configured.",
      };
    case "denied":
      return {
        title: `You can't read ${env}'s files`,
        body: (
          <>
            Browsing an environment&apos;s buckets needs the {code("storage:read")} grant in this organization, separate from{" "}
            {code("db:query")}: files are often more sensitive than rows. An organization admin can give it to you. An access token is never
            admitted to an audited environment, even with the scope: sign in as yourself.
          </>
        ),
      };
    case "confirmation":
      return { title: `${env} is audited`, body: "Every read here needs a reason, and this one was sent without one." };
    case "shape-unknown":
      return {
        title: `${env}'s buckets aren't known yet`,
        body: (
          <>
            Reliant learns which buckets an environment has, and who may read them, from its recorded release. {env} has none yet: deploy it
            once ({code(`reliant forge env deploy ${env}`)}) and they appear here.
          </>
        ),
      };
    case "unreachable":
      return {
        title: `Reliant can't reach ${subject}`,
        body: "Only Reliant-hosted buckets can be browsed. Direct GCS, S3 and MinIO buckets are unavailable; a hosted bucket must also be provisioned and ready.",
      };
    case "not-previewable":
      return { title: "No preview for this object", body: "Only images (png, jpeg, gif, webp), text and JSON preview here. Download it to open it." };
    case "too-large":
      return { title: "Too large to preview", body: "Previews stop at 10 MiB for an image and 1 MiB for text. Download it to open it." };
    case "refused":
      return { title: `Couldn't read ${subject}`, body: "The control plane refused the read. Its words:" };
    case "bad-token":
      return {
        title: "That page of the listing can't be opened here",
        body: "A page link belongs to one bucket, folder and environment. Start from the first page.",
      };
    case "invalid":
      return { title: `Couldn't read ${subject}`, body: "The control plane refused the request. Its words:" };
    case "not-found":
      return {
        title: `${subject} isn't there`,
        body: `It does not exist in ${env}: it may have been deleted, or the link came from another environment, whose buckets hold other objects.`,
      };
    case "busy":
      return {
        title: "Too many reads at once",
        body: "The control plane limits how many storage reads one person makes a minute. Wait a moment and try again.",
      };
    case "too-slow":
      return { title: "The bucket took too long", body: "The storage backend answered too slowly. Try again." };
    case "unavailable":
      return { title: `Couldn't read ${subject}`, body: "The control plane or the storage backend did not answer." };
  }
}

/** A failed read, in words. `subject` names what was being read: "the uploads bucket", "avatars/ada.png". */
export function StorageError({
  error,
  env,
  subject,
  onRetry,
  retrying,
  actions,
  testId = "storage-error",
}: {
  error: unknown;
  env: string;
  subject: string;
  onRetry?: () => void;
  retrying?: boolean;
  actions?: ReactNode;
  testId?: string;
}) {
  const kind = storageErrorKind(error);
  const copy = errorCopy(kind, env, subject);
  const detail = kind === "invalid" || kind === "refused" || kind === "unavailable" ? obsErrorMessage(error) : "";
  const canRetry = onRetry && (kind === "busy" || kind === "too-slow" || kind === "unavailable");
  const neutral = kind === "shape-unknown" || kind === "not-previewable" || kind === "too-large" || kind === "busy";
  return (
    <div data-testid={testId} data-kind={kind}>
      <StateFrame
        testId={`${testId}-${kind}`}
        tone={neutral ? "neutral" : "error"}
        icon={KIND_ICON[kind]}
        title={copy.title}
        actions={
          canRetry || actions ? (
            <>
              {canRetry && (
                <Button variant="outline" size="sm" className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0" onClick={onRetry} loading={retrying} disabled={retrying} leftIcon={<RefreshCw className="h-3 w-3" />}>
                  Try again
                </Button>
              )}
              {actions}
            </>
          ) : undefined
        }
      >
        <p>{copy.body}</p>
        {detail && <p className="break-words font-mono text-2xs text-ink">{detail}</p>}
      </StateFrame>
    </div>
  );
}

/** A note that is not a failure: the frame, neutral. */
export function StorageNote({ icon, title, children, actions, testId }: { icon?: ReactNode; title: string; children?: ReactNode; actions?: ReactNode; testId: string }) {
  return (
    <StateFrame testId={testId} icon={icon ?? <HardDrive className="h-4 w-4" />} title={title} actions={actions}>
      {children}
    </StateFrame>
  );
}

/** Where a person learns to declare a bucket (forge.Bucket in the environment's KCL). */
export const STORAGE_BUCKETS_DOCS_URL = "https://docs.reliantlabs.io/forge/deploy-without-an-account#adding-file-uploads";

/**
 * The bucket list was read and the environment declares none. Only the
 * buckets an app declares are listed, so this says that, and says what is
 * deliberately missing: the platform's own storage (images, sites, backups)
 * is never a bucket here. Shown only after a successful read: loading and a
 * failed read are their own states, never "no buckets".
 */
export function StorageNoBuckets({ env }: { env: string }) {
  return (
    <StateFrame testId="storage-no-buckets" icon={<HardDrive className="h-4 w-4" />} title={`This project declares no storage buckets in ${env}`}>
      <p>Buckets an app declares in its config appear here: each {code("forge.Bucket")} in this environment&apos;s last recorded release.</p>
      <p>Platform storage (container images, static sites, backups) isn&apos;t listed here.</p>
      <p>
        <a href={STORAGE_BUCKETS_DOCS_URL} target="_blank" rel="noreferrer" className="text-primary-ink underline underline-offset-2" data-testid="storage-no-buckets-docs">
          How to declare a bucket
        </a>
      </p>
    </StateFrame>
  );
}

/** The environment's policy refuses every read: say whose decision that is and where it lives. */
export function StorageDenied({ env, unknown }: { env: string; unknown: boolean }) {
  return (
    <StateFrame
      testId="storage-policy-deny"
      icon={<ShieldOff className="h-4 w-4" />}
      title={unknown ? `${env} has no recorded storage access policy` : `${env} doesn't allow browsing its files`}
    >
      <p>
        {unknown ? (
          <>
            Reliant reads an environment&apos;s storage access policy from its last recorded release, and {env} has none it understands, so every
            read of its buckets is refused.
          </>
        ) : (
          <>Its storage access policy is {code("deny")}, so no one can list or download its objects here, including organization admins.</>
        )}
      </p>
      <p>
        The policy is part of the environment&apos;s declaration ({code("forge.DataAccess")}, {code("storage")}; unset, it follows{" "}
        {code("db_query")}): change it there and deploy to open it as {code("audited")} or {code("open")}.
      </p>
    </StateFrame>
  );
}

/** Rows of placeholder bars: a listing or a pane while it loads. */
export function StorageLoading({ rows = 8, testId = "storage-loading", label = "Loading" }: { rows?: number; testId?: string; label?: string }) {
  return (
    <div data-testid={testId} aria-busy="true" aria-label={label} className="space-y-2 p-3">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="h-5 animate-pulse rounded bg-border/60 motion-reduce:animate-none" style={{ width: `${94 - ((index * 17) % 34)}%` }} />
      ))}
    </div>
  );
}
