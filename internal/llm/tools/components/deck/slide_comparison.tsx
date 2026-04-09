import React from "react";

interface SlideComparisonProps {
  title: string;
  before: {
    label: string;
    items: string[];
  };
  after: {
    label: string;
    items: string[];
  };
}

export const SlideComparison: React.FC<SlideComparisonProps> = ({
  title,
  before,
  after,
}) => {
  return (
    <div
      className="slide relative flex flex-col overflow-hidden bg-gray-950"
      style={{ width: 1280, height: 720 }}
    >
      {/* Header */}
      <div className="flex-shrink-0 px-16 pt-12 pb-8">
        <h1 className="text-center text-4xl font-bold tracking-tight text-white">
          {title}
        </h1>
      </div>

      {/* Comparison panels */}
      <div className="flex flex-1 gap-6 px-16 pb-14">
        {/* Before panel */}
        <div className="flex flex-1 flex-col rounded-2xl border border-red-500/20 bg-red-950/10 p-8">
          <div className="mb-6 flex items-center gap-3">
            <div className="h-3 w-3 rounded-full bg-red-500" />
            <h2 className="text-2xl font-bold text-red-400">
              {before.label}
            </h2>
          </div>
          <ul className="flex flex-col gap-4">
            {before.items.map((item, i) => (
              <li key={i} className="flex items-start gap-3">
                <span className="mt-1.5 flex h-5 w-5 flex-shrink-0 items-center justify-center rounded-full bg-red-500/20 text-xs text-red-400">
                  ✕
                </span>
                <span className="text-base leading-relaxed text-gray-300">
                  {item}
                </span>
              </li>
            ))}
          </ul>
        </div>

        {/* Divider arrow */}
        <div className="flex flex-shrink-0 items-center justify-center px-2">
          <svg
            width="32"
            height="32"
            viewBox="0 0 24 24"
            fill="none"
            className="text-gray-600"
          >
            <path
              d="M5 12h14m0 0l-6-6m6 6l-6 6"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </div>

        {/* After panel */}
        <div className="flex flex-1 flex-col rounded-2xl border border-emerald-500/20 bg-emerald-950/10 p-8">
          <div className="mb-6 flex items-center gap-3">
            <div className="h-3 w-3 rounded-full bg-emerald-500" />
            <h2 className="text-2xl font-bold text-emerald-400">
              {after.label}
            </h2>
          </div>
          <ul className="flex flex-col gap-4">
            {after.items.map((item, i) => (
              <li key={i} className="flex items-start gap-3">
                <span className="mt-1.5 flex h-5 w-5 flex-shrink-0 items-center justify-center rounded-full bg-emerald-500/20 text-xs text-emerald-400">
                  ✓
                </span>
                <span className="text-base leading-relaxed text-gray-300">
                  {item}
                </span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </div>
  );
};

export default SlideComparison;
