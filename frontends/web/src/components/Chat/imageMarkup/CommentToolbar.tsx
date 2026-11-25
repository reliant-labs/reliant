import { CloseButton, Divider, FloatingPill, SaveButton, type PillPlacement } from './toolbarParts';

interface CommentToolbarProps {
  pill: PillPlacement;
  dirty: boolean;
  saving: boolean;
  onSave: () => void;
  onClose: () => void;
}

export function CommentToolbar({ pill, dirty, saving, onSave, onClose }: CommentToolbarProps) {
  return (
    <FloatingPill label="Comment tools" pill={pill}>
      <span className="px-2 text-sm text-white/80">Click on the image to add comments</span>
      <Divider />
      <SaveButton dirty={dirty} saving={saving} onSave={onSave} />
      <CloseButton onClick={onClose} />
    </FloatingPill>
  );
}
