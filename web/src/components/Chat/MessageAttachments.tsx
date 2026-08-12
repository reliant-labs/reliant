import { useState, useEffect, useRef } from "react";
import type { Attachment } from "../../api/client";
import { FileIcon } from "../ui/FileIcon";
import { ImagePreviewModal } from "../ui/ImagePreviewModal";
import { attachmentGrpc } from "../../api/attachment-grpc";

interface MessageAttachmentsProps {
  attachments: Attachment[];
  isUser?: boolean;
  className?: string;
}

const isImageMimeType = (mimeType: string): boolean => {
  return mimeType.startsWith('image/');
};

// Chat thread attachment previews were 80x80 (h-20/w-20).
// Reduce to Tailwind h-8/w-8 per UX request.
const CHAT_THREAD_ATTACHMENT_PREVIEW_SIZE_CLASS = "h-8 w-8";

export function MessageAttachments({
  attachments,
  isUser = false,
  className = "",
}: MessageAttachmentsProps) {
  const [previewImage, setPreviewImage] = useState<{ url: string; filename: string } | null>(null);
  const [imageErrors, setImageErrors] = useState<Set<string>>(new Set());
  const [blobUrls, setBlobUrls] = useState<Map<string, string>>(new Map());
  const blobUrlsRef = useRef<Map<string, string>>(blobUrls);
  blobUrlsRef.current = blobUrls;

  // Load images via gRPC and convert to blob URLs
  useEffect(() => {
    let cancelled = false;

    const loadImages = async () => {
      const newBlobUrls = new Map<string, string>();

      for (const attachment of attachments) {
        if (cancelled) break;
        if (!isImageMimeType(attachment.mimeType || '')) {
          continue;
        }
        if (blobUrlsRef.current.has(attachment.id)) continue; // Already loaded
        
        try {
          // Fetch image via gRPC
          const blob = await attachmentGrpc.getAttachmentAsBlob(attachment.id);
          if (cancelled) {
            // Don't create blob URLs we'd never clean up
            break;
          }
          const blobUrl = URL.createObjectURL(blob);
          newBlobUrls.set(attachment.id, blobUrl);
        } catch (error) {
          if (cancelled) break;
          console.error('Error loading image:', {
            id: attachment.id,
            error,
          });
          setImageErrors(prev => new Set(prev).add(attachment.id));
        }
      }
      
      if (!cancelled && newBlobUrls.size > 0) {
        setBlobUrls(prev => new Map([...prev, ...newBlobUrls]));
      } else if (cancelled) {
        // Revoke any blob URLs created during this cancelled run
        newBlobUrls.forEach(url => URL.revokeObjectURL(url));
      }
    };

    loadImages();
    
    // Cleanup blob URLs when effect re-runs or component unmounts
    return () => {
      cancelled = true;
      blobUrlsRef.current.forEach(url => URL.revokeObjectURL(url));
      blobUrlsRef.current = new Map();
    };
  }, [attachments]);
  
  if (!attachments || attachments.length === 0) return null;



  return (
    <>
      <div className={`flex flex-wrap gap-1 pt-1 ${className}`}>
        {attachments.map((attachment) => {
          const mimeType = attachment.mimeType || '';
          const isImage = isImageMimeType(mimeType);
          
          const blobUrl = blobUrls.get(attachment.id);
          const hasError = imageErrors.has(attachment.id);
          const isLoading = !blobUrl && !hasError;
          
          return isImage ? (
            <div
              key={attachment.id}
              className="rounded-md overflow-hidden cursor-pointer transition-all duration-200 hover:opacity-90 hover:elevation-3"
              style={{
                border: '1px solid var(--chat-border)',
                backgroundColor: 'var(--chat-input-bg)',
              }}
              onClick={() => blobUrl && setPreviewImage({ url: blobUrl, filename: attachment.filename })}
              title={`${attachment.filename} - Click to view full size`}
            >
              {isLoading && (
                <div
                  className={`flex items-center justify-center ${CHAT_THREAD_ATTACHMENT_PREVIEW_SIZE_CLASS} text-[9px] text-muted-foreground`}
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
                  className={`flex items-center justify-center ${CHAT_THREAD_ATTACHMENT_PREVIEW_SIZE_CLASS} text-[9px] text-red-500`}
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
