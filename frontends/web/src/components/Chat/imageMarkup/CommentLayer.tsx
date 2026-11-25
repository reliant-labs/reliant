import { useCallback, useEffect, useImperativeHandle, useRef, useState, type Dispatch, type MouseEvent as ReactMouseEvent, type Ref } from 'react';
import { Trash2 } from 'lucide-react';
import { newShapeId, nextPinNumber, pinRadius, pinsOf, type CommentPin, type MarkupAction, type MarkupDoc } from './model';
import { clampPoint, clientToImage, type LayerHandle } from './surfaceCoords';

interface CommentLayerProps {
  doc: MarkupDoc;
  /** Screen pixels per image pixel. */
  scale: number;
  dispatch: Dispatch<MarkupAction>;
  ref?: Ref<LayerHandle>;
}

interface Editing {
  id: string;
  /** Dropped by this edit and not typed into yet: Escape takes it back with one undo. */
  fresh: boolean;
}

const INPUT_WIDTH = 240;

/**
 * Comment mode. The pins are drawn by the SVG surface; this layer sits above
 * it to take the clicks that drop a pin, to give each pin a real button, and
 * to host the inline "Add a comment…" input.
 */
export function CommentLayer({ doc, scale, dispatch, ref }: CommentLayerProps) {
  const layerRef = useRef<HTMLDivElement>(null);
  const [editing, setEditingState] = useState<Editing | null>(null);
  const editingRef = useRef<Editing | null>(null);
  const setEditing = useCallback((next: Editing | null) => {
    editingRef.current = next;
    setEditingState(next);
  }, []);
  const typed = useRef(false);

  const pins = pinsOf(doc);
  const radius = pinRadius(doc);
  const safeScale = scale > 0 ? scale : 1;

  const close = useCallback(() => {
    if (!editingRef.current) return;
    setEditing(null);
    dispatch({ type: 'commit' });
  }, [dispatch, setEditing]);

  const cancelEdit = useCallback((): boolean => {
    const cur = editingRef.current;
    if (!cur) return false;
    const pin = pinsOf(doc).find((p) => p.id === cur.id);
    setEditing(null);
    dispatch({ type: 'commit' });
    if (pin && pin.text === '' && cur.fresh) {
      if (typed.current) dispatch({ type: 'remove', ids: [pin.id] });
      else dispatch({ type: 'undo' });
    }
    return true;
  }, [dispatch, doc, setEditing]);

  useImperativeHandle(ref, () => ({ cancel: cancelEdit }), [cancelEdit]);

  // Leaving comment mode closes the input, keeping what was typed.
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => () => closeRef.current(), []);

  const onLayerClick = (e: ReactMouseEvent<HTMLDivElement>) => {
    if (e.target !== e.currentTarget) return;
    e.stopPropagation();
    const at = clampPoint(clientToImage(layerRef.current, e.clientX, e.clientY, doc), doc);
    if (editingRef.current) close();
    const pin: CommentPin = { kind: 'pin', id: newShapeId(), n: nextPinNumber(doc), x: at.x, y: at.y, text: '' };
    dispatch({ type: 'add', shape: pin });
    typed.current = false;
    setEditing({ id: pin.id, fresh: true });
  };

  const openPin = (pin: CommentPin) => {
    if (editingRef.current && editingRef.current.id !== pin.id) close();
    typed.current = false;
    setEditing({ id: pin.id, fresh: false });
  };

  const boxWidth = doc.width * safeScale;
  const editingPin = editing ? pins.find((p) => p.id === editing.id) : undefined;

  return (
    <div ref={layerRef} data-testid="comment-layer" className="absolute inset-0 z-10 cursor-crosshair" onClick={onLayerClick}>
      {pins.map((pin) => {
        const size = 2 * radius * safeScale;
        return (
          <button
            key={pin.id}
            type="button"
            aria-label={pin.text ? `Comment ${pin.n}: ${pin.text}` : `Comment ${pin.n}`}
            className="absolute rounded-full bg-transparent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
            style={{ left: pin.x * safeScale - size / 2, top: pin.y * safeScale - size / 2, width: size, height: size }}
            onClick={(e) => {
              e.stopPropagation();
              openPin(pin);
            }}
          />
        );
      })}
      {editing && editingPin ? (
        <PinInput
          key={editing.id}
          pin={editingPin}
          left={pinInputLeft(editingPin.x * safeScale, radius * safeScale, boxWidth)}
          top={editingPin.y * safeScale}
          onChange={(text) => {
            typed.current = true;
            dispatch({ type: 'update', id: editingPin.id, patch: { text }, coalesce: `pin:${editingPin.id}` });
          }}
          onCommit={close}
          onCancel={cancelEdit}
          onDelete={() => {
            setEditing(null);
            dispatch({ type: 'commit' });
            dispatch({ type: 'remove', ids: [editingPin.id] });
          }}
        />
      ) : null}
    </div>
  );
}

function pinInputLeft(pinX: number, pinR: number, boxWidth: number): number {
  const right = pinX + pinR + 8;
  if (right + INPUT_WIDTH <= boxWidth || pinX - pinR - 8 - INPUT_WIDTH < 0) return right;
  return pinX - pinR - 8 - INPUT_WIDTH;
}

interface PinInputProps {
  pin: CommentPin;
  left: number;
  top: number;
  onChange: (text: string) => void;
  onCommit: () => void;
  onCancel: () => void;
  onDelete: () => void;
}

function PinInput({ pin, left, top, onChange, onCommit, onCancel, onDelete }: PinInputProps) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    ref.current?.focus();
  }, []);
  return (
    <div
      className="absolute flex items-center gap-1 rounded-full border border-white/15 bg-black/85 py-1 pl-3 pr-1 text-white shadow-lg backdrop-blur"
      style={{ left, top, width: INPUT_WIDTH, transform: 'translateY(-50%)' }}
      onClick={(e) => e.stopPropagation()}
    >
      <input
        ref={ref}
        type="text"
        aria-label={`Comment ${pin.n} text`}
        placeholder="Add a comment…"
        value={pin.text}
        onChange={(e) => onChange(e.target.value)}
        onBlur={onCommit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            e.stopPropagation();
            onCommit();
          } else if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            onCancel();
          }
        }}
        className="min-w-0 flex-1 bg-transparent text-sm text-white outline-none placeholder:text-white/50"
      />
      <button
        type="button"
        aria-label={`Delete comment ${pin.n}`}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-white/70 hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-11 touch:w-11"
        onPointerDown={(e) => e.preventDefault()}
        onClick={onDelete}
      >
        <Trash2 className="h-3.5 w-3.5" aria-hidden />
      </button>
    </div>
  );
}
