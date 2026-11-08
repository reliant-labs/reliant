// Copyright (c) 2025 Reliant Labs

/**
 * THE STORAGE BROWSER for one environment: its buckets, one level of the
 * chosen one, and an object's metadata, preview and download. Read-only
 * (control-plane docs/storage-browser.md): there is no upload, delete or
 * rename, and the control plane reads with a viewer identity, so a write
 * would be refused by the backend, not by this page.
 *
 * Free of the console: the environment, the URL state and the way to change
 * it are props, so StoragePage mounts it in the console and the preview and
 * tests mount it on fixtures.
 *
 * In order, before any object is read:
 *   1. ListBrowsableBuckets names the buckets, each one's reachability, and
 *      the environment's policy (it reads no object and writes no audit row).
 *      One is chosen: the URL's, else the first that can be read.
 *   2. A deny policy, or a bucket that cannot be reached, says so.
 *   3. An AUDITED environment asks for a reason once per tab session
 *      (storageAccess-model), and every read then carries it.
 *   4. ListObjects lists the level; the object comes from the URL.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { HardDrive, ShieldCheck } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import type { StorageBucket } from "@/services/controlPlane/storageBrowser/storageBrowserApi";

import { AccessReasonDialog } from "../AuditedAccess/AccessReasonDialog";
import { isDenied, needsConfirmation, type AccessConfirmation } from "../AuditedAccess/accessSession-model";
import { BucketSelector, StoragePolicyTag } from "./BucketSelector";
import { ObjectListing } from "./ObjectListing";
import { ObjectPane } from "./ObjectPane";
import { clearStorageAccess, readStorageAccess, writeStorageAccess } from "./storageAccess-model";
import { StorageDenied, StorageError, StorageLoading, StorageNote } from "./StorageStates";
import { isBucketName, objectOf, patchStorageSearch, prefixOf, type StorageSearch } from "./storageUrl-model";
import { useBrowsableBuckets } from "./useStorageBrowser";

export interface StorageBrowserProps {
  /** The control-plane environment (deploy_environments.id) and its name. */
  env: { id: string; name: string };
  search: StorageSearch;
  /** Move to another view. `replace` for a correction (an auto-chosen bucket) rather than a step. */
  onNavigate: (next: StorageSearch, options?: { replace?: boolean }) => void;
  /** The URL of a view, for links (a folder, an object, a crumb). */
  href: (next: StorageSearch) => string;
  /** Hand a minted download URL to the browser. Default: navigate to it (an attachment downloads without leaving the page). */
  onDownloadUrl?: (url: string) => void;
  /** Entries per listing page; 0 lets the server choose (200). */
  pageSize?: number;
}

/** The bucket a bare link opens: the first that can be read, else the first. */
export function defaultBucket(buckets: readonly StorageBucket[]): StorageBucket | null {
  return buckets.find((bucket) => bucket.reachable) ?? buckets[0] ?? null;
}

const navigateTo = (url: string) => window.location.assign(url);

