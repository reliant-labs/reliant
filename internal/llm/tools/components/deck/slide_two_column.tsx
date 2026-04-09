import React from "react";

interface SlideTwoColumnProps {
  title: string;
  left: React.ReactNode;
  right: React.ReactNode;
  /** Subtitle below title */
  subtitle?: string;
}

export const SlideTwoColumn: React.FC<SlideTwoColumnProps> = ({
  title,
  left,
  right,
  subtitle,
}) => {
  return (
    <div
      className="slide relative flex flex-col overflow-hidden bg-gray-950"
      style={{ width: 1280, height: 720 }}
    >
      {/* Title bar */}
      <div className="flex-shrink-0 border-b border-gray-800 px-16 pb-6 pt-12">
        <h1 className="text-4xl font-bold tracking-tight text-white">
          {title}
        </h1>
        {subtitle && (
          <p className="mt-2 text-lg text-gray-400">{subtitle}</p>
        )}
      </div>

      {/* Two columns */}
      <div className="flex flex-1 min-h-0">
        <div className="flex w-1/2 flex-col justify-center border-r border-gray-800/50 px-16 py-10">
          {left}
        </div>
        <div className="flex w-1/2 flex-col justify-center px-16 py-10">
          {right}
        </div>
      </div>
    </div>
  );
};

export default SlideTwoColumn;
