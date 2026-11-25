---
name: observability/query/incidents
description: Why one outage does not bury the issue inbox — the sfp2 fingerprint folds a normalized error class (SQLSTATE, Go error type, masked message) into records with no exception.type, and `forge obs issues` collapses issues that share a cause and first fired within `--incident-window` into one incident (`incident_key`, `related_count`, `--expand-incidents`) without merging them.
---

# One cause, many issues

Two things keep an outage from burying the inbox.

**The cause is part of the fingerprint.** A log line that puts its failure in the `error` attribute with no `exception.type` (a logger that formats the error to a string, or a library outside `forge/pkg/observe`) used to group by its message template alone, so one `Transaction failed` was UTF-8 corruption, serialization failures, prepared-statement clashes and timeouts at once. The fingerprint (sfp2) folds a normalized error class into exactly those records: the `(SQLSTATE nnnnn)` the text carries, else a leading Go type name (`*pgconn.PgError`), else the message with numbers, ids and quoted values masked. The class refines the call site, never replaces it: an error that carries a stack still groups by the frame it began at. A record with an `exception.type` or an `error.group` keeps the fingerprint it always had, so nothing already filed is re-keyed.

**An incident is not a merge.** A dead database fails every caller, and each caller is its own call site and so its own issue. `forge obs issues` marks issues that share a *cause* (the SQLSTATE, the refused dial target, or the error class) and first fired within `--incident-window` of each other, in any service, with an `incident_key` and a `related_count`, and collapses each incident to its first issue. The hidden issues still exist with their own counts and state; `--expand-incidents` lists them, and `forge obs issue <id>` lists the rest of its incident. The window is anchored on the incident's first issue, so a slow drip of one cause is several incidents, not one that never ends.
