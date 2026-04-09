import React from "react";

interface SplitViewProps {
  left: React.ReactNode;
  right: React.ReactNode;
  /** Initial split ratio 0-1 (default 0.5) */
  ratio?: number;
}

export default function SplitView({
  left,
  right,
  ratio = 0.5,
}: SplitViewProps) {
  const leftPercent = Math.round(ratio * 100);
  const rightPercent = 100 - leftPercent;

  return (
    <div className="flex h-screen overflow-hidden">
      <div
        className="overflow-y-auto border-r border-gray-200"
        style={{ flexBasis: `${leftPercent}%` }}
      >
        {left}
      </div>
      <div
        className="overflow-y-auto"
        style={{ flexBasis: `${rightPercent}%` }}
      >
        {right}
      </div>
    </div>
  );
}
