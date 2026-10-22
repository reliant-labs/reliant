/**
 * URL detection utilities
 */

/**
 * True if the string is, in its entirety, an http(s) URL.
 *
 * Deliberately requires an explicit scheme. This runs against text the user
 * never marked up as a link (inline code spans), where scheme-less candidates
 * like `localhost:3000`, `example.com` or a bare package name are ambiguous —
 * and path-shaped strings are already claimed by isFilePath().
 */
export function isHttpUrl(str: string): boolean {
  if (!str || typeof str !== 'string') {
    return false;
  }

  const trimmed = str.trim();

  // A URL with interior whitespace is prose that happens to start with a scheme.
  if (!trimmed || /\s/.test(trimmed)) {
    return false;
  }

  if (!/^https?:\/\//i.test(trimmed)) {
    return false;
  }

  try {
    // Rejects `https://` and other scheme-only strings that pass the regex.
    return new URL(trimmed).hostname !== '';
  } catch {
    return false;
  }
}
