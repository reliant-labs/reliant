import { useState } from "react";
import type { Attachment } from "../../api/client";
import { FileIcon } from "../ui/FileIcon";
import { ImagePreviewModal } from "../ui/ImagePreviewModal";
import { isImageMimeType, useAttachmentBlobUrls } from "./useAttachmentBlobUrls";

interface MessageAttachmentsProps {
  attachments: Attachment[];
  isUser?: boolean;
  className?: string;
}

// Chat thread attachment previews were 80x80 (h-20/w-20).
// Reduce to Tailwind h-8/w-8 per UX request.
//
// This is the size for a *reference* to a file the user attached — a chip, not
// the content. An image a tool GENERATED is content and is rendered much larger
// by MessageGeneratedImages, which is why that is a separate component rather
// than a size prop here: changing this constant would regress the request above.
const CHAT_THREAD_ATTACHMENT_PREVIEW_SIZE_CLASS = "h-8 w-8";

export function MessageAttachments({
  attachments,
  isUser = false,
  className = "",
}: MessageAttachmentsProps) {
  const [previewImage, setPreviewImage] = useState<{ url: string; filename: string } | null>(null);
  const { blobUrls, failedIds } = useAttachmentBlobUrls(attachments);

  if (!attachments || attachments.length === 0) return null;

  return (
    <>
      <div className={`flex flex-wrap gap-1 pt-1 ${className}`}>
        {attachments.map((attachment) => {
          const mimeType = attachment.mimeType || '';
          const isImage = isImageMimeType(mimeType);
          
          const blobUrl = blobUrls.get(attachment.id);
          const hasError = failedIds.has(attachment.id);
          const isLoading = !blobUrl && !hasError;
          
          return isImage ? (
            <div
              key={attachment.id}
              className="rounded-md overflow-hidden cursor-pointer transition-opacity duration-200 hover:opacity-90 hover:elevation-3"
              style={{
                border: '1px solid var(--chat-border)',
                backgroundColor: 'var(--chat-input-bg)',
              }}
              onClick={() => blobUrl && setPreviewImage({ url: blobUrl, filename: attachment.filename })}
              title={`${attachment.filename} - Click to view full size`}
            >
              {isLoading && (
                <div
                  className={`flex items-center justify-center ${CHAT_THREAD_ATTACHMENT_PREVIEW_SIZE_CLASS} text-3xs text-muted-foreground`}
                  aria-label="Loading image attachment"
                >
                  <span className="animate-pulse">•</span>
                </div>
              )}
              {blobUrl && (
                <img
                  src={blobUrl}
                  alt={attachment.filename}
                  className={`${CHAT_THREAD_ATTACHMENT_PREVIEW_SIZE_CLASS} object-cover`}
                />
              )}
              {hasError && (
                <div
                  className={`flex items-center justify-center ${CHAT_THREAD_ATTACHMENT_PREVIEW_SIZE_CLASS} text-3xs text-red-500`}
                  title="Failed to load image"
                  aria-label="Failed to load image attachment"
                >
                  !
                </div>
              )}
            </div>
          ) : (
            <div
              key={attachment.id}
              className="flex items-center gap-1 px-2 py-1 rounded-md text-xs"
              style={{
                backgroundColor: isUser
                  ? 'var(--chat-button-bg)'
                  : 'var(--secondary)',
                border: '1px solid var(--chat-border)',
                color: 'var(--chat-input-text)',
                opacity: isUser ? 0.9 : 0.8,
              }}
              title={attachment.filename}
            >
              <FileIcon mimeType={mimeType} />
              <span className="whitespace-nowrap">{attachment.filename}</span>
            </div>
          );
        })}
      </div>
      
      {previewImage && (
        <ImagePreviewModal
          isOpen={true}
          onClose={() => setPreviewImage(null)}
          imageUrl={previewImage.url}
          filename={previewImage.filename}
        />
      )}
    </>
  );
}
