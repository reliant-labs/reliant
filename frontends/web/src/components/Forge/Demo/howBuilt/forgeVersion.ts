// Copyright (c) 2025 Reliant Labs

/**
 * A forge version for a reader: `v1.0.17` for the Go pseudo-version
 * `v1.0.17-0.20261105225243-7ad8943ce387` (the full string goes in a title).
 * A tagged release, or anything else, is returned as is.
 */
export function shortForgeVersion(version: string): string {
  return /^(v\d+\.\d+\.\d+)-(?:[0-9A-Za-z.]*\.)?0\.\d{14}-[0-9a-f]{12}$/.exec(version)?.[1] ?? version;
}
