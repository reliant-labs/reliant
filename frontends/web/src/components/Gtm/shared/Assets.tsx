// Copyright (c) 2026 Reliant Labs

/**
 * An item's or a campaign's assets (gtm_assets): a thumbnail for a ready
 * image, its file name, size and where it came from; download, delete (a
 * person's act) and upload.
 *
 * Upload is three steps (CreateAssetUpload): the service records a PENDING
 * asset and hands back a presigned PUT for the asset's staging key; the page
 * PUTs the file there with the headers it names; ConfirmAssetUpload checks
 * the bytes and makes the asset READY. Until asset storage is configured the
 * service answers Unavailable, and the panel says uploads are unavailable
 * rather than failing on every try.
 *
 * Download is GetAssetDownload: a short-lived URL, opened in a new tab.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { Bot, Download, FileText, Trash2, Upload, UserRound, Workflow } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { GtmAssetSource, GtmAssetStatus, type GtmAsset } from "@/gen/reliant/v1/gtm_pb";
import { useConfirmAssetUpload, useCreateAssetUpload, useDeleteAsset, useGetAssetDownload, useListAssets } from "@/hooks/gtm-service-hooks_gen";
import { whenOrgScopeReady } from "@/lib/orgScope";
import { toast } from "@/lib/toast-manager";

import { useGtmOrgId } from "./GtmShell";
import { reportGtmWriteError } from "./writes";

const SOURCE_ICON = { [GtmAssetSource.CHAT]: Bot, [GtmAssetSource.WORKFLOW]: Workflow, [GtmAssetSource.PERSON]: UserRound } as const;

export const UPLOADS_UNAVAILABLE = "Uploads are unavailable here until asset storage is set up.";

/** The largest file the page offers to upload: 25 MiB. Checked before CreateAssetUpload, so a large file is refused at once, not after a request. */
export const MAX_ASSET_BYTES = 25 * 1024 * 1024;

export const tooLargeMessage = (file: File) => `${file.name} is ${size(BigInt(file.size))}. Files up to 25 MB can be uploaded.`;

function size(bytes: bigint): string {
  const n = Number(bytes);
  return n >= 1024 * 1024 ? `${(n / (1024 * 1024)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`;
}

const isUnavailable = (error: unknown) => error instanceof ConnectError && error.code === Code.Unavailable;

/** What the assets are attached to: exactly one of an item and a campaign. */
export type AssetOwner = { itemId: string } | { campaignId: string };

export function AssetPanel({ owner, label = "Assets" }: { owner: AssetOwner; label?: string }) {
  const orgId = useGtmOrgId();
  const queryClient = useQueryClient();
  const assetsQ = useListAssets({ orgId, ...owner, pageSize: 100 }, { enabled: !!orgId });
  const createUpload = useCreateAssetUpload();
  const confirmUpload = useConfirmAssetUpload();
  const [uploading, setUploading] = useState<string | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const assets = assetsQ.data?.assets ?? [];

  async function upload(file: File) {
    if (file.size > MAX_ASSET_BYTES) {
      toast.warning(tooLargeMessage(file));
      if (input.current) input.current.value = "";
      return;
    }
    setUploading(file.name);
    try {
      const target = "itemId" in owner ? { case: "itemId" as const, value: owner.itemId } : { case: "campaignId" as const, value: owner.campaignId };
      const started = await createUpload.mutateAsync({ orgId, target, filename: file.name, contentType: file.type || "application/octet-stream", sizeBytes: BigInt(file.size) });
      const put = await fetch(started.uploadUrl, { method: "PUT", headers: started.requiredHeaders, body: file });
      if (!put.ok) throw new Error(`The upload did not go through (${put.status}). Try again.`);
      await confirmUpload.mutateAsync({ orgId, assetId: started.asset!.id });
      toast.success(`${file.name} uploaded`);
    } catch (error) {
      if (isUnavailable(error)) setUnavailable(true);
      else reportGtmWriteError(queryClient, error);
    } finally {
      setUploading(null);
      if (input.current) input.current.value = "";
    }
  }

  return (
    <section aria-label={label} data-testid="gtm-assets">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-ink">{label}</h3>
        <input
          ref={input}
          type="file"
          className="sr-only"
          // The Upload button opens it; the input itself is not a second tab stop.
          tabIndex={-1}
          aria-label={`Upload a file to ${label.toLowerCase()}`}
          onChange={(e) => {
            const file = e.target.files?.[0];
            if (file) void upload(file);
          }}
        />
        <Button variant="ghost" size="xs" leftIcon={<Upload />} onClick={() => input.current?.click()} loading={uploading !== null} disabled={unavailable}>
          Upload
        </Button>
      </div>
      {unavailable && (
        <p className="mb-2 text-xs text-ink-muted" data-testid="gtm-assets-unavailable">
          {UPLOADS_UNAVAILABLE}
        </p>
      )}
      {uploading && <p className="mb-2 text-xs text-ink-muted">Uploading {uploading}…</p>}
      {assetsQ.isError ? (
        <p className="text-sm text-ink-muted">The files could not be listed.</p>
      ) : assets.length === 0 ? (
        assetsQ.isPending ? null : <p className="text-sm text-ink-muted">No files yet.</p>
      ) : (
        <ul className="grid grid-cols-2 gap-3 sm:grid-cols-[repeat(auto-fill,minmax(11rem,13rem))]">
          {assets.map((a) => (
            <AssetCard key={a.id} asset={a} />
          ))}
        </ul>
      )}
    </section>
  );
}

