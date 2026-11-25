import { useRef, type PointerEvent as ReactPointerEvent, type ReactNode, type Ref } from 'react';
import { GripVertical, Loader2, X } from 'lucide-react';
import Tooltip from '../../forge-ui/tooltip';
import { cn } from '../../../lib/utils';

/**
 * The editor sits inside the preview dialog (z-[9999]). forge-ui portals its
 * tooltips (z-[60]) and popovers (z-50) to <body>, which would put them
 * UNDER the dialog, so every one of them lifts itself above it.
 */
export const ABOVE_DIALOG = '!z-[10000]';

export interface PillPlacement {
  offset: { x: number; y: number };
  onOffsetChange: (offset: { x: number; y: number }) => void;
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

/** The dark rounded pill every toolbar is: bottom-centre by default, draggable by its handle, kept inside the viewport. */
export function FloatingPill({ label, pill, children }: { label: string; pill: PillPlacement; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const drag = useRef<{ px: number; py: number; ox: number; oy: number; rect: DOMRect } | null>(null);

  const onDown = (e: ReactPointerEvent<HTMLButtonElement>) => {
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    e.preventDefault();
    try {
      e.currentTarget.setPointerCapture?.(e.pointerId);
    } catch {
      // A synthetic pointer has nothing to capture.
    }
    drag.current = {
      px: e.clientX,
      py: e.clientY,
      ox: pill.offset.x,
      oy: pill.offset.y,
      rect: ref.current?.getBoundingClientRect() ?? new DOMRect(),
    };
  };
  const onMove = (e: ReactPointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d) return;
    const dx = clamp(e.clientX - d.px, -d.rect.left, window.innerWidth - d.rect.right);
    const dy = clamp(e.clientY - d.py, -d.rect.top, window.innerHeight - d.rect.bottom);
    pill.onOffsetChange({ x: d.ox + dx, y: d.oy + dy });
  };
  const onUp = () => {
    drag.current = null;
  };

  return (
    <div
      ref={ref}
      role="toolbar"
      aria-label={label}
      className="absolute bottom-6 left-1/2 z-20 flex max-w-[calc(100%-1rem)] items-center gap-1 rounded-full border border-white/15 bg-black/85 px-2 py-1.5 text-white shadow-lg backdrop-blur touch:py-2"
      style={{ transform: `translate(calc(-50% + ${pill.offset.x}px), ${pill.offset.y}px)` }}
      onPointerDown={(e) => e.stopPropagation()}
      onClick={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        aria-label="Move toolbar"
        className="flex h-8 w-5 shrink-0 cursor-grab touch-none items-center justify-center rounded-full text-white/60 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring active:cursor-grabbing touch:h-11 touch:w-7"
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={onUp}
      >
        <GripVertical className="h-4 w-4" aria-hidden />
      </button>
      {children}
    </div>
  );
}

export function Divider() {
  return <span aria-hidden className="mx-0.5 h-5 w-px shrink-0 bg-white/20" />;
}

export const BUTTON_BASE =
  'inline-flex h-8 min-w-8 shrink-0 items-center justify-center gap-1.5 rounded-full px-2 text-sm font-medium text-white transition-colors hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent touch:h-11 touch:min-w-11';
export const BUTTON_ACTIVE = 'bg-white/20';

interface ToolButtonProps {
  label: string;
  icon: ReactNode;
  /** A visible caption beside the icon (the entry toolbar). */
  caption?: string;
  /** Present for a toggle: drives aria-pressed and the active background. */
  pressed?: boolean;
  disabled?: boolean;
  shortcut?: string;
  onClick: () => void;
}

export function ToolButton({ label, icon, caption, pressed, disabled, shortcut, onClick }: ToolButtonProps) {
  return (
    <Tooltip content={label} shortcut={shortcut} side="top" delay={300} tooltipClassName={ABOVE_DIALOG}>
      {(tp) => (
        <button
          {...tp}
          type="button"
          aria-label={label}
          aria-pressed={pressed}
          disabled={disabled}
          className={cn(BUTTON_BASE, pressed && BUTTON_ACTIVE)}
          onClick={onClick}
        >
          {icon}
          {caption ? <span>{caption}</span> : null}
        </button>
      )}
    </Tooltip>
  );
}

export function SaveButton({ dirty, saving, onSave }: { dirty: boolean; saving: boolean; onSave: () => void }) {
  return (
    <button
      type="button"
      aria-label="Save"
      disabled={!dirty || saving}
      aria-busy={saving || undefined}
      onClick={onSave}
      className="inline-flex h-8 shrink-0 items-center justify-center gap-1.5 rounded-full bg-primary px-3.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-40 touch:h-11 touch:px-4"
    >
      {saving ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> : null}
      Save
    </button>
  );
}

export function CloseButton({ label = 'Close editor', onClick }: { label?: string; onClick: () => void }) {
  return <ToolButton label={label} icon={<X className="h-4 w-4" aria-hidden />} onClick={onClick} />;
}

export function mergeRefs<T>(...refs: Array<Ref<T> | undefined>): (node: T | null) => void {
  return (node) => {
    for (const ref of refs) {
      if (typeof ref === 'function') ref(node);
      else if (ref) (ref as { current: T | null }).current = node;
    }
  };
}
