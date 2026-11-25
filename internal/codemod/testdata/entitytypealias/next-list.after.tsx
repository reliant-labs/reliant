"use client";

// yours: scaffolded once, never touched again — forge will not overwrite this file
// Disposable: forge scaffolds a CRUD route for EVERY entity; delete it (and its nav entry) if unused.
// Links list page (`forge scaffold page`): <Resource> owns loading/error/empty states,
// the filter box and cursor pagination, all SERVER-SIDE via the List RPC's filter fields — never
// filter one fetched page on the client. Depth: `forge skill load frontend/pages`.

import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";
import { keepPreviousData } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";

import Button from "@/components/ui/button";
import { useQueryResource } from "@/hooks/use-query-resource";
import { entityViewHref } from "@/lib/entity-routes";
import { formatValue } from "@/lib/format-utils";
import { useListLinks } from "@/hooks/thing-service-hooks_gen";

import type { Link as LinkEntity } from "@/gen/services/things/v1/things_pb";

// Rows per page requested from the server. Paginate the result with the
// <Resource> footer's Prev/Next (they walk the next_page_token cursor).
// Bump this to taste.
const PAGE_SIZE = 25;

// ---- Typed columns ----
// One entry per renderable proto field, type-checked against Link.
// The id and the created/updated timestamps are deliberately NOT here — the
// primary cell already links to the detail page, which is where they belong.
// Add one back if this list needs it; this file is yours.
const columns: ResourceColumn<LinkEntity>[] = [
  {
    header: "Title",
    cell: (item) => (
      <Link
        href={entityViewHref("links", item.id)}
        className="font-medium text-ink underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        {formatValue(item.title)}
      </Link>
    ),
  },
];

export default function LinksPage() {
  const router = useRouter();

  // Cursor stack: each entry is the page_token that opened that page (the
  // last entry is the current page; empty = first page). The cursor stays in
  // component state — it is an opaque server cursor, not a view worth
  // linking to — and any filter or sort change resets the stack to page one.
  const [cursor, setCursor] = useState<string[]>([]);

  // Server-side query: filters, sort and cursor go to the backend; nothing is
  // filtered, sorted or capped in the browser. keepPreviousData holds the
  // prior page while the next loads (no skeleton flash).
  const query = useListLinks({
    pageSize: PAGE_SIZE,
    pageToken: cursor.at(-1),
  }, { placeholderData: keepPreviousData });
  const resource = useQueryResource(query);

  const nextToken = resource.status === "success" ? resource.data.nextPageToken : "";

  return (
    <Resource<LinkEntity>
      title="Links"
      status={resource.status}
      data={resource.status === "success" ? resource.data.links : undefined}
      error={resource.status === "error" ? resource.error : undefined}
      onRetry={resource.status === "error" ? resource.refetch : undefined}
      isFetching={query.isFetching}
      columns={columns}
      rowKey={(item) => String(item.id)}
      onRowClick={(item) => router.push(entityViewHref("links", item.id))}
      rowsHaveLinks
      emptyTitle="No links found"
      emptyMessage="No records match the current view."
      onNextPage={() => setCursor((c) => [...c, nextToken])}
      onPrevPage={() => setCursor((c) => c.slice(0, -1))}
      hasNextPage={Boolean(nextToken)}
      hasPrevPage={cursor.length > 0}
      actions={
        <Button href="/links/new" size="lg" leftIcon={<Plus aria-hidden strokeWidth={2} />}>
          New Link
        </Button>
      }
    />
  );
}


