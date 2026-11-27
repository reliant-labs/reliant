// Package gocacheprog is a GOCACHEPROG helper that lets every build on a
// machine read one shared Go build cache while writing only to its own.
//
// # The two tiers
//
// The machine keeps one TRUSTED cache (default
// /var/cache/workspace/go-build-trusted, or $FORGE_GO_TRUSTED_CACHE). Only
// trusted builds write it: `forge cache seed` on every merge to main, and the
// gate on main, both running plain `go` with GOCACHE pointing at it. Every
// other build on the machine — agents, worktrees, GitHub self-hosted runners,
// the merge-queue gate container — is untrusted. It runs with
// GOCACHEPROG="forge cache prog" and GOCACHE set to its own OVERLAY cache:
//
//   - get looks in the trusted tier first, then in the overlay;
//   - put writes only to the overlay. When the trusted tier already holds the
//     output bytes, only the overlay's index entry is written.
//
// Both tiers are plain cmd/go DiskCache directories (`<xx>/<hex>-a` index
// entries, `<xx>/<hex>-d` outputs), so a tier can be used by plain `go`
// directly and forge's storage trim (internal/storage) applies to each one
// unchanged. Nothing is shared between machines.
//
// # Why the split
//
// A cache entry is keyed by the hash of a build action's inputs, and cmd/go
// trusts whatever bytes sit under that key. Anyone who can write the shared
// directory can therefore plant an object under a key main's build will look
// up, and every later build on the machine links it. A pull request's code, or
// an agent's half-finished tree, must never be able to do that to the entries
// main's builds read. So untrusted contexts only read the trusted tier, and
// what they produce stays in a cache only they read.
//
// # Why trusted hits are returned in place
//
// A trusted hit hands cmd/go the trusted `-d` path itself rather than a copy
// in the overlay. Copying every hit would cost the very I/O the shared cache is
// there to save: there is no reflink on ext4, a hardlink would let the reader
// modify the trusted file through its own name, and fs.protected_hardlinks
// forbids linking a file another user owns in the first place.
//
// A hit restamps the served entry's mtimes (when an hour or more stale, like
// cmd/go's markUsed), since mtime-as-last-use is what the storage trim reads.
// Trusted hits are restamped when the reader may (same uid); the restamp
// changes no bytes, so trust is unaffected, and every error is ignored. For
// other readers (a runner's uid, a read-only bind mount in a gate container),
// main seeding and the trim's 2h floor plus live-build lease (the oldest
// running go process) protect the entries: seeding re-reads, and so restamps,
// every entry main's build set uses on every merge to main.
//
// # Failure modes
//
// A missing or unreadable trusted tier is not an error: the helper runs on the
// overlay alone, and the build proceeds at plain-GOCACHE speed. A damaged entry
// in either tier (a truncated index file, an output whose size disagrees with
// its index) is a miss, never a failure. Per-request errors go back to cmd/go
// in the response; the helper never writes to stderr in normal operation,
// because cmd/go connects it to the user's terminal.
package gocacheprog
