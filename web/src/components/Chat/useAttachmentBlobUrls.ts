/**
 * Load image attachments as blob URLs.
 *
 * Extracted from MessageAttachments so the transcript's two image surfaces —
 * the small uploaded-file chips and the large inline generated images — share
 * ONE loading path. There is no HTTP route that serves attachment bytes
 * (`attachment.url` is vestigial), so the only way to get pixels is
 * `attachmentGrpc.getAttachmentAsBlob`, and a second copy of this effect would
 * be a second place for the revoke/cancel bookkeeping to drift.
 */

import { useEffect, useRef, useState } from "react";
import { attachmentGrpc } from "../../api/attachment-grpc";

/** Attachment fields this hook needs — narrower than the full Attachment. */
interface LoadableAttachment {
  id: string;
  mimeType?: string;
}

export interface AttachmentBlobUrls {
  /** attachment id -> blob URL, present once the bytes have arrived. */
  blobUrls: Map<string, string>;
  /** attachment ids whose fetch failed. */
  failedIds: Set<string>;
}

export function isImageMimeType(mimeType: string | undefined): boolean {
  return Boolean(mimeType?.startsWith("image/"));
}

export function useAttachmentBlobUrls(
  attachments: readonly LoadableAttachment[] | undefined,
): AttachmentBlobUrls {
  const [blobUrls, setBlobUrls] = useState<Map<string, string>>(new Map());
  const [failedIds, setFailedIds] = useState<Set<string>>(new Set());
  const blobUrlsRef = useRef<Map<string, string>>(blobUrls);
  blobUrlsRef.current = blobUrls;

  useEffect(() => {
    let cancelled = false;

    const loadImages = async () => {
      const loaded = new Map<string, string>();

      for (const attachment of attachments || []) {
        if (cancelled) break;
        if (!isImageMimeType(attachment.mimeType)) continue;
        if (blobUrlsRef.current.has(attachment.id)) continue;

        try {
          const blob = await attachmentGrpc.getAttachmentAsBlob(attachment.id);
          // Check before creating the URL: one we create after unmount has no
          // owner left to revoke it.
          if (cancelled) break;
          loaded.set(attachment.id, URL.createObjectURL(blob));
        } catch (error) {
          if (cancelled) break;
          console.error("Error loading image:", { id: attachment.id, error });
          setFailedIds((prev) => new Set(prev).add(attachment.id));
        }
      }

      if (cancelled) {
        loaded.forEach((url) => URL.revokeObjectURL(url));
        return;
      }
      if (loaded.size > 0) {
        setBlobUrls((prev) => new Map([...prev, ...loaded]));
      }
    };

    void loadImages();

    return () => {
      cancelled = true;
      blobUrlsRef.current.forEach((url) => URL.revokeObjectURL(url));
      blobUrlsRef.current = new Map();
    };
  }, [attachments]);

  return { blobUrls, failedIds };
}
