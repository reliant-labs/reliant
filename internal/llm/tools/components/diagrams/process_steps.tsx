import React from 'react';

interface ProcessStepsProps {
  /** Steps */
  steps: Array<{
    title: string;
    description?: string;
    status?: 'completed' | 'active' | 'pending';
  }>;
  /** Horizontal or vertical layout */
  direction?: 'horizontal' | 'vertical';
  /** Accent color */
  accentColor?: string;
}

function CheckmarkIcon(): React.ReactElement {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <path
        d="M4 8.5l2.5 2.5 5.5-5.5"
        stroke="white"
        strokeWidth="2.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export default function ProcessSteps({
  steps,
  direction = 'horizontal',
  accentColor,
}: ProcessStepsProps): React.ReactElement {
  if (steps.length === 0) {
    return <div className="text-sm text-gray-400 italic">No steps provided</div>;
  }

  const isVertical = direction === 'vertical';

  const getCircleStyle = (
    status: 'completed' | 'active' | 'pending'
  ): { className: string; style?: React.CSSProperties } => {
    switch (status) {
      case 'completed':
        return {
          className: 'flex items-center justify-center w-9 h-9 rounded-full shrink-0',
          style: { backgroundColor: accentColor ?? '#10b981' },
        };
      case 'active':
        return {
          className:
            'flex items-center justify-center w-9 h-9 rounded-full shrink-0 ring-4 ring-offset-2 animate-pulse',
          style: {
            backgroundColor: accentColor ?? '#3b82f6',
            '--tw-ring-color': `${accentColor ?? '#3b82f6'}33`,
          } as React.CSSProperties,
        };
      case 'pending':
        return {
          className:
            'flex items-center justify-center w-9 h-9 rounded-full shrink-0 bg-gray-200',
        };
    }
  };

  const getLineColor = (status: 'completed' | 'active' | 'pending'): string => {
    if (status === 'completed') return accentColor ?? '#10b981';
    return '#d1d5db';
  };

  return (
    <div
      className={`flex ${isVertical ? 'flex-col' : 'flex-row items-start'} gap-0 w-full ${
        !isVertical ? 'overflow-x-auto' : ''
      }`}
    >
      {steps.map((step, i) => {
        const status = step.status ?? 'pending';
        const circle = getCircleStyle(status);
        const isLast = i === steps.length - 1;

        // For the connecting line, use the current step's status
        const lineColor = getLineColor(status);

        if (isVertical) {
          return (
            <div key={i} className="flex gap-4">
              {/* Circle + vertical line column */}
              <div className="flex flex-col items-center">
                <div className={circle.className} style={circle.style}>
                  {status === 'completed' ? (
                    <CheckmarkIcon />
                  ) : (
                    <span
                      className={`text-sm font-bold ${
                        status === 'active' ? 'text-white' : 'text-gray-400'
                      }`}
                    >
                      {i + 1}
                    </span>
                  )}
                </div>
                {!isLast && (
                  <div
                    className="w-0.5 grow min-h-[32px]"
                    style={{ backgroundColor: lineColor }}
                  />
                )}
              </div>

              {/* Text content */}
              <div className={`pb-6 ${isLast ? '' : ''}`}>
                <span
                  className={`text-sm font-bold leading-tight block ${
                    status === 'pending' ? 'text-gray-400' : 'text-gray-800'
                  }`}
                >
                  {step.title}
                </span>
                {step.description && (
                  <span
                    className={`text-xs leading-snug mt-0.5 block ${
                      status === 'pending' ? 'text-gray-300' : 'text-gray-500'
                    }`}
                  >
                    {step.description}
                  </span>
                )}
              </div>
            </div>
          );
        }

        // Horizontal layout
        return (
          <div key={i} className="flex items-start flex-1 min-w-[100px]">
            <div className="flex flex-col items-center">
              {/* Circle */}
              <div className={circle.className} style={circle.style}>
                {status === 'completed' ? (
                  <CheckmarkIcon />
                ) : (
                  <span
                    className={`text-sm font-bold ${
                      status === 'active' ? 'text-white' : 'text-gray-400'
                    }`}
                  >
                    {i + 1}
                  </span>
                )}
              </div>

              {/* Title + description below circle */}
              <span
                className={`text-xs font-bold text-center mt-2 leading-tight max-w-[100px] ${
                  status === 'pending' ? 'text-gray-400' : 'text-gray-800'
                }`}
              >
                {step.title}
              </span>
              {step.description && (
                <span
                  className={`text-[10px] text-center mt-0.5 leading-snug max-w-[100px] ${
                    status === 'pending' ? 'text-gray-300' : 'text-gray-500'
                  }`}
                >
                  {step.description}
                </span>
              )}
            </div>

            {/* Horizontal connector line */}
            {!isLast && (
              <div className="flex items-center h-9 flex-1 min-w-[24px] px-1">
                <div className="w-full h-0.5" style={{ backgroundColor: lineColor }} />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
