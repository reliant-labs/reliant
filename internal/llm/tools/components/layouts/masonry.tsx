import React from "react";

interface MasonryProps {
  items: Array<{ content: React.ReactNode; span?: 1 | 2 }>;
  columns?: 2 | 3 | 4;
}

const columnClasses: Record<number, string> = {
  2: "columns-1 sm:columns-2",
  3: "columns-1 sm:columns-2 lg:columns-3",
  4: "columns-1 sm:columns-2 lg:columns-4",
};

export default function Masonry({ items, columns = 3 }: MasonryProps) {
  return (
    <div className={`gap-4 ${columnClasses[columns]}`}>
      {items.map((item, i) => (
        <div
          key={i}
          className={`mb-4 break-inside-avoid rounded-xl border border-gray-200 bg-white p-5 ${
            item.span === 2 ? "sm:col-span-2" : ""
          }`}
        >
          {item.content}
        </div>
      ))}
    </div>
  );
}
