---
name: proto-list-filters
description: Declare range and prefix filters on a List request with (forge.v1.filter) so the generated List op applies them; no hand-written List handler for a time window or a key prefix.
---

# Range and prefix filters on a List request

A List request field is an exact match on the column of the same name
(`proto` has the conventions). For anything else — a time window, a "from"
bound, a key prefix — declare the comparison on the field with
`(forge.v1.filter)`, and the generated List op applies it:

```proto
import "google/protobuf/timestamp.proto";

message ListMetricSnapshotsRequest {
  int32 page_size = 1;
  string page_token = 2;
  optional string key_prefix = 3 [(forge.v1.filter) = {column: "key", op: LIST_FILTER_OP_PREFIX}];
  optional string week_start_from = 4 [(forge.v1.filter) = {column: "week_start", op: LIST_FILTER_OP_GTE}];
  optional string week_start_before = 5 [(forge.v1.filter) = {column: "week_start", op: LIST_FILTER_OP_LT}];
  google.protobuf.Timestamp published_after = 6 [(forge.v1.filter) = {column: "published_at", op: LIST_FILTER_OP_GTE}];
}
```

`column` names the column compared, which may differ from the field name.
Empty means the field's own name. `op` is the comparison:

| `op` | SQL | Field types |
|---|---|---|
| unset | `col = v` | string, number, bool, enum, `google.protobuf.Timestamp` |
| `LIST_FILTER_OP_GT` / `GTE` | `col > v` / `col >= v` | string, number, `google.protobuf.Timestamp` |
| `LIST_FILTER_OP_LT` / `LTE` | `col < v` / `col <= v` | string, number, `google.protobuf.Timestamp` |
| `LIST_FILTER_OP_PREFIX` | `col LIKE 'v%'` | string, against a text column |

- **Unset applies no filter**, as for an exact field: a nil `optional` or
  `Timestamp`, an empty string, a zero number. Declare range fields
  `optional` (`forgeconv-list-filter-optional`), so that 0 and "" can be
  real bounds.
- **A prefix is literal.** `orm.WherePrefix` escapes `%`, `_` and `\`, so
  `weekly_` does not match `weeklyX`.
- **A Timestamp compares against a time column**, bound as `AsTime()`, by
  range or by equality. Equality needs no facet when the field is named for
  its column: `google.protobuf.Timestamp week_start = 9;` takes one week
  exactly, beside a `week_start_from` / `week_start_before` window.
- **`forge generate` refuses** a column the applied schema does not declare,
  a prefix on a non-string field or non-text column, a range on a bool, enum
  or message other than Timestamp, equality on a repeated field, bytes, a
  map or a message other than Timestamp (facet or not), a Timestamp against
  a non-time column, and a facet on `search`, `query` or `q` (those names
  are the free-text search box). Each refusal names the field.

Two fields can bound one column: `_from` (`GTE`) with `_before` (`LT`)
makes a half-open window. The custom-read-shape skeleton (`proto` has it)
does not seed facet fields; wire those yourself with `orm.WhereGte` /
`orm.WherePrefix` and the rest.
