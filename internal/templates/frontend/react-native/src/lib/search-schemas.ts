// src/lib/search-schemas.ts — codifies the "schema at the URL boundary" pattern.
//
// Expo Router serializes route params as strings. This helper parses them
// through a Zod schema so screens see typed values instead of reaching for
// raw `useLocalSearchParams()` and casting.
//
// Conventions (see the `frontend/state` skill):
//   - One schema per route. Co-locate the schema with the screen.
//   - When you add a filter, extend the schema — don't add raw lookups.
//   - For numeric/boolean fields, use `z.coerce` since query strings are
//     always strings on the wire.
//
// Example:
//
//   const filterSchema = z.object({
//     q: z.string().optional().default(""),
//     page: z.coerce.number().int().min(1).optional().default(1),
//   });
//   const { q, page } = useTypedSearchParams(filterSchema);

import { useLocalSearchParams } from "expo-router";
import { useMemo } from "react";
import type { z } from "zod";

export function useTypedSearchParams<S extends z.ZodTypeAny>(
  schema: S,
): z.infer<S> {
  const sp = useLocalSearchParams();
  return useMemo(() => schema.parse(sp), [sp, schema]);
}