export function StorageBrowser({ env, search, onNavigate, href, onDownloadUrl = navigateTo, pageSize }: StorageBrowserProps) {
  const listed = useBrowsableBuckets(env.id);
  const buckets = useMemo(() => listed.data?.buckets ?? [], [listed.data]);
  const policy = listed.data?.policy ?? "unknown";
  const named = isBucketName(search.bucket) ? (buckets.find((bucket) => bucket.name === search.bucket) ?? null) : null;
  const selected = named ?? defaultBucket(buckets);

  // A bare link opens the default bucket. One naming a bucket this
  // environment does not have opens it at the root: its prefix and object
  // belonged to the other bucket.
  useEffect(() => {
    if (listed.data && selected && search.bucket !== selected.name) {
      onNavigate(search.bucket === undefined ? { ...search, bucket: selected.name } : patchStorageSearch(search, { bucket: selected.name }), { replace: true });
    }
  }, [listed.data, selected, search, onNavigate]);

  const [access, setAccess] = useState<AccessConfirmation | null>(() => readStorageAccess(env.id));
  const [asking, setAsking] = useState(false);
  const [confirmationRequired, setConfirmationRequired] = useState(false);
  const confirmationLost = useCallback(() => {
    clearStorageAccess(env.id);
    setAccess(null);
    setConfirmationRequired(true);
    setAsking(true);
  }, [env.id]);

  // A policy can change after the bucket list was read. The server's refusal
  // takes precedence over a cached OPEN policy.
  const audited = needsConfirmation(policy) || confirmationRequired;
  const waitingForReason = audited && !access;
  useEffect(() => {
    if (listed.data && waitingForReason) setAsking(true);
  }, [listed.data, waitingForReason]);
  const sentAccess = audited && access ? access : undefined;

  const go = useCallback((next: StorageSearch, options?: { replace?: boolean }) => onNavigate(next, options), [onNavigate]);

  if (listed.error) {
    return (
      <StorageError
        error={listed.error}
        env={env.name}
        subject={`${env.name}'s buckets`}
        onRetry={() => void listed.refetch()}
        retrying={listed.isFetching}
        testId="storage-buckets-error"
      />
    );
  }
  if (!listed.data) {
    return (
      <Card padding="none">
        <StorageLoading rows={4} testId="storage-buckets-loading" label={`Loading ${env.name}'s buckets`} />
      </Card>
    );
  }
  if (buckets.length === 0) {
    return (
      <StorageNote testId="storage-no-buckets" icon={<HardDrive className="h-4 w-4" />} title={`${env.name} has no buckets`}>
        <p>
          The browser shows the buckets an environment declares: each <code className="font-mono text-ink">forge.Bucket</code> in its last
          recorded release. {env.name}&apos;s declares none.
        </p>
      </StorageNote>
    );
  }

  const objectKey = objectOf(search);
  // Wait for the URL correction before starting reads: a missing bucket's
  // prefix and key must never be sent to the replacement bucket.
  const readable = !!selected && selected.name === search.bucket && selected.reachable && !isDenied(policy);

  return (
    <div className="min-w-0 space-y-4" data-testid="storage-browser" data-env={env.name} data-bucket={selected?.name ?? ""}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <BucketSelector buckets={buckets} selected={selected?.name ?? null} onSelect={(name) => go(patchStorageSearch(search, { bucket: name }))} />
        <div className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
          <StoragePolicyTag policy={policy} />
          {audited && access && (
            <span className="flex min-w-0 items-center gap-1.5" data-testid="storage-reason">
              <span className="max-w-[24rem] truncate" title={access.reason}>
                Reading as: <span className="text-ink">{access.reason}</span>
              </span>
              <button type="button" className="min-h-[44px] min-w-[44px] text-primary-ink hover:underline sm:min-h-0 sm:min-w-0" onClick={() => setAsking(true)} data-testid="storage-reason-change">
                Change
              </button>
            </span>
          )}
        </div>
      </div>

      {asking && audited && (
        <AccessReasonDialog
          open
          env={env.name}
          subject="files"
          description={`Every listing, preview and download in ${env.name} is recorded in the audit log with your name and this reason, and opening a bucket or downloading a file shows in the project's Activity. It's asked once for this tab.`}
          placeholder="Checking the avatar a customer reported in #4412"
          testIdPrefix="storage"
          initialReason={access?.reason ?? ""}
          onConfirm={(reason) => {
            setAccess(writeStorageAccess(env.id, reason));
            setAsking(false);
          }}
          onCancel={() => setAsking(false)}
        />
      )}

      {!selected ? null : isDenied(policy) ? (
        <StorageDenied env={env.name} unknown={policy === "unknown"} />
      ) : !selected.reachable ? (
        <StorageNote testId="storage-unreachable" title={`${selected.name} can't be browsed from Reliant`}>
          <p>{selected.unreachableReason || "The control plane has no way to read it."}</p>
        </StorageNote>
      ) : waitingForReason ? (
        <StorageNote
          testId="storage-needs-reason"
          icon={<ShieldCheck className="h-4 w-4" />}
          title={`${env.name} is audited: reads need a reason`}
          actions={
            <Button variant="primary" size="sm" className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0" onClick={() => setAsking(true)} data-testid="storage-give-reason">
              Give a reason
            </Button>
          }
        >
          <p>Nothing has been read yet. Each folder, preview and download is recorded with the reason you give, once, for this tab.</p>
        </StorageNote>
      ) : readable ? (
        <div className={objectKey ? "grid gap-4 xl:grid-cols-[minmax(0,1fr)_26rem]" : undefined}>
          <ObjectListing
            key={`${selected.name}|${prefixOf(search)}`}
            env={env}
            bucket={selected.name}
            search={search}
            access={sentAccess}
            onNavigate={go}
            href={href}
            onConfirmationLost={confirmationLost}
            pageSize={pageSize}
          />
          {objectKey && (
            <aside className="min-w-0 xl:sticky xl:top-4 xl:self-start" aria-label="Object">
              <ObjectPane
                key={`${selected.name}|${objectKey}`}
                env={env}
                bucket={selected.name}
                objectKey={objectKey}
                access={sentAccess}
                closeHref={href(patchStorageSearch(search, { object: undefined }))}
                onConfirmationLost={confirmationLost}
                onDownloadUrl={onDownloadUrl}
              />
            </aside>
          )}
        </div>
      ) : null}
    </div>
  );
}
