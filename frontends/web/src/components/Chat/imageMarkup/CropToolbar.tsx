import { cn } from '../../../lib/utils';
import type { CropAspect } from './cropMath';
import { BUTTON_ACTIVE, BUTTON_BASE, CloseButton, Divider, FloatingPill, type PillPlacement } from './toolbarParts';

const ASPECTS: Array<{ id: CropAspect; label: string }> = [
  { id: 'free', label: 'Free' },
  { id: '1:1', label: '1:1' },
  { id: '16:9', label: '16:9' },
];

interface CropToolbarProps {
  pill: PillPlacement;
  aspect: CropAspect;
  onAspect: (aspect: CropAspect) => void;
  onReset: () => void;
  onDone: () => void;
  onClose: () => void;
}

export function CropToolbar({ pill, aspect, onAspect, onReset, onDone, onClose }: CropToolbarProps) {
  return (
    <FloatingPill label="Crop tools" pill={pill}>
      {ASPECTS.map((a) => (
        <button
          key={a.id}
          type="button"
          aria-label={`Aspect ratio ${a.label}`}
          aria-pressed={aspect === a.id}
          className={cn(BUTTON_BASE, aspect === a.id && BUTTON_ACTIVE)}
          onClick={() => onAspect(a.id)}
        >
          {a.label}
        </button>
      ))}
      <button type="button" aria-label="Reset crop" className={BUTTON_BASE} onClick={onReset}>
        Reset
      </button>
      <Divider />
      <button
        type="button"
        aria-label="Done"
        className="inline-flex h-8 shrink-0 items-center justify-center rounded-full bg-primary px-3.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-11 touch:px-4"
        onClick={onDone}
      >
        Done
      </button>
      <CloseButton label="Cancel crop" onClick={onClose} />
    </FloatingPill>
  );
}
