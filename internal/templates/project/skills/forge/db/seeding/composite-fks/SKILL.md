---
name: composite-fks
description: How `forge db seed` places a composite foreign key — every column from one parent row, two composites sharing a column, a composite that references its own table, and a composite UNIQUE over composite columns.
---

# Composite foreign keys

`db/seeding` covers the seeder as a whole. This is how it places a
multi-column `FOREIGN KEY`.

## One parent row, every column

The multi-tenant pattern is a database fact, so forge needs no declaration
for it:

```sql
ALTER TABLE strategies ADD CONSTRAINT strategies_tenant_id_id_key UNIQUE (tenant_id, id);
ALTER TABLE hypotheses ADD CONSTRAINT hypotheses_same_tenant_fkey
    FOREIGN KEY (tenant_id, strategy_id) REFERENCES strategies (tenant_id, id);
```

The seeder places a composite whole. It picks one parent row for it, and
takes every column from that row, so `tenant_id` is always the tenant of the
strategy that `strategy_id` names. The single-column references beside it
(`hypotheses_tenant_id_fkey`, `hypotheses_strategy_id_fkey`) still hold.
A `forge:ref` comment on a column that a composite places is not read,
because the composite already decides that column.

## Two composites sharing a column

Sometimes two composites share a column, as `(tenant_id, strategy_id)` and
`(tenant_id, channel_id)` do. The first one, in constraint-name order, places
the shared column. The second picks among the parent rows that agree with it,
here the channels of that tenant. When no parent row agrees, the second
composite keeps its own pick and postgres reports the violation by name:
forge does not invent a parent.

## A composite that references its own table

A tenant-scoped tree: `FOREIGN KEY (tenant_id, parent_id) REFERENCES nodes
(tenant_id, id)`. The seeder shapes the table into trees: about one node in
five is a root, which keeps its own `tenant_id` (a real tenant); every other
node's `parent_id` names an earlier node, and its `tenant_id` is its root's,
so a node and its parent always share a tenant. A root's `parent_id` is NULL
(or the node itself, when `parent_id` is NOT NULL). When something else also
decides `tenant_id` (another composite, a composite UNIQUE, a `forge:ref`
declaration), the seeder keeps that value and points `parent_id` at the latest
earlier node of the same tenant, or NULL when there is none.

## A composite UNIQUE over composite columns

A composite UNIQUE index deals distinct parent rows to a composite that no
earlier composite shares a column with, through the column that references
the parent's primary key: `UNIQUE (tenant_id, channel_id)` then never
repeats. No other column a composite places is dealt. If no other column of the index can
be dealt, the seed plan warns that it cannot place the index.

## Schemas

Only references to a table in the same schema are seeded as references. A
`REFERENCES auth.users` column is seeded as a plain value.
