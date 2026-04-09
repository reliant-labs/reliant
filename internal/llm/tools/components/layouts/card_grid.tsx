import React from "react";

interface CardGridProps {
  cards: Array<{
    title: string;
    description: string;
    image?: string;
    tags?: string[];
  }>;
  columns?: 2 | 3 | 4;
}

const columnClasses: Record<number, string> = {
  2: "grid-cols-1 sm:grid-cols-2",
  3: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3",
  4: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-4",
};

export default function CardGrid({ cards, columns = 3 }: CardGridProps) {
  return (
    <div className={`grid gap-6 ${columnClasses[columns]}`}>
      {cards.map((card, i) => (
        <div
          key={i}
          className="overflow-hidden rounded-xl border border-gray-200 bg-white"
        >
          {card.image && (
            <img
              src={card.image}
              alt={card.title}
              className="h-48 w-full object-cover"
            />
          )}

          <div className="p-5">
            <h3 className="text-lg font-semibold text-gray-900">
              {card.title}
            </h3>
            <p className="mt-1 text-sm text-gray-500">{card.description}</p>

            {card.tags && card.tags.length > 0 && (
              <div className="mt-3 flex flex-wrap gap-2">
                {card.tags.map((tag, j) => (
                  <span
                    key={j}
                    className="inline-flex rounded-full bg-indigo-50 px-2.5 py-0.5 text-xs font-medium text-indigo-700"
                  >
                    {tag}
                  </span>
                ))}
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
