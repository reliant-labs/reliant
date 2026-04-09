import React from 'react';

interface FlowHorizontalProps {
  /** Steps in the flow */
  steps: Array<{
    title: string;
    description?: string;
    /** Optional: 'completed' | 'active' | 'pending' */
    status?: 'completed' | 'active' | 'pending';
  }>;
  /** Show a loop-back arrow from last to first (default false) */
  showLoop?: boolean;
  /** Loop label text */
  loopLabel?: string;
  /** Accent color */
  accentColor?: string;
  /** Secondary color for alternating steps */
  secondaryColor?: string;
}

const statusStyles = {
  completed: {
    border: 'border-emerald-400',
    bg: 'bg-emerald-50',
    dot: 'bg-emerald-500',
    text: 'text-emerald-700',
    line: 'stroke-emerald-400',
  },
  active: {
    border: 'border-blue-400',
    bg: 'bg-blue-50',
    dot: 'bg-blue-500',
    text: 'text-blue-700',
    line: 'stroke-blue-400',
  },
  pending: {
    border: 'border-gray-300',
    bg: 'bg-gray-50',
    dot: 'bg-gray-300',
    text: 'text-gray-500',
    line: 'stroke-gray-300',
  },
} as const;

export default function FlowHorizontal({
  steps,
  showLoop = false,
  loopLabel,
  accentColor,
  secondaryColor,
}: FlowHorizontalProps): React.ReactElement {
  if (steps.length === 0) {
    return <div className="text-sm text-gray-400 italic">No steps provided</div>;
  }

  const getStatus = (step: FlowHorizontalProps['steps'][number]) =>
    step.status ?? 'pending';

  return (
    <div className="relative w-full overflow-x-auto">
      {/* Main flow row */}
      <div className="flex items-stretch gap-0 min-w-max px-2 pt-2 pb-8">
        {steps.map((step, i) => {
          const status = getStatus(step);
          const s = statusStyles[status];
          const isEven = i % 2 === 0;

          const boxBg =
            accentColor && isEven
              ? undefined
              : secondaryColor && !isEven
                ? undefined
                : undefined;

          const inlineStyle: React.CSSProperties = {};
          if (accentColor && isEven) {
            inlineStyle.borderColor = accentColor;
            inlineStyle.backgroundColor = `${accentColor}10`;
          } else if (secondaryColor && !isEven) {
            inlineStyle.borderColor = secondaryColor;
            inlineStyle.backgroundColor = `${secondaryColor}10`;
          }

          return (
            <React.Fragment key={i}>
              {/* Step box */}
              <div
                className={`relative flex flex-col items-center justify-start rounded-lg border-2 px-5 py-4 min-w-[140px] max-w-[200px] ${
                  !inlineStyle.borderColor ? `${s.border} ${s.bg}` : ''
                }`}
                style={inlineStyle.borderColor ? inlineStyle : undefined}
              >
                {/* Status dot */}
                <div className="flex items-center gap-2 mb-1">
                  <span
                    className={`inline-block w-2.5 h-2.5 rounded-full ${s.dot} ${
                      status === 'active' ? 'animate-pulse' : ''
                    }`}
                  />
                  <span className={`text-xs font-semibold uppercase tracking-wide ${s.text}`}>
                    {status}
                  </span>
                </div>

                {/* Title */}
                <span className="text-sm font-bold text-gray-800 text-center leading-tight mt-1">
                  {step.title}
                </span>

                {/* Description */}
                {step.description && (
                  <span className="text-xs text-gray-500 text-center mt-1 leading-snug">
                    {step.description}
                  </span>
                )}
              </div>

              {/* Arrow connector between steps */}
              {i < steps.length - 1 && (
                <div className="flex items-center justify-center shrink-0" style={{ width: 48 }}>
                  <svg width="48" height="24" viewBox="0 0 48 24" fill="none">
                    <line
                      x1="0"
                      y1="12"
                      x2="36"
                      y2="12"
                      className={statusStyles[getStatus(steps[i + 1])].line}
                      strokeWidth="2"
                    />
                    <polygon
                      points="36,6 48,12 36,18"
                      className={statusStyles[getStatus(steps[i + 1])].line}
                      style={{ fill: 'currentColor' }}
                      fill="currentColor"
                    />
                  </svg>
                </div>
              )}
            </React.Fragment>
          );
        })}
      </div>

      {/* Loop-back arrow */}
      {showLoop && steps.length > 1 && (
        <svg
          className="absolute left-0 right-0 bottom-0 w-full pointer-events-none"
          height="40"
          preserveAspectRatio="none"
          viewBox="0 0 100 40"
          fill="none"
          style={{ overflow: 'visible' }}
        >
          <path
            d="M 90 0 L 90 28 Q 90 36 82 36 L 18 36 Q 10 36 10 28 L 10 0"
            stroke={accentColor ?? '#94a3b8'}
            strokeWidth="1.5"
            strokeDasharray="6 3"
            fill="none"
            vectorEffect="non-scaling-stroke"
          />
          {/* Arrowhead pointing up at the start */}
          <polygon
            points="6,6 10,0 14,6"
            fill={accentColor ?? '#94a3b8'}
            vectorEffect="non-scaling-stroke"
          />
          {loopLabel && (
            <text
              x="50"
              y="34"
              textAnchor="middle"
              fill={accentColor ?? '#64748b'}
              fontSize="9"
              fontFamily="system-ui, sans-serif"
            >
              {loopLabel}
            </text>
          )}
        </svg>
      )}
    </div>
  );
}
