import React from "react";

interface TimelineProps {
  items: Array<{
    date: string;
    title: string;
    description: string;
    icon?: React.ReactNode;
  }>;
}

export default function Timeline({ items }: TimelineProps) {
  return (
    <div className="mx-auto max-w-3xl px-6 py-10">
      <div className="relative">
        {/* Vertical line */}
        <div className="absolute left-4 top-0 h-full w-0.5 bg-surface-muted" />

        <ul className="space-y-10">
          {items.map((item, i) => (
            <li key={i} className="relative pl-12">
              {/* Dot / icon */}
              <div className="absolute left-0 flex h-8 w-8 items-center justify-center rounded-full border-2 border-accent-border bg-surface">
                {item.icon ?? <span className="h-2.5 w-2.5 rounded-full bg-accent" />}
              </div>

              <time className="text-xs font-medium text-ink-muted">{item.date}</time>
              <h3 className="mt-1 text-base font-semibold text-ink">{item.title}</h3>
              <p className="mt-1 text-sm text-ink-muted">{item.description}</p>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
