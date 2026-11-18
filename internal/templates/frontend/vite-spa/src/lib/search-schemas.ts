// src/lib/search-schemas.ts — codifies the "schema at the URL boundary" pattern.
//
// Tanstack-router model
// ---------------------
// Unlike Next.js, tanstack-router validates and types search params on the
// ROUTE itself via `validateSearch: schema.parse`. Read them in the
// component with `useSearch({ from: <routeId> })`. The schema lives next to
// the route definition; we still export `defineSearchSchema` here as a
// thin helper so callers have one canonical way to spell "Zod search
// schema for this route".
//
// Conventions:
//   - One schema per route. Co-locate the schema with the route definition.
//   - When you add a filter, extend the schema — don't reach for
//     `window.location.search` or `URLSearchParams`.
//   - For boolean-ish or numeric fields, use `z.coerce` so the schema
//     absorbs the URL's string-only nature.
//
// Example route:
//
//   const listSchema = z.object({
//     q: z.string().optional().default(""),
//     status: z.enum(["all", "active", "archived"]).optional().default("all"),
//     page: z.coerce.number().int().min(1).optional().default(1),
//   });
//
//   const listRoute = createRoute({
//     getParentRoute: () => rootRoute,
//     path: "/widgets",
//     validateSearch: listSchema.parse,
//     component: WidgetsList,
//   });
//
//   // In the component:
//   const { q, status, page } = useSearch({ from: listRoute.id });

import type { z } from "zod";

/**
 * defineSearchSchema is a no-op type helper that documents intent and
 * keeps Zod's inferred type in scope at the call site. Pass the result
 * to `createRoute({ validateSearch: schema.parse })`.
 */
export function defineSearchSchema<S extends z.ZodTypeAny>(schema: S): S {
  return schema;
}
