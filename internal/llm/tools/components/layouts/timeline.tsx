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
        <div className="absolute left-4 top-0 h-full w-0.5 bg-gray-200" />

        <ul className="space-y-10">
          {items.map((item, i) => (
            <li key={i} className="relative pl-12">
              {/* Dot / icon */}
              <div className="absolute left-0 flex h-8 w-8 items-center justify-center rounded-full border-2 border-indigo-600 bg-white">
                {item.icon ?? (
                  <span className="h-2.5 w-2.5 rounded-full bg-indigo-600" />
                )}
              </div>

              <time className="text-xs font-medium text-gray-500">
                {item.date}
              </time>
              <h3 className="mt-1 text-base font-semibold text-gray-900">
                {item.title}
              </h3>
              <p className="mt-1 text-sm text-gray-500">{item.description}</p>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
