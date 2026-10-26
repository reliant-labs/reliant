/**
 * A machine name nobody on the account is using yet.
 *
 * The control plane keys a machine on (owner, name), so a create that reuses a
 * name does not make a second machine. Every create a person starts therefore
 * proposes a name that is free: `base` when nothing holds it, otherwise the
 * first free `base-2`, `base-3`, …
 *
 * `taken` is whatever list the caller has. It can be stale (a machine created
 * in another tab a second ago), so the server still refuses a duplicate, and a
 * caller that generated the name retries with the next one.
 */
export function uniqueMachineName(base: string, taken: Iterable<string | undefined>): string {
  const used = new Set<string>();
  for (const name of taken) {
    if (name) used.add(name.trim().toLowerCase());
  }
  const root = normalizeMachineName(base) || "machine";
  if (!used.has(root)) return root;
  for (let n = 2; ; n++) {
    const candidate = `${root}-${n}`;
    if (!used.has(candidate)) return candidate;
  }
}

/**
 * Coerce free text into the control plane's name rule: 1-63 lowercase
 * alphanumerics or hyphens, starting and ending alphanumeric. A suffix of up
 * to four characters ("-999") is left room for.
 */
export function normalizeMachineName(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 59)
    .replace(/-+$/g, "");
}
