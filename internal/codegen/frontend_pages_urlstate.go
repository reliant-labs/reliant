package codegen

// Generated list pages hold their filters in the URL.
//
// forge ships `useTypedSearchParams` in src/lib/search-schemas.ts and a
// documented "schema at the URL boundary" convention, and then the ONLY page
// that ever used it was the scaffolded auth/callback route. Every generated
// list page held its filters in useState, so in the dogfood app the
// dashboard's four `?status=…` tiles were DEAD LINKS: the tile said "5
// pending", the click landed on all 20, unfiltered, with no error anywhere.
//
// The URL is therefore the source of truth for every filter the list page
// renders. That is what makes a deep link work, what makes the Back button
// undo a filter change, and what makes a filtered view something you can
// paste into a ticket. The page-token cursor stays in component state: it is
// an opaque server cursor, not a view anyone should be linking to, and a
// filter change resets it to page one.

// HasURLFilters reports whether the list page has any filter control at all,
// and therefore whether it reads and writes the query string. False (an
// unfiltered list) leaves the page exactly as it was: no schema, no
// useSearchParams, and so no Suspense boundary either.
func (p PageTemplateData) HasURLFilters() bool {
	return p.SearchFilterField != "" || len(p.ExactFilterFields) > 0
}

// ListReactImports is the `react` import list the list page needs, in
// eslint's canonical (alphabetical) order. Computed rather than spelled out
// in the template because the conditions overlap — a page with both a cursor
// and a scalar filter needs useState once, not twice, and a duplicate
// specifier is a TS error on a file forge just wrote.
func (p PageTemplateData) ListReactImports() []string {
	if !p.HasURLFilters() {
		if p.HasCursorPagination {
			return []string{"useState"}
		}
		return nil
	}
	imports := []string{"Suspense", "useCallback"}
	if p.HasScalarExactFilter {
		imports = append(imports, "useEffect")
	}
	if p.HasCursorPagination || p.HasScalarExactFilter {
		imports = append(imports, "useState")
	}
	return imports
}
