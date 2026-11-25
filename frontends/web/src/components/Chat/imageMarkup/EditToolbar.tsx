import { Crop, MessageSquareDashed, PenLine } from 'lucide-react';
import { Divider, FloatingPill, SaveButton, ToolButton, type PillPlacement } from './toolbarParts';
import type { EditorMode } from './useMarkupEditor';

interface EditToolbarProps {
  pill: PillPlacement;
  onMode: (mode: EditorMode) => void;
  /** Edits not yet saved (a crop applied in crop mode, say): the pill then offers Save too. */
  dirty?: boolean;
  saving?: boolean;
  onSave?: () => void;
}

/** The entry pill shown over the image in view mode: pick Markup, Comment or Crop. */
export function EditToolbar({ pill, onMode, dirty, saving, onSave }: EditToolbarProps) {
  return (
    <FloatingPill label="Edit image" pill={pill}>
      <ToolButton label="Markup" caption="Markup" icon={<PenLine className="h-4 w-4" aria-hidden />} onClick={() => onMode('markup')} />
      <ToolButton label="Comment" caption="Comment" icon={<MessageSquareDashed className="h-4 w-4" aria-hidden />} onClick={() => onMode('comment')} />
      <ToolButton label="Crop" caption="Crop" icon={<Crop className="h-4 w-4" aria-hidden />} onClick={() => onMode('crop')} />
      {dirty && onSave ? (
        <>
          <Divider />
          <SaveButton dirty saving={saving ?? false} onSave={onSave} />
        </>
      ) : null}
    </FloatingPill>
  );
}
