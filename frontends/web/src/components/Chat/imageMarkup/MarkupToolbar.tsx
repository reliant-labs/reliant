import type { ReactNode } from 'react';
import { Check } from 'lucide-react';
import { Eraser, MousePointer2, PenLine, Redo2, Shapes, Square, Circle, MoveUpRight, Type, Undo2 } from 'lucide-react';
import Popover from '../../forge-ui/popover';
import Tooltip from '../../forge-ui/tooltip';
import { cn } from '../../../lib/utils';
import { MARKUP_COLORS } from './model';
import {
  ABOVE_DIALOG,
  BUTTON_ACTIVE,
  BUTTON_BASE,
  CloseButton,
  Divider,
  FloatingPill,
  mergeRefs,
  SaveButton,
  ToolButton,
  type PillPlacement,
} from './toolbarParts';
import type { MarkupTool, ShapeKind } from './useMarkupEditor';

const ICON = 'h-4 w-4';

const SHAPE_CHOICES: Array<{ kind: ShapeKind; label: string; icon: ReactNode }> = [
  { kind: 'rect', label: 'Rectangle', icon: <Square className={ICON} aria-hidden /> },
  { kind: 'ellipse', label: 'Ellipse', icon: <Circle className={ICON} aria-hidden /> },
  { kind: 'arrow', label: 'Arrow', icon: <MoveUpRight className={ICON} aria-hidden /> },
];

const PANEL = `${ABOVE_DIALOG} p-1.5`;

interface MarkupToolbarProps {
  pill: PillPlacement;
  tool: MarkupTool;
  onTool: (tool: MarkupTool) => void;
  shapeKind: ShapeKind;
  onShapeKind: (kind: ShapeKind) => void;
  color: string;
  onColor: (color: string) => void;
  canUndo: boolean;
  canRedo: boolean;
  onUndo: () => void;
  onRedo: () => void;
  dirty: boolean;
  saving: boolean;
  onSave: () => void;
  onClose: () => void;
}

export function MarkupToolbar(p: MarkupToolbarProps) {
  const shape = SHAPE_CHOICES.find((s) => s.kind === p.shapeKind) ?? SHAPE_CHOICES[0];
  return (
    <FloatingPill label="Markup tools" pill={p.pill}>
      <ToolButton label="Select" icon={<MousePointer2 className={ICON} aria-hidden />} pressed={p.tool === 'select'} onClick={() => p.onTool('select')} />
      <ToolButton label="Pen" icon={<PenLine className={ICON} aria-hidden />} pressed={p.tool === 'pen'} onClick={() => p.onTool('pen')} />
      <ToolButton label="Text" icon={<Type className={ICON} aria-hidden />} pressed={p.tool === 'text'} onClick={() => p.onTool('text')} />

      <Popover
        side="top"
        label="Shapes"
        panelClassName={PANEL}
        trigger={(pp) => (
          <Tooltip content="Shapes" side="top" delay={300} tooltipClassName={ABOVE_DIALOG}>
            {(tp) => (
              <button
                {...pp}
                {...tp}
                ref={mergeRefs(pp.ref, tp.ref)}
                type="button"
                aria-label="Shapes"
                aria-pressed={p.tool === 'shape'}
                className={cn(BUTTON_BASE, p.tool === 'shape' && BUTTON_ACTIVE)}
                onClick={(e) => {
                  p.onTool('shape');
                  pp.onClick(e);
                }}
              >
                {p.tool === 'shape' ? shape.icon : <Shapes className={ICON} aria-hidden />}
              </button>
            )}
          </Tooltip>
        )}
      >
        {({ close }) => (
          <div role="group" aria-label="Shape" className="flex gap-1">
            {SHAPE_CHOICES.map((s) => (
              <button
                key={s.kind}
                type="button"
                aria-label={s.label}
                aria-pressed={p.shapeKind === s.kind}
                className={cn(
                  'inline-flex h-8 w-8 items-center justify-center rounded-md text-foreground hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-11 touch:w-11',
                  p.shapeKind === s.kind && 'bg-foreground/10',
                )}
                onClick={() => {
                  p.onShapeKind(s.kind);
                  p.onTool('shape');
                  close();
                }}
              >
                {s.icon}
              </button>
            ))}
          </div>
        )}
      </Popover>

      <Popover
        side="top"
        label="Colour"
        panelClassName={PANEL}
        trigger={(pp) => (
          <Tooltip content="Colour" side="top" delay={300} tooltipClassName={ABOVE_DIALOG}>
            {(tp) => (
              <button {...pp} {...tp} ref={mergeRefs(pp.ref, tp.ref)} type="button" aria-label="Colour" className={BUTTON_BASE}>
                <span aria-hidden className="h-4 w-4 rounded-full border border-white/60" style={{ backgroundColor: p.color }} />
              </button>
            )}
          </Tooltip>
        )}
      >
        {({ close }) => (
          <div role="group" aria-label="Colours" className="grid grid-cols-4 gap-1.5">
            {MARKUP_COLORS.map((c) => (
              <button
                key={c}
                type="button"
                aria-label={`Colour ${c}`}
                aria-pressed={p.color === c}
                className="flex h-7 w-7 items-center justify-center rounded-full border border-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-11 touch:w-11"
                style={{ backgroundColor: c }}
                onClick={() => {
                  p.onColor(c);
                  close();
                }}
              >
                {p.color === c ? <Check className={ICON} style={{ color: c === '#ffffff' ? '#000000' : '#ffffff' }} aria-hidden /> : null}
              </button>
            ))}
          </div>
        )}
      </Popover>

      <ToolButton label="Eraser" icon={<Eraser className={ICON} aria-hidden />} pressed={p.tool === 'eraser'} onClick={() => p.onTool('eraser')} />
      <Divider />
      <ToolButton label="Undo" icon={<Undo2 className={ICON} aria-hidden />} disabled={!p.canUndo} onClick={p.onUndo} />
      <ToolButton label="Redo" icon={<Redo2 className={ICON} aria-hidden />} disabled={!p.canRedo} onClick={p.onRedo} />
      <SaveButton dirty={p.dirty} saving={p.saving} onSave={p.onSave} />
      <CloseButton onClick={p.onClose} />
    </FloatingPill>
  );
}
