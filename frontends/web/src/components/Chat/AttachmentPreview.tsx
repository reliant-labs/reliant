import { useEffect, useState, type ReactNode } from 'react';
import { RotateCw, X } from 'lucide-react';
import { cn } from '../../lib/utils';
import { logger } from '../../lib/logger';
import { fetchAttachmentBlob } from '../../hooks/attachment-queries';
import { FileIcon } from '../FileBrowser/FileIcon';
import { ImagePreviewModal, type ImageMarkupResult } from './ImagePreviewModal';
import { TextAttachmentModal, isTextAttachment } from './TextAttachmentModal';
import { isImageMimeType, useAttachmentBlobUrls } from './useAttachmentBlobUrls';
import { useAttachmentStore } from '../../store/attachmentStore';

interface Attachment {
  id: string;
  filename: string;
  size: number;
  mime_type: string;
  url: string;
}

/**
 * One chip in the composer's chip row (design doc §5.3): a body that opens the
 * thing the chip stands for, and a × that removes it. The × is the chip's
 * full height on touch (44px), so it is never a 12px target beside the body.
 */
export function ComposerChip({
  icon,
  label,
  onOpen,
  openLabel,
  onRemove,
  removeLabel,
  tone = 'default',
  children,
}: {
  icon?: ReactNode;
  label: ReactNode;
  /** Tap on the body. Absent: the body is plain text. */
  onOpen?: () => void;
  openLabel?: string;
  /** The ×. Absent: the chip cannot be removed from here. */
  onRemove?: () => void;
  removeLabel?: string;
  tone?: 'default' | 'warning';
  /** Extra controls between the label and the × (an upload's Retry). */
  children?: ReactNode;
}) {
  const body = (
    <>
      {icon && <span className="flex shrink-0 items-center">{icon}</span>}
      <span className="min-w-0 truncate">{label}</span>
    </>
  );
  return (
    <div
      className={cn(
        'flex h-8 max-w-[16rem] shrink-0 items-center rounded-full border text-xs touch:h-11',
        tone === 'warning'
          ? 'border-warning/40 bg-warning/10 text-warning-ink'
          : 'border-border bg-background text-foreground',
      )}
    >
      {onOpen ? (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onOpen();
          }}
          aria-label={openLabel}
          className="flex h-full min-w-0 items-center gap-1.5 rounded-full pl-2.5 pr-1 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {body}
        </button>
      ) : (
        <span className="flex h-full min-w-0 items-center gap-1.5 pl-2.5 pr-1">{body}</span>
      )}
      {children}
      {onRemove ? (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            e.preventDefault();
            onRemove();
          }}
          aria-label={removeLabel}
          className="flex h-full w-7 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:text-destructive-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:w-11"
        >
          <X className="h-3 w-3" aria-hidden />
        </button>
      ) : (
        <span className="w-1.5" aria-hidden />
      )}
    </div>
  );
}

/**
 * The attachments on the next message, as chips. An image chip carries a
 * thumbnail and opens the full preview on tap; a text chip opens the text,
 * editable while `onSave` is given.
 *
 * Renders chips only — no wrapper — so they share the composer's single chip
 * row with uploads, file references, the thread and the preset; with no
 * attachments it renders nothing and takes no space.
 */
