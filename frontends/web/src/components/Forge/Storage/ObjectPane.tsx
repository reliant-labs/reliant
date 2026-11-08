// Copyright (c) 2025 Reliant Labs

/**
 * ONE OBJECT: its metadata (a HEAD), a preview when it qualifies, and
 * Download.
 *
 *   - An image (png, jpeg, gif, webp; never SVG) is drawn by an <img> from
 *     the 2-minute URL PreviewObject mints: never an iframe, never an
 *     <object>. Type/10 MiB limits are checked at stat time only: a writer
 *     can replace the object before GET because the URL is not generation-bound.
 *   - Text and JSON arrive as bytes (at most 1 MiB) and are shown as text,
 *     JSON re-indented; the browser never navigates to the object.
 *   - Anything else is metadata and Download only.
 *
 * Download mints a 5-minute attachment URL on the click
 * (PresignObjectDownload, a mutation) and hands it straight to the browser,
 * which downloads it as application/octet-stream without leaving the page.
 * The URL is never rendered, linked, cached or fetched ahead of a click: it
 * is a bearer credential to the object, and each one is an audit row and an
 * Activity line.
 */

import { useEffect, useState, type ReactNode } from "react";
import { Download, FileQuestion, ImageOff, RefreshCw, X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import type { ObjectInput, StorageObjectMeta, StoragePreview } from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { storageErrorKind } from "@/services/controlPlane/storageBrowser/errors";

import type { AccessConfirmation } from "../AuditedAccess/accessSession-model";
import { formatWhen } from "../Observability/format";
import { exactBytes, formatSize, previewKindOf, previewText, TEXT_PREVIEW_CAP } from "./storageObject-model";
import { StorageError, StorageLoading, StorageNote } from "./StorageStates";
import { basename } from "./storageUrl-model";
import { useObjectDownload, useObjectMetadata, useObjectPreview } from "./useStorageBrowser";

export interface ObjectPaneProps {
  env: { id: string; name: string };
  bucket: string;
  objectKey: string;
  access: AccessConfirmation | undefined;
  /** The view without the object open. */
  closeHref: string;
  onConfirmationLost: () => void;
  /** Hand a minted download URL to the browser. */
  onDownloadUrl: (url: string) => void;
}

export function ObjectPane({ env, bucket, objectKey, access, closeHref, onConfirmationLost, onDownloadUrl }: ObjectPaneProps) {
  const target: ObjectInput = { environmentId: env.id, bucket, key: objectKey, ...(access ? { access } : {}) };
  const metadata = useObjectMetadata(target);
  const download = useObjectDownload(onDownloadUrl);
  const name = basename(objectKey) || objectKey;

  const confirmationLost = [metadata.isFetching ? null : metadata.error, download.error].some((error) => error && storageErrorKind(error) === "confirmation");
  useEffect(() => {
    if (confirmationLost) onConfirmationLost();
  }, [confirmationLost, onConfirmationLost]);

  const startDownload = () => download.mutate(target);

  return (
    <Card padding="none" className="min-w-0" data-testid="storage-object-pane" data-key={objectKey}>
      <div className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <h2 className="truncate whitespace-pre font-mono text-sm font-medium text-ink" title={name}>
            {name}
          </h2>
          <p className="break-all font-mono text-2xs text-ink-muted" data-testid="storage-object-key">
            {objectKey}
          </p>
        </div>
        <Link href={closeHref} className="shrink-0 rounded p-1 text-ink-muted hover:bg-ink/5 hover:text-ink" aria-label="Close the object" data-testid="storage-object-close">
          <X className="h-4 w-4" />
        </Link>
      </div>

      <div className="space-y-4 px-4 py-3">
        {metadata.error ? (
          <StorageError
            error={metadata.error}
            env={env.name}
            subject={name}
            onRetry={() => void metadata.refetch()}
            retrying={metadata.isFetching}
            testId="storage-metadata-error"
          />
        ) : !metadata.data ? (
          <StorageLoading rows={5} testId="storage-metadata-loading" label={`Reading ${name}'s metadata`} />
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="primary"
                size="sm"
                onClick={startDownload}
                loading={download.isPending}
                disabled={download.isPending}
                leftIcon={<Download className="h-3.5 w-3.5" />}
                data-testid="storage-download"
              >
                Download
              </Button>
              <span className="text-xs text-ink-muted">{formatSize(metadata.data.size)}</span>
            </div>
            {download.error && storageErrorKind(download.error) !== "confirmation" && (
              <StorageError error={download.error} env={env.name} subject={name} testId="storage-download-error" />
            )}
            <Metadata meta={metadata.data} />
            <PreviewSection env={env} target={target} meta={metadata.data} onConfirmationLost={onConfirmationLost} />
          </>
        )}
      </div>
    </Card>
  );
}

function Row({ label, children, title }: { label: string; children: ReactNode; title?: string }) {
  return (
    <div className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3 py-1.5">
      <dt className="text-xs text-ink-muted">{label}</dt>
      <dd className="min-w-0 break-words text-xs text-ink" title={title}>
        {children}
      </dd>
    </div>
  );
}

function Metadata({ meta }: { meta: StorageObjectMeta }) {
  const user = Object.entries(meta.metadata).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  return (
    <div className="space-y-3" data-testid="storage-metadata">
      <dl className="divide-y divide-border/60">
        <Row label="Size" title={exactBytes(meta.size)}>
          {formatSize(meta.size)} <span className="text-ink-muted">({exactBytes(meta.size)})</span>
        </Row>
        <Row label="Content type">{meta.contentType ? <span className="font-mono">{meta.contentType}</span> : <span className="text-ink-muted">none stored</span>}</Row>
        <Row label="Modified" title={meta.updatedMs !== null ? new Date(meta.updatedMs).toISOString() : undefined}>
          {meta.updatedMs !== null ? formatWhen(meta.updatedMs) : "—"}
        </Row>
        <Row label="ETag">
          <span className="break-all font-mono">{meta.etag || "—"}</span>
        </Row>
      </dl>
      {user.length > 0 && (
        <section aria-label="User metadata">
          <h3 className="pb-1 text-xs font-medium text-ink-muted">User metadata</h3>
          <dl className="divide-y divide-border/60 rounded-md border border-border/60 bg-surface-sunken px-3" data-testid="storage-user-metadata">
            {user.map(([key, value]) => (
              <div key={key} className="grid grid-cols-[minmax(0,10rem)_minmax(0,1fr)] gap-3 py-1.5">
                <dt className="break-all font-mono text-2xs text-ink-muted">{key}</dt>
                <dd className="break-words font-mono text-2xs text-ink">{value}</dd>
              </div>
            ))}
          </dl>
        </section>
      )}
    </div>
  );
}

function PreviewSection({
  env,
  target,
  meta,
  onConfirmationLost,
}: {
  env: { id: string; name: string };
  target: ObjectInput;
  meta: StorageObjectMeta;
  onConfirmationLost: () => void;
}) {
  const kind = previewKindOf(meta);
  return (
    <section aria-label="Preview" className="space-y-2" data-testid="storage-preview" data-kind={kind}>
      <h3 className="text-xs font-medium text-ink-muted">Preview</h3>
      {kind === "none" ? (
        <StorageNote testId="storage-preview-none" icon={<FileQuestion className="h-4 w-4" />} title="No preview for this object">
          <p>
            Previews are for images (png, jpeg, gif, webp) checked at up to 10 MiB, and text and JSON up to 1 MiB.{" "}
            {meta.contentType ? (
              <>
                This one is <span className="font-mono text-ink">{meta.contentType}</span>.
              </>
            ) : (
              "This one has no stored content type."
            )}{" "}
            Download it to open it.
          </p>
        </StorageNote>
      ) : (
        <PreviewBody env={env} target={target} kind={kind} onConfirmationLost={onConfirmationLost} />
      )}
    </section>
  );
}

function PreviewBody({
  env,
  target,
  kind,
  onConfirmationLost,
}: {
  env: { id: string; name: string };
  target: ObjectInput;
  kind: "image" | "text" | "json";
  onConfirmationLost: () => void;
}) {
  const preview = useObjectPreview(target);
  const lost = !preview.isFetching && !!preview.error && storageErrorKind(preview.error) === "confirmation";
  useEffect(() => {
    if (lost) onConfirmationLost();
  }, [lost, onConfirmationLost]);

  if (preview.error) {
    return (
      <StorageError
        error={preview.error}
        env={env.name}
        subject={`the preview of ${basename(target.key) || target.key}`}
        onRetry={() => void preview.refetch()}
        retrying={preview.isFetching}
        testId="storage-preview-error"
      />
    );
  }
  if (!preview.data) return <StorageLoading rows={kind === "image" ? 6 : 8} testId="storage-preview-loading" label="Loading the preview" />;
  if (kind === "image") return <ImagePreview preview={preview.data} alt={basename(target.key) || target.key} onReload={() => void preview.refetch()} />;
  return <TextPreview preview={preview.data} kind={kind} />;
}

function ImagePreview({ preview, alt, onReload }: { preview: StoragePreview; alt: string; onReload: () => void }) {
  const [failed, setFailed] = useState<string | null>(null);
  const url = previewKindOf({ preview: preview.kind, contentType: preview.contentType }) === "image" ? (preview.image?.url ?? "") : "";
  if (!url || failed === url) {
    return (
      <StorageNote
        testId="storage-preview-image-failed"
        icon={<ImageOff className="h-4 w-4" />}
        title="The image didn't load"
        actions={
          <Button variant="outline" size="sm" onClick={() => { setFailed(null); onReload(); }} leftIcon={<RefreshCw className="h-3 w-3" />}>
            Load again
          </Button>
        }
      >
        <p>Its preview link lasts two minutes; loading again mints a new one.</p>
      </StorageNote>
    );
  }
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-border/60 bg-surface-sunken p-2">
      <img
        src={url}
        alt={alt}
        referrerPolicy="no-referrer"
        decoding="async"
        onError={() => setFailed(url)}
        className="max-h-[28rem] max-w-full object-contain"
        data-testid="storage-preview-image"
      />
      <p className="text-2xs text-ink-muted">Size and type were checked before this link was issued; the image may have changed since.</p>
    </div>
  );
}

function TextPreview({ preview, kind }: { preview: StoragePreview; kind: "text" | "json" }) {
  const shown = previewText(preview.content ?? new Uint8Array(), kind, preview.size);
  return (
    <div className="space-y-1.5">
      {(shown.cut || shown.unparsed) && (
        <p className="text-xs text-ink-muted" data-testid="storage-preview-note">
          {shown.cut && (
            <>
              Cut at {formatSize(TEXT_PREVIEW_CAP)}: the first {formatSize(preview.content?.byteLength ?? 0)} of {formatSize(preview.size)} is shown. Download it for the rest.
            </>
          )}
          {!shown.cut && shown.unparsed && "Not valid JSON, so it is shown as stored."}
        </p>
      )}
      <pre
        className="max-h-[32rem] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border/60 bg-surface-sunken p-3 font-mono text-xs text-ink"
        data-testid="storage-preview-text"
        data-pretty={shown.pretty ? "true" : "false"}
      >
        {shown.text}
      </pre>
    </div>
  );
}
