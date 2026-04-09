import React, { useRef, useLayoutEffect, useState, useCallback } from 'react';

interface ArchitectureDiagramProps {
  /** Groups of services */
  groups: Array<{
    label: string;
    color?: string;
    services: Array<{
      name: string;
      description?: string;
    }>;
  }>;
  /** Connections between services */
  connections: Array<{
    from: string;
    to: string;
    label?: string;
  }>;
}

interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

const DEFAULT_COLORS = [
  '#3b82f6',
  '#8b5cf6',
  '#10b981',
  '#f59e0b',
  '#ef4444',
  '#ec4899',
  '#06b6d4',
];

function getEdgePoint(rect: Rect, targetRect: Rect): { x: number; y: number } {
  const cx = rect.x + rect.width / 2;
  const cy = rect.y + rect.height / 2;
  const tx = targetRect.x + targetRect.width / 2;
  const ty = targetRect.y + targetRect.height / 2;

  const dx = tx - cx;
  const dy = ty - cy;
  const absDx = Math.abs(dx);
  const absDy = Math.abs(dy);

  // Determine which edge to exit from
  const hw = rect.width / 2;
  const hh = rect.height / 2;

  if (absDx * hh > absDy * hw && absDx > 0) {
    // Exit left or right
    const sign = dx > 0 ? 1 : -1;
    return { x: cx + sign * hw, y: cy + (dy * hw) / absDx };
  } else if (absDy > 0) {
    // Exit top or bottom
    const sign = dy > 0 ? 1 : -1;
    return { x: cx + (dx * hh) / absDy, y: cy + sign * hh };
  }

  return { x: cx, y: cy };
}

export default function ArchitectureDiagram({
  groups,
  connections,
}: ArchitectureDiagramProps): React.ReactElement {
  const containerRef = useRef<HTMLDivElement>(null);
  const [serviceRects, setServiceRects] = useState<Map<string, Rect>>(new Map());
  const [containerRect, setContainerRect] = useState<Rect>({ x: 0, y: 0, width: 0, height: 0 });

  const measurePositions = useCallback(() => {
    if (!containerRef.current) return;

    const cRect = containerRef.current.getBoundingClientRect();
    setContainerRect({ x: cRect.left, y: cRect.top, width: cRect.width, height: cRect.height });

    const rects = new Map<string, Rect>();
    const elements = containerRef.current.querySelectorAll<HTMLElement>('[data-service]');
    elements.forEach((el) => {
      const name = el.getAttribute('data-service');
      if (!name) return;
      const r = el.getBoundingClientRect();
      rects.set(name, {
        x: r.left - cRect.left,
        y: r.top - cRect.top,
        width: r.width,
        height: r.height,
      });
    });
    setServiceRects(rects);
  }, []);

  useLayoutEffect(() => {
    measurePositions();
    // Re-measure on resize
    const observer = new ResizeObserver(measurePositions);
    if (containerRef.current) observer.observe(containerRef.current);
    return () => observer.disconnect();
  }, [measurePositions, groups]);

  if (groups.length === 0) {
    return <div className="text-sm text-gray-400 italic">No groups provided</div>;
  }

  return (
    <div ref={containerRef} className="relative w-full">
      {/* Groups & services grid */}
      <div
        className="grid gap-6 w-full"
        style={{
          gridTemplateColumns: `repeat(${Math.min(groups.length, 3)}, 1fr)`,
        }}
      >
        {groups.map((group, gi) => {
          const color = group.color ?? DEFAULT_COLORS[gi % DEFAULT_COLORS.length];
          return (
            <div
              key={gi}
              className="rounded-xl border-2 p-4"
              style={{
                borderColor: `${color}40`,
                backgroundColor: `${color}08`,
              }}
            >
              {/* Group label */}
              <div className="flex items-center gap-2 mb-3">
                <div
                  className="w-3 h-3 rounded-full"
                  style={{ backgroundColor: color }}
                />
                <span
                  className="text-xs font-bold uppercase tracking-wider"
                  style={{ color }}
                >
                  {group.label}
                </span>
              </div>

              {/* Services */}
              <div className="flex flex-col gap-2">
                {group.services.map((service, si) => (
                  <div
                    key={si}
                    data-service={service.name}
                    className="bg-white rounded-lg border border-gray-200 px-3 py-2.5 shadow-sm hover:shadow-md transition-shadow"
                  >
                    <span className="text-sm font-semibold text-gray-800 block">
                      {service.name}
                    </span>
                    {service.description && (
                      <span className="text-xs text-gray-500 mt-0.5 block leading-snug">
                        {service.description}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          );
        })}
      </div>

      {/* SVG overlay for connections */}
      {serviceRects.size > 0 && connections.length > 0 && (
        <svg
          className="absolute inset-0 w-full h-full pointer-events-none"
          style={{ zIndex: 10 }}
        >
          <defs>
            <marker
              id="arch-arrowhead"
              markerWidth="8"
              markerHeight="6"
              refX="8"
              refY="3"
              orient="auto"
            >
              <polygon points="0 0, 8 3, 0 6" fill="#94a3b8" />
            </marker>
          </defs>

          {connections.map((conn, ci) => {
            const fromRect = serviceRects.get(conn.from);
            const toRect = serviceRects.get(conn.to);
            if (!fromRect || !toRect) return null;

            const start = getEdgePoint(fromRect, toRect);
            const end = getEdgePoint(toRect, fromRect);

            const midX = (start.x + end.x) / 2;
            const midY = (start.y + end.y) / 2;

            return (
              <g key={ci}>
                <line
                  x1={start.x}
                  y1={start.y}
                  x2={end.x}
                  y2={end.y}
                  stroke="#94a3b8"
                  strokeWidth="1.5"
                  markerEnd="url(#arch-arrowhead)"
                />
                {conn.label && (
                  <>
                    <rect
                      x={midX - conn.label.length * 3.2 - 4}
                      y={midY - 8}
                      width={conn.label.length * 6.4 + 8}
                      height={16}
                      rx={4}
                      fill="white"
                      stroke="#e2e8f0"
                      strokeWidth="1"
                    />
                    <text
                      x={midX}
                      y={midY + 4}
                      textAnchor="middle"
                      fill="#64748b"
                      fontSize="10"
                      fontFamily="system-ui, sans-serif"
                      fontWeight="500"
                    >
                      {conn.label}
                    </text>
                  </>
                )}
              </g>
            );
          })}
        </svg>
      )}
    </div>
  );
}
