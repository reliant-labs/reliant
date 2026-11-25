import { memo } from 'react';
import {
  arrowHead,
  pinRadius,
  PIN_FILL,
  PIN_TEXT,
  strokePath,
  type MarkupDoc,
  type Shape,
} from './model';

/** Same face and line height as renderCanvas.ts, so the export matches the editor. */
export const TEXT_FONT_FAMILY = 'ui-sans-serif, system-ui, sans-serif';
export const TEXT_LINE_HEIGHT = 1.25;

interface ShapeViewProps {
  shape: Shape;
  width: MarkupDoc['width'];
  height: MarkupDoc['height'];
  opacity?: number;
}

const strokeProps = (color: string, size: number) => ({
  fill: 'none',
  stroke: color,
  strokeWidth: size,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
});

/** One shape as SVG, in image pixels. Never takes pointer events: hit-testing is the model's. */
export const ShapeView = memo(function ShapeView({ shape, width, height, opacity }: ShapeViewProps) {
  const common = { opacity, pointerEvents: 'none' as const };
  switch (shape.kind) {
    case 'stroke':
      return <path d={strokePath(shape.points)} {...strokeProps(shape.color, shape.size)} {...common} />;
    case 'rect':
      return <rect x={shape.x} y={shape.y} width={shape.w} height={shape.h} {...strokeProps(shape.color, shape.size)} {...common} />;
    case 'ellipse':
      return (
        <ellipse
          cx={shape.x + shape.w / 2}
          cy={shape.y + shape.h / 2}
          rx={shape.w / 2}
          ry={shape.h / 2}
          {...strokeProps(shape.color, shape.size)}
          {...common}
        />
      );
    case 'arrow': {
      const [a, b] = arrowHead(shape.from, shape.to, shape.size);
      const d = `M${shape.from.x} ${shape.from.y}L${shape.to.x} ${shape.to.y}M${shape.to.x} ${shape.to.y}L${a.x} ${a.y}M${shape.to.x} ${shape.to.y}L${b.x} ${b.y}`;
      return <path d={d} {...strokeProps(shape.color, shape.size)} {...common} />;
    }
    case 'text': {
      const filterId = `markup-shadow-${shape.id.replace(/[^a-zA-Z0-9_-]/g, '')}`;
      return (
        <g {...common}>
          <defs>
            <filter id={filterId} x="-20%" y="-20%" width="140%" height="140%">
              <feDropShadow dx="0" dy="0" stdDeviation={shape.fontSize / 12} floodColor="#000000" floodOpacity="0.6" />
            </filter>
          </defs>
          <g filter={`url(#${filterId})`}>
            {shape.text.split('\n').map((line, i) => (
              <text
                key={i}
                x={shape.x}
                y={shape.y + i * shape.fontSize * TEXT_LINE_HEIGHT}
                fill={shape.color}
                fontSize={shape.fontSize}
                fontFamily={TEXT_FONT_FAMILY}
                dominantBaseline="hanging"
                style={{ whiteSpace: 'pre' }}
              >
                {line === '' ? ' ' : line}
              </text>
            ))}
          </g>
        </g>
      );
    }
    case 'pin': {
      const r = pinRadius({ width, height });
      return (
        <g {...common}>
          <circle cx={shape.x} cy={shape.y} r={r} fill={PIN_FILL} stroke="#ffffff" strokeWidth={2} />
          <text
            x={shape.x}
            y={shape.y}
            fill={PIN_TEXT}
            fontSize={Math.round(r)}
            fontWeight="bold"
            fontFamily={TEXT_FONT_FAMILY}
            textAnchor="middle"
            dominantBaseline="central"
          >
            {shape.n}
          </text>
        </g>
      );
    }
  }
});
