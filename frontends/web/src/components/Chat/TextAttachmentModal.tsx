import { useEffect, useState } from "react";
import Modal from "../forge-ui/modal";
import Textarea from "../forge-ui/textarea";
import Button from "../forge-ui/button";
import { fetchAttachmentBlob } from "../../hooks/attachment-queries";
import { focusChatInput } from "../../hooks/useFocusManager";
import { isTextFile } from "../../lib/filetypes";

/** Whether an attachment opens in the text viewer rather than as an image or not at all. */
export function isTextAttachment(filename: string, mimeType: string | undefined): boolean {
  return Boolean(mimeType?.startsWith("text/")) || isTextFile(filename);
}

export const textCounts = (text: string) => ({
  characters: Array.from(text).length,
  lines: text.split(/\r\n|\r|\n/).length,
});

/**
 * A text attachment, opened from its chip: the composer's own draft
 * attachments open editable (Save replaces the attachment, Insert puts the
 * text back in the message), a sent message's open read-only.
 *
 * The text comes from the local blob URL when this session uploaded it, and
 * from the server otherwise (a sent attachment, or a draft restored after a
 * reload).
 */
export function TextAttachmentModal({
  attachmentId,
  filename,
  blobUrl,
  onClose,
  onSave,
  onInsert,
}: {
  attachmentId: string;
  filename: string;
  blobUrl?: string;
  onClose: () => void;
  /** Present: the text is editable and Save calls this. Rejecting keeps the dialog open. */
  onSave?: (text: string) => Promise<void>;
  /** Present: offers "Insert as text", which hands the (edited) text back. */
  onInsert?: (text: string) => void;
}) {
  const [original, setOriginal] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setOriginal(null);
    setLoadError(false);
    const load = blobUrl
      ? fetch(blobUrl).then((response) => response.text())
      : fetchAttachmentBlob(attachmentId).then((blob) => blob.text());
    load.then(
      (content) => {
        if (cancelled) return;
        setOriginal(content);
        setText(content);
      },
      () => {
        if (!cancelled) setLoadError(true);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [attachmentId, blobUrl]);

  const editable = Boolean(onSave);
  const loaded = original !== null;
  const dirty = loaded && text !== original;
  const counts = textCounts(text);

  const save = async () => {
    if (!onSave || !dirty) return;
    setSaving(true);
    try {
      await onSave(text);
      onClose();
    } catch {
      // The store already raised a toast; the edit stays in the dialog.
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      fallbackFocus={focusChatInput}
      title={filename}
      size="xl"
      footer={
        loaded ? (
          <div className="forge-ui flex w-full flex-wrap items-center gap-3">
            <span className="mr-auto text-xs text-muted-foreground" data-testid="text-attachment-counts">
              {counts.lines.toLocaleString()} lines · {counts.characters.toLocaleString()} characters
            </span>
            {onInsert && (
              <Button variant="outline" onClick={() => onInsert(text)} disabled={saving}>
                Insert as text
              </Button>
            )}
            {editable && (
              <Button onClick={() => void save()} disabled={!dirty} loading={saving}>
                Save
              </Button>
            )}
          </div>
        ) : undefined
      }
    >
      {loadError ? (
        <p role="alert" className="text-sm text-destructive-ink">
          Couldn't load {filename}. It may have been removed.
        </p>
      ) : !loaded ? (
        <p role="status" className="text-sm text-muted-foreground">
          Loading…
        </p>
      ) : (
        <Textarea
          aria-label={editable ? `Edit ${filename}` : filename}
          value={text}
          readOnly={!editable}
          onChange={(e) => setText(e.target.value)}
          spellCheck={false}
          resize="none"
          className="forge-ui h-[60dvh] font-mono text-xs leading-relaxed max-sm:h-full"
        />
      )}
    </Modal>
  );
}
