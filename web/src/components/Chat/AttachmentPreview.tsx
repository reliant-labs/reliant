import { useState, useEffect } from 'react';
import { X } from 'lucide-react';
import { FileIcon } from '../ui/FileIcon';
import { ImagePreviewModal } from '../ui/ImagePreviewModal';
import { useAttachmentStore } from '../../store/attachmentStore';

interface Attachment {
  id: string;
  filename: string;
  size: number;
  mime_type: string;
  url: string;
}

interface AttachmentPreviewProps {
  attachments: Attachment[];
  onRemove: (id: string) => void;
  className?: string;
}

const isImageMimeType = (mimeType: string): boolean => {
  return mimeType.startsWith('image/');
};

export function AttachmentPreview({ attachments, onRemove, className = '' }: AttachmentPreviewProps) {
  const [previewImage, setPreviewImage] = useState<{ url: string; filename: string } | null>(null);
  const [isTouchDevice, setIsTouchDevice] = useState(false);
  const getBlobUrl = useAttachmentStore((state) => state.getBlobUrl);

  // Detect touch device on mount
  useEffect(() => {
    const hasTouch = 'ontouchstart' in window || navigator.maxTouchPoints > 0;
    setIsTouchDevice(hasTouch);
  }, []);

  if (attachments.length === 0) return null;

  return (
    <>
      <div className={`flex flex-wrap gap-2 ${className}`}>
        {attachments.map((attachment) => {
          const isImage = isImageMimeType(attachment.mime_type);
          const blobUrl = getBlobUrl(attachment.id);
          const imageUrl = blobUrl || attachment.url;
          
          return isImage ? (
            <div
              key={attachment.id}
              className="relative group rounded overflow-hidden transition-all duration-200"
              style={{
                border: '1px solid var(--chat-border)',
                isolation: 'isolate',
              }}
            >
              <img
                src={imageUrl}
                alt={attachment.filename}
                className="h-20 w-20 object-cover cursor-pointer transition-opacity duration-200 hover:opacity-80"
                onClick={() => setPreviewImage({ url: imageUrl, filename: attachment.filename })}
                title={attachment.filename}
              />
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  e.preventDefault();
                  onRemove(attachment.id);
                }}
                className={`absolute top-0.5 right-0.5 p-1.5 rounded-full bg-black/60 hover:bg-black/80 transition-colors duration-100 ${
                  isTouchDevice ? 'opacity-90' : 'opacity-0 group-hover:opacity-100'
                }`}
                style={{
                  pointerEvents: 'auto',
                }}
                title="Remove attachment"
                aria-label="Remove attachment"
              >
                <X className="w-3.5 h-3.5 text-white" strokeWidth={2.5} />
              </button>
            </div>
          ) : (
            <div
              key={attachment.id}
              className="flex items-center gap-2 px-2 py-1 rounded text-xs transition-all duration-200 w-fit"
              style={{
                backgroundColor: 'var(--chat-button-bg)',
                border: '1px solid var(--chat-border)',
                color: 'var(--chat-input-text)',
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.backgroundColor = 'var(--chat-button-hover)';
                e.currentTarget.style.borderColor = 'var(--chat-border-streaming)';
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.backgroundColor = 'var(--chat-button-bg)';
                e.currentTarget.style.borderColor = 'var(--chat-border)';
              }}
            >
              <span className="flex-shrink-0"><FileIcon mimeType={attachment.mime_type} /></span>
              <span className="whitespace-nowrap">{attachment.filename}</span>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  e.preventDefault();
                  onRemove(attachment.id);
                }}
                className="flex-shrink-0 transition-opacity duration-200 hover:opacity-100"
                style={{
                  color: 'var(--chat-input-placeholder)',
                  opacity: 0.8,
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.color = 'var(--destructive)';
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.color = 'var(--chat-input-placeholder)';
                }}
              >
                <X className="w-3 h-3" />
              </button>
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
