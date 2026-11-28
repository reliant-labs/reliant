/**
 * Images a tool produced, rendered as content in the conversation.
 *
 * An image a tool generated is the OUTCOME of the turn, not a detail of how the
 * turn was executed, so it does not belong inside the tool-call card: that card
 * is collapsed by default and its attachment chips are 32px, which made a
 * generated image effectively invisible. This renders in the message flow
 * instead, at a size you can actually look at, positioned where the tool call
 * happened so text->image->text ordering survives.
 *
 * Deliberately a SEPARATE component from MessageAttachments rather than a size
 * prop on it. The two are different content types with different jobs: a user's
 * uploaded attachment is a *reference* to a file they are talking about (a chip,
 * intentionally small — that 32px was an explicit UX request), while this is the
 * artifact itself. They share the loading path via useAttachmentBlobUrls, not
 * the presentation.
 */

import { useRef, useState } from "react";
import { ImageOff } from "lucide-react";
import type { Attachment } from "../../types/chat";
import { cn } from "../../lib/utils";
import { ImagePreviewModal } from "./ImagePreviewModal";
import { isImageMimeType, useAttachmentBlobUrls } from "./useAttachmentBlobUrls";

interface MessageGeneratedImagesProps {
  attachments: Attachment[];
  /** One line under the image(s): what the user is looking at. */
  caption?: string;
  className?: string;
}

/**
 * A single image caps at a comfortable reading width rather than filling the
 * column — a generated image is usually square-ish, and full-width pushes the
 * rest of the turn off screen. Two or more tile so a set reads as a set.
 */
const SINGLE_IMAGE_MAX = "max-w-sm";

export function MessageGeneratedImages({
  attachments,
  caption,
  className,
}: MessageGeneratedImagesProps) {
  const [previewId, setPreviewId] = useState<string | null>(null);
  const previewTrigger = useRef<HTMLButtonElement>(null);

  const images = attachments.filter((attachment) =>
    isImageMimeType(attachment.mimeType),
  );
  const { blobUrls, blobs, failedIds } = useAttachmentBlobUrls(images);
  const availableImages = images.filter(({ id }) => blobUrls.has(id) && !failedIds.has(id));
  const previewIndex = availableImages.findIndex(({ id }) => id === previewId);
  const preview = availableImages[previewIndex];

  if (images.length === 0) return null;

  const isSingle = images.length === 1;

  const grid = (
    <div
      className={cn(
        caption ? undefined : "my-2",
        isSingle ? SINGLE_IMAGE_MAX : "grid grid-cols-2 gap-2",
        caption ? undefined : className,
      )}
      data-testid="message-generated-images"
    >
      {images.map((attachment) => {
        const blobUrl = blobUrls.get(attachment.id);
        const hasFailed = failedIds.has(attachment.id);

        if (hasFailed) {
          return (
            <div
              key={attachment.id}
              // aspect-video rather than a fixed height so the failed tile
              // occupies the same box the image would have, and the
              // transcript does not reflow if a retry later succeeds.
              className="flex aspect-video flex-col items-center justify-center gap-1.5 rounded-lg border border-border bg-muted/40 text-muted-foreground"
              role="img"
              aria-label={`${attachment.filename} failed to load`}
            >
              <ImageOff className="h-5 w-5" aria-hidden="true" />
              <span className="px-2 text-center text-2xs">
                Could not load image
              </span>
            </div>
          );
        }

        if (!blobUrl) {
          return (
            <div
              key={attachment.id}
              className="aspect-video animate-pulse rounded-lg border border-border bg-muted/40"
              aria-label={`Loading ${attachment.filename}`}
              aria-busy="true"
            />
          );
        }

        return (
          <button
            key={attachment.id}
            type="button"
            onClick={(event) => {
              previewTrigger.current = event.currentTarget;
              setPreviewId(attachment.id);
            }}
            // A button, not a div+onClick: click-to-expand has to be reachable
            // by keyboard, and this is the only affordance for seeing the
            // image at full size.
            className={cn(
              "group block overflow-hidden rounded-lg border border-border bg-muted/20",
              "transition-opacity hover:opacity-90",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
            )}
            title={`${attachment.filename} — click to view full size`}
          >
            <img
              src={blobUrl}
              alt={attachment.filename}
              // `contain` and an auto height: this is the artifact, so it must
              // not be cropped the way a thumbnail can be.
              className={cn(
                "h-auto w-full object-contain",
                !isSingle && "aspect-square",
              )}
            />
          </button>
        );
      })}
    </div>
  );

  return (
    <>
      {caption ? (
        <figure className={cn("my-2", isSingle && SINGLE_IMAGE_MAX, className)}>
          {grid}
          <figcaption className="mt-1 text-meta text-muted-foreground">{caption}</figcaption>
        </figure>
      ) : (
        grid
      )}

      <ImagePreviewModal
        isOpen={Boolean(preview)}
        onClose={() => setPreviewId(null)}
        restoreFocus={() => previewTrigger.current?.focus({ preventScroll: true })}
        imageUrl={preview ? blobUrls.get(preview.id) ?? "" : ""}
        filename={preview?.filename ?? ""}
        imageBlob={preview ? blobs.get(preview.id) : undefined}
        navigation={availableImages.length > 1 ? {
          index: previewIndex,
          total: availableImages.length,
          onSelect: (index) => setPreviewId(availableImages[index]?.id ?? null),
        } : undefined}
      />
    </>
  );
}
