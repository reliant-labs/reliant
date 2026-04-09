import React from "react";

interface SlideCardGridProps {
  title: string;
  subtitle?: string;
  cards: Array<{
    title: string;
    description: string;
    /** Optional icon as React node */
    icon?: React.ReactNode;
    /** Highlight border color */
    highlight?: boolean;
    /** Small tag/badge text */
    badge?: string;
  }>;
}

export const SlideCardGrid: React.FC<SlideCardGridProps> = ({
  title,
  subtitle,
  cards,
}) => {
  const colCount = cards.length <= 2 ? cards.length : cards.length <= 4 ? 2 : 3;

  return (
    <div
      className="slide relative flex flex-col overflow-hidden bg-gray-950"
      style={{ width: 1280, height: 720 }}
    >
      {/* Header */}
      <div className="flex-shrink-0 px-16 pt-12 pb-6">
        <h1 className="text-4xl font-bold tracking-tight text-white">
          {title}
        </h1>
        {subtitle && (
          <p className="mt-2 text-lg text-gray-400">{subtitle}</p>
        )}
      </div>

      {/* Card grid */}
      <div className="flex flex-1 items-center justify-center px-16 pb-12">
        <div
          className="grid w-full gap-5"
          style={{
            gridTemplateColumns: `repeat(${colCount}, minmax(0, 1fr))`,
          }}
        >
          {cards.map((card, i) => (
            <div
              key={i}
              className={`relative flex flex-col gap-3 rounded-xl border p-6 ${
                card.highlight
                  ? "border-indigo-500/60 bg-indigo-950/20"
                  : "border-gray-800 bg-gray-900/50"
              }`}
            >
              {card.badge && (
                <span className="absolute right-4 top-4 rounded-full bg-indigo-500/20 px-2.5 py-0.5 text-xs font-medium text-indigo-300">
                  {card.badge}
                </span>
              )}

              {card.icon && (
                <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-gray-800 text-indigo-400">
                  {card.icon}
                </div>
              )}

              <h3 className="text-xl font-semibold text-white">
                {card.title}
              </h3>

              <p className="text-sm leading-relaxed text-gray-400">
                {card.description}
              </p>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};

export default SlideCardGrid;
