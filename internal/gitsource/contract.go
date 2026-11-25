package gitsource

import "context"

// The package's behavioural surface is two seams, one per side of the cache:
//
//   - [SourceResolver] — declared source → directory on disk (override, then
//     cache, then fetch). *Resolver is the implementation; callers that only
//     resolve (the release pin check) hold this instead of the concrete type.
//   - [Fetcher] — the cache-miss materialization *Resolver delegates to.
//     GitFetcher is the production implementation; tests inject a fake.
//
// Source, Resolution and Metadata are data carriers, not behaviour to mock.

// SourceResolver turns a declared source into the directory holding its code,
// and drops a cached copy so the next resolution re-fetches it.
type SourceResolver interface {
	// Resolve returns the directory holding the source's code, fetching it
	// into the cache if needed. A local override wins over the pin and is
	// reported in the Resolution, never silently.
	Resolve(ctx context.Context, src Source) (Resolution, error)

	// Refresh drops the cache entry for a source so the next Resolve
	// re-fetches it — the supported way to pick up a moved branch or tag.
	Refresh(src Source) error
}

// Fetcher materializes a source into a destination directory. It exists
// as an interface so the resolver can be tested without a network: the
// production implementation shells out to git, and a test injects a fake.
//
// A Fetcher is called only on a cache MISS, and must leave dst either
// fully populated or absent — the resolver writes the completion marker,
// so a Fetcher that fails partway is retried rather than trusted.
type Fetcher interface {
	// Fetch checks repo out at ref into dst (which does not yet exist).
	// It returns the resolved commit sha when it knows one; an empty
	// string is acceptable and only costs auditability.
	Fetch(ctx context.Context, src Source, dst string) (commit string, err error)
}

var (
	_ SourceResolver = (*Resolver)(nil)
	_ Fetcher        = GitFetcher{}
)
