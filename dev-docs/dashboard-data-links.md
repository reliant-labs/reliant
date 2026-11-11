# Dashboard Data links and saved views

Stacked UI follow-up to the Data browser (#2339). The server must pin forge
with dashspec `Panel.Link` and `KindBrowse` (forge #1900) before authors save
these descriptors. No generated client changes: DashboardService carries the
canonical spec as JSON, and preserves these additive fields through the existing
save, version, restore and promote flow.

## Reading

`DashboardView` supplies `DataPanelBody` with the **selected project environment**
and the console's typed Data route builder. No environment is read from a panel.
All-environment, org and platform views cannot read a saved browse panel or
activate Data links. Choose one project environment first.

Data admission uses the same `DataBrowserApiContext` list/describe/browse API and
React Query hooks as Data. Selection is exact: a missing database or table never
falls back to another database. Audited reads require that environment's tab
confirmation and reason; denied/unknown policies never read rows. The server's
`db:query` admission remains authoritative. A permission refusal is displayed,
not retried as a different request. Reads do not poll with telemetry panels.

`Panel.link` can filter a target column from a result field or open a row from
`fromFields` in described primary-key order. The entire composite key must be
present, scalar and non-null. Description/result fields that are absent disable
the link. Table rows have Data actions and linked group labels; bars themselves
link, with a text link list for accessibility (including pie/donut renderings).
All dynamic values use the existing Data URL encoding, never an arbitrary href.

`Panel.browse` is a structured descriptor: columns, ANDed filters, sort and page
size. References are whole values `{from}`, `{to}` (RFC3339), or a declared
filter id. Unset declared filters are omitted, as dashspec specifies; unknown
references and malformed descriptors stop before BrowseTable. Partial brace
strings stay literal. There is no evaluation or SQL construction. The existing
read-only DataGrid provides cells, paging, columns and filters; adjustments are
transient, and Open in Data carries the current view. Time bounds remain stable
through telemetry re-renders until the dashboard range changes.

## Saving

Data's **Save as view** chooses an existing writable workspace dashboard and
appends a browse panel using `SaveDashboard` at the version just read. Existing
panels and environment scope stay unchanged. The saved snapshot has filters,
columns, sort and size, not row selection or page offset. A literal value shaped
exactly like a parameter reference is refused rather than silently acquiring
parameter semantics. Conflicts are surfaced, not silently overwritten. Create a
workspace dashboard or use the existing Save as copy flow first if none exists.

## Deliberate limits

Identity links are a separate integration: they require an explicit identity
subject schema marker plus the additive metadata/GetUser contract. No guesses
from email/external_id and no client-side two-hop ownership scans are made here.
Unknown future panel kinds continue to render an explicit unavailable note.
New saved-view creation targets existing workspace dashboards only; versioning
and promotion are the existing dashboard controls, unchanged.
