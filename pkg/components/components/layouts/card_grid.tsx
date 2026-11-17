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
        <div key={i} className="overflow-hidden rounded-xl border border-border bg-surface">
          {card.image && (
            <img src={card.image} alt={card.title} className="h-48 w-full object-cover" />
          )}

          <div className="p-5">
            <h3 className="text-lg font-semibold text-ink">{card.title}</h3>
            <p className="mt-1 text-sm text-ink-muted">{card.description}</p>

            {card.tags && card.tags.length > 0 && (
              <div className="mt-3 flex flex-wrap gap-2">
                {card.tags.map((tag, j) => (
                  <span
                    key={j}
                    className="inline-flex rounded-full bg-accent-surface px-2.5 py-0.5 text-xs font-medium text-accent-ink"
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
