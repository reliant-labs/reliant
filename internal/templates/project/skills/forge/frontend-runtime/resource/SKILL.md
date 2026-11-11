---
name: frontend-runtime/resource
description: The forge-web-runtime <Resource> data-table container — the loading/error/empty/data ladder, debounced server-side filter and cursor pagination with useQueryResource and a generated list hook, its layout, sorting (sortKey / order_by) and row clicks.
---

# `<Resource>` — the data-table container

`<Resource>` encapsulates the loading / error / empty / data tristate ladder,
a debounced server-side filter, and cursor pagination in one owned-once
component. Pair it with `useQueryResource` and a generated list hook. Do not
hand-roll the tristate ladder, and do not client-side-filter a single page cap.
Pass the query's error straight through: the error rung renders
`userMessage(error)`, so there is nothing to pre-format.

```tsx
import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";
import { useQueryResource } from "@/hooks/use-query-resource";

const columns: ResourceColumn<Item>[] = [
  { header: "Name", cell: (i) => i.name },
  { header: "Created", cell: (i) => fmtDate(i.createdAt) },
];

export function ItemsPage() {
  const [cursor, setCursor] = useState<string[]>([]);
  const [filter, setFilter] = useState("");
  const q = useQueryResource(useListItems({ pageToken: cursor.at(-1), filter }));
  return (
    <Resource
      title="Items"
      status={q.status}
      data={q.status === "success" ? q.data.items : undefined}
      error={q.status === "error" ? q.error : undefined}
      columns={columns}
      rowKey={(i) => i.id}
      filter={filter}
      onFilterChange={setFilter}
      onNextPage={() => setCursor((c) => [...c, nextToken])}
      onPrevPage={() => setCursor((c) => c.slice(0, -1))}
      hasNextPage={Boolean(nextToken)}
      hasPrevPage={cursor.length > 0}
    />
  );
}
```

Layout: header (`title` + `actions`, the primary action only), a wrapping
toolbar (search + `toolbar`, where filters go), the table, and a footer with
the count (`totalCount`; `pageStart` for "1–25 of 112") and the pager.

- **Sorting**: a column's `sortKey` (the List `order_by`) plus `sort` /
  `onSortChange` make its header an `aria-sort` button (asc → desc → off).
  pkg/crud pages only in key order: a sorted List is one page.
- **Row clicks**: link the primary cell, pass `rowsHaveLinks` (rows stay
  table rows); without it an `onRowClick` row is a button.