export function AttachmentPreview({ attachments, onRemove, onSave, onInsert, onSaveImage, onRevertImage }: {
  attachments: Attachment[];
  onRemove: (id: string) => void;
  /** Replace a text attachment with edited text. Absent: text opens read-only. */
  onSave?: (id: string, filename: string, text: string) => Promise<void>;
  /** Put a text attachment's text into the message in its place. */
  onInsert?: (id: string, text: string) => void;
  /** Replace an image attachment with its marked-up copy. Absent: images open read-only. */
  onSaveImage?: (id: string, result: ImageMarkupResult) => Promise<void>;
  /** Put a marked-up image's original back. */
  onRevertImage?: (id: string) => Promise<void>;
}) {
  const [previewImage, setPreviewImage] = useState<{ id: string; url: string; filename: string } | null>(null);
  const [openText, setOpenText] = useState<Attachment | null>(null);
  const localBlobUrls = useAttachmentStore((state) => state.blobUrls);
  const localFiles = useAttachmentStore((state) => state.localFiles);
  // A draft restored after a reload has no local bytes: fetch its thumbnails.
  const { blobUrls: fetchedBlobUrls } = useAttachmentBlobUrls(
    attachments
      .filter((attachment) => !localBlobUrls.has(attachment.id))
      .map((attachment) => ({ id: attachment.id, mimeType: attachment.mime_type })),
  );

  if (attachments.length === 0) return null;

  // The open attachment can be replaced (Save) or sent while open; follow it out.
  const openTextAttachment = openText && attachments.some((a) => a.id === openText.id) ? openText : null;
  const openImage = previewImage && attachments.some((a) => a.id === previewImage.id) ? previewImage : null;

  return (
    <>
      {attachments.map((attachment) => {
        const isImage = isImageMimeType(attachment.mime_type);
        const isText = !isImage && isTextAttachment(attachment.filename, attachment.mime_type);
        const imageUrl = localBlobUrls.get(attachment.id) ?? fetchedBlobUrls.get(attachment.id);
        return (
          <ComposerChip
            key={attachment.id}
            icon={
              isImage && imageUrl ? (
                <img src={imageUrl} alt="" className="h-5 w-5 rounded object-cover touch:h-7 touch:w-7" />
              ) : (
                <FileIcon mimeType={attachment.mime_type} />
              )
            }
            label={attachment.filename}
            onOpen={
              isImage && imageUrl
                ? () => setPreviewImage({ id: attachment.id, url: imageUrl, filename: attachment.filename })
                : isText
                  ? () => setOpenText(attachment)
                  : undefined
            }
            openLabel={
              isImage || isText
                ? `${(isText && onSave) || (isImage && onSaveImage) ? 'Open' : 'Preview'} ${attachment.filename}`
                : undefined
            }
            onRemove={() => onRemove(attachment.id)}
            removeLabel={`Remove ${attachment.filename}`}
          />
        );
      })}

      {openImage && (
        <DraftImagePreview
          key={openImage.id}
          attachmentId={openImage.id}
          imageUrl={openImage.url}
          filename={openImage.filename}
          onClose={() => setPreviewImage(null)}
          onSave={onSaveImage && ((result) => onSaveImage(openImage.id, result))}
          onRevert={onRevertImage && (() => onRevertImage(openImage.id))}
        />
      )}

      {openTextAttachment && (
        <TextAttachmentModal
          attachmentId={openTextAttachment.id}
          filename={openTextAttachment.filename}
          localFile={localFiles.get(openTextAttachment.id)}
          onClose={() => setOpenText(null)}
          onSave={onSave && ((text) => onSave(openTextAttachment.id, openTextAttachment.filename, text))}
          onInsert={onInsert && ((text) => {
            setOpenText(null);
            onInsert(openTextAttachment.id, text);
          })}
        />
      )}
    </>
  );
}

/**
 * A draft image's preview, editable while `onSave` is given. The editor works
 * on the un-annotated original: the bytes kept from this session's upload or
 * an earlier markup, else (a draft restored after a reload) the attachment's
 * bytes from the server. Until those are in hand the preview is read-only.
 */
function DraftImagePreview({ attachmentId, imageUrl, filename, onClose, onSave, onRevert }: {
  attachmentId: string;
  imageUrl: string;
  filename: string;
  onClose: () => void;
  onSave?: (result: ImageMarkupResult) => Promise<void>;
  onRevert?: () => Promise<void>;
}) {
  const markup = useAttachmentStore((state) => state.markups.get(attachmentId));
  const localSource = useAttachmentStore(
    (state) => state.markups.get(attachmentId)?.source ?? state.localImages.get(attachmentId),
  );
  const [fetchedSource, setFetchedSource] = useState<Blob>();
  const source = localSource ?? fetchedSource;

  useEffect(() => {
    if (!onSave || localSource) return;
    let cancelled = false;
    fetchAttachmentBlob(attachmentId).then(
      (blob) => {
        if (!cancelled) setFetchedSource(blob);
      },
      (error) => logger.warn('Could not load the image to edit; it opens read-only:', attachmentId, error),
    );
    return () => {
      cancelled = true;
    };
  }, [attachmentId, localSource, onSave]);

  return (
    <ImagePreviewModal
      isOpen={true}
      onClose={onClose}
      imageUrl={imageUrl}
      filename={filename}
      edit={
        onSave && source
          ? {
              source,
              initialDoc: markup?.doc,
              onSave,
              onRevert: markup && onRevert ? onRevert : undefined,
            }
          : undefined
      }
    />
  );
}

/** A file still uploading, or one whose upload failed: progress, Retry, and × to cancel. */
export function UploadChip({ name, failed, onRetry, onCancel }: {
  name: string;
  failed: boolean;
  onRetry: () => void;
  onCancel: () => void;
}) {
  return (
    <div role="status" className="contents">
      <ComposerChip
        tone={failed ? 'warning' : 'default'}
        icon={failed ? undefined : <span className="h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent" aria-hidden />}
        label={`${name} · ${failed ? 'Upload failed' : 'Uploading…'}`}
        onRemove={onCancel}
        removeLabel={`Cancel ${name}`}
      >
        {failed && (
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              onRetry();
            }}
            aria-label={`Retry ${name}`}
            className="flex h-full w-7 shrink-0 items-center justify-center rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:w-11"
          >
            <RotateCw className="h-3 w-3" aria-hidden />
          </button>
        )}
      </ComposerChip>
    </div>
  );
}