function AssetCard({ asset }: { asset: GtmAsset }) {
  const orgId = useGtmOrgId();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const del = useDeleteAsset({ onSuccess: () => setDeleting(false), onError: (e) => (setDeleting(false), reportGtmWriteError(queryClient, e)) });
  const ready = asset.status === GtmAssetStatus.READY;
  const image = ready && asset.contentType.startsWith("image/");
  // GetAssetDownload is a mutation (it mints a short-lived URL): the
  // thumbnail asks for one once, when the card mounts; a failure leaves the
  // file icon. Download asks for a fresh one on each click.
  const thumb = useGetAssetDownload({ onError: () => {} });
  const getDownload = useGetAssetDownload({ onError: () => {} });
  const askedThumb = useRef(false);
  useEffect(() => {
    if (!image || !orgId || askedThumb.current) return;
    askedThumb.current = true;
    thumb.mutate({ orgId, assetId: asset.id });
  }, [image, orgId, asset.id, thumb]);
  const Source = SOURCE_ICON[asset.source as keyof typeof SOURCE_ICON] ?? FileText;

  async function download() {
    // Opened now, inside the click, so a popup blocker allows it; pointed at
    // the URL once it arrives, with no way back to this page.
    const tab = window.open("", "_blank");
    if (tab) tab.opener = null;
    try {
      await whenOrgScopeReady();
      const res = await getDownload.mutateAsync({ orgId, assetId: asset.id });
      if (tab) tab.location.href = res.downloadUrl;
      else window.open(res.downloadUrl, "_blank", "noopener,noreferrer");
    } catch (error) {
      tab?.close();
      if (isUnavailable(error)) toast.warning("Downloads are unavailable here until asset storage is set up.");
      else reportGtmWriteError(queryClient, error);
    }
  }

  return (
    <li className="overflow-hidden rounded-lg border border-border bg-surface" data-testid={`gtm-asset-${asset.id}`}>
      {image && thumb.data?.downloadUrl ? (
        <img src={thumb.data.downloadUrl} alt={asset.filename} className="aspect-video w-full bg-surface-sunken object-cover" />
      ) : (
        <div className="flex aspect-video items-center justify-center bg-surface-sunken">
          <FileText className="size-6 text-ink-muted" aria-hidden="true" />
        </div>
      )}
      <div className="space-y-1 px-3 py-2">
        <p className="truncate text-sm font-medium text-ink" title={asset.filename}>
          {asset.filename}
        </p>
        <p className="flex items-center gap-1.5 text-xs text-ink-muted">
          <Source className="size-3 shrink-0" aria-hidden="true" />
          <span className="truncate">{ready ? `${size(asset.sizeBytes)} · ${asset.uploadedBy || "Unknown"}` : "Waiting for its upload"}</span>
        </p>
        <div className="flex gap-1 pt-1">
          <Button variant="ghost" size="xs" leftIcon={<Download />} onClick={() => void download()} disabled={!ready} aria-label={`Download ${asset.filename}`}>
            Download
          </Button>
          <Button variant="ghost" size="xs" leftIcon={<Trash2 />} onClick={() => setDeleting(true)} aria-label={`Delete ${asset.filename}`}>
            Delete
          </Button>
        </div>
      </div>
      <ConfirmationDialog
        open={deleting}
        onCancel={() => setDeleting(false)}
        onConfirm={() => del.mutate({ orgId, assetId: asset.id })}
        title={`Delete ${asset.filename}?`}
        description="The file is removed for everyone in the org. Only a person can delete a file."
        confirmLabel="Delete"
        variant="danger"
        loading={del.isPending}
      />
    </li>
  );
}
