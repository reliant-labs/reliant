# db analyze Phase 3 spike: can restored statistics make an empty table plan as large?

Run 2026-11 against **PostgreSQL 18.3** (forge `pgtest.NewURL()` embedded server; role `forge`, superuser).
Scripts: the two SQL files below, run with `psql -f`. Outputs are verbatim (blank lines trimmed).

## TL;DR

- **No.** Restoring stats on an empty table makes it plan _as smaller_, not larger. `estimate_rel_size`
  takes the tuple _density_ (reltuples/relpages) from the stats and multiplies it by the relation's
  **actual** block count. Empty file = 0 blocks = estimate clamps to rows=1, cost 0, and every
  predicate becomes a Seq Scan (even `id = $1` on the primary key). Without stats, PG's
  "never-vacuumed table = 10 pages" heuristic at least plans the index (section A).
- Same with a handful of real rows (1 block): 1 block × 100 tuples/page = 100 rows; still Seq Scan.
- Direct `UPDATE pg_class` behaves identically (same code path).
- Padding the heap with dead blocks _does_ make the restored density apply (section G), but the
  estimate is only `blocks × density`; reaching 10M rows needs ~100k real blocks (~800 MB) per table.
- `SET enable_seqscan = off` on an empty, stats-less table cleanly separates "an index serves this"
  from "nothing does", and PG18 marks the loser explicitly with **`Disabled: true`** in EXPLAIN.

## Script 1 (empty table, restore, few rows, rollback, pg_class)

```sql
\pset pager off
CREATE TABLE t (id bigint primary key, org_id bigint, status text, created_at timestamptz);
CREATE INDEX t_org ON t(org_id);
CREATE TABLE o (id bigint primary key, name text);
\echo '### A. empty, no stats'
SELECT relpages, reltuples FROM pg_class WHERE relname='t';
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE org_id = $1;
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE status = $1;
\echo '### B. restore relation stats 10M rows / 100k pages'
SELECT pg_restore_relation_stats('schemaname','public','relname','t','relpages',100000::integer,'reltuples',10000000::real,'relallvisible',0::integer);
SELECT pg_restore_relation_stats('schemaname','public','relname','t_org','relpages',30000::integer,'reltuples',10000000::real);
SELECT relpages, reltuples FROM pg_class WHERE relname IN ('t','t_org');
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE org_id = $1;
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE status = $1;
\echo '### C. plus attribute stats'
SELECT pg_restore_attribute_stats('schemaname','public','relname','t','attname','org_id','inherited',false,'null_frac',0::real,'avg_width',8,'n_distinct',5000::real);
SELECT pg_restore_attribute_stats('schemaname','public','relname','t','attname','status','inherited',false,'null_frac',0::real,'avg_width',8,'n_distinct',5::real);
SELECT pg_restore_attribute_stats('schemaname','public','relname','t','attname','id','inherited',false,'null_frac',0::real,'avg_width',8,'n_distinct',-1::real);
SELECT attname,null_frac,n_distinct FROM pg_stats WHERE tablename='t';
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE org_id = $1;
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE status = $1;
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE id = $1;
EXPLAIN SELECT * FROM t ORDER BY created_at DESC LIMIT 20;
\echo '### D. handful of real rows, then re-restore'
INSERT INTO t SELECT g, g%3, 'a', now() FROM generate_series(1,5) g;
SELECT relpages, reltuples, pg_relation_size('t')/8192 AS actual_blocks FROM pg_class WHERE relname='t';
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE org_id = $1;
EXPLAIN (GENERIC_PLAN) SELECT * FROM t WHERE status = $1;
\echo '### E. rollback test'
BEGIN;
SELECT pg_restore_relation_stats('schemaname','public','relname','o','relpages',50000::integer,'reltuples',5000000::real);
SELECT relpages,reltuples FROM pg_class WHERE relname='o';
ROLLBACK;
SELECT relpages,reltuples FROM pg_class WHERE relname='o';
\echo '### F. direct pg_class update'
UPDATE pg_class SET relpages=100000, reltuples=1e7 WHERE relname='o';
EXPLAIN SELECT * FROM o WHERE name='x';
```

### Output

```
Pager usage is off.
CREATE TABLE
CREATE INDEX
CREATE TABLE
### A. empty, no stats
 relpages | reltuples
----------+-----------
        0 |        -1
(1 row)
                             QUERY PLAN
--------------------------------------------------------------------
 Bitmap Heap Scan on t  (cost=4.19..12.66 rows=5 width=56)
   Recheck Cond: (org_id = $1)
   ->  Bitmap Index Scan on t_org  (cost=0.00..4.19 rows=5 width=0)
         Index Cond: (org_id = $1)
(4 rows)
                    QUERY PLAN
---------------------------------------------------
 Seq Scan on t  (cost=0.00..22.12 rows=5 width=56)
   Filter: (status = $1)
(2 rows)
### B. restore relation stats 10M rows / 100k pages
 pg_restore_relation_stats
---------------------------
 t
(1 row)
 pg_restore_relation_stats
---------------------------
 t
(1 row)
 relpages | reltuples
----------+-----------
   100000 |     1e+07
    30000 |     1e+07
(2 rows)
                    QUERY PLAN
--------------------------------------------------
 Seq Scan on t  (cost=0.00..0.00 rows=1 width=56)
   Filter: (org_id = $1)
(2 rows)
                    QUERY PLAN
--------------------------------------------------
 Seq Scan on t  (cost=0.00..0.00 rows=1 width=56)
   Filter: (status = $1)
(2 rows)
### C. plus attribute stats
 pg_restore_attribute_stats
----------------------------
 t
(1 row)
 pg_restore_attribute_stats
----------------------------
 t
(1 row)
 pg_restore_attribute_stats
----------------------------
 t
(1 row)
 attname | null_frac | n_distinct
---------+-----------+------------
 org_id  |         0 |       5000
 status  |         0 |          5
 id      |         0 |         -1
(3 rows)
                    QUERY PLAN
--------------------------------------------------
 Seq Scan on t  (cost=0.00..0.00 rows=1 width=32)
   Filter: (org_id = $1)
(2 rows)
                    QUERY PLAN
--------------------------------------------------
 Seq Scan on t  (cost=0.00..0.00 rows=1 width=32)
   Filter: (status = $1)
(2 rows)
                    QUERY PLAN
--------------------------------------------------
 Seq Scan on t  (cost=0.00..0.00 rows=1 width=32)
   Filter: (id = $1)
(2 rows)
                          QUERY PLAN
--------------------------------------------------------------
 Limit  (cost=0.01..0.02 rows=1 width=32)
   ->  Sort  (cost=0.01..0.02 rows=1 width=32)
         Sort Key: created_at DESC
         ->  Seq Scan on t  (cost=0.00..0.00 rows=1 width=32)
(4 rows)
### D. handful of real rows, then re-restore
INSERT 0 5
 relpages | reltuples | actual_blocks
----------+-----------+---------------
   100000 |     1e+07 |             1
(1 row)
                    QUERY PLAN
--------------------------------------------------
 Seq Scan on t  (cost=0.00..2.25 rows=1 width=32)
   Filter: (org_id = $1)
(2 rows)
                    QUERY PLAN
---------------------------------------------------
 Seq Scan on t  (cost=0.00..2.25 rows=20 width=32)
   Filter: (status = $1)
(2 rows)
### E. rollback test
BEGIN
 pg_restore_relation_stats
---------------------------
 t
(1 row)
 relpages | reltuples
----------+-----------
    50000 |     5e+06
(1 row)
ROLLBACK
 relpages | reltuples
----------+-----------
        0 |        -1
(1 row)
### F. direct pg_class update
UPDATE 1
                    QUERY PLAN
--------------------------------------------------
 Seq Scan on o  (cost=0.00..0.00 rows=1 width=40)
   Filter: (name = 'x'::text)
(2 rows)
```

## Script 2 (padded heap; enable_seqscan=off probes)

```sql
\pset pager off
\echo '### G. padded file: 1000 dead blocks + restored density'
CREATE TABLE p (id bigint primary key, org_id bigint, status text, created_at timestamptz) WITH (autovacuum_enabled=false);
CREATE INDEX p_org ON p(org_id);
INSERT INTO p SELECT g, g, 'x', now() FROM generate_series(1,100000) g;
DELETE FROM p;
SELECT pg_relation_size('p')/8192 AS blocks;
SELECT pg_restore_relation_stats('schemaname','public','relname','p','relpages',100000::integer,'reltuples',10000000::real);
SELECT pg_restore_attribute_stats('schemaname','public','relname','p','attname','org_id','inherited',false,'null_frac',0::real,'avg_width',8,'n_distinct',5000::real);
SELECT pg_restore_attribute_stats('schemaname','public','relname','p','attname','status','inherited',false,'null_frac',0::real,'avg_width',8,'n_distinct',5::real);
EXPLAIN (GENERIC_PLAN) SELECT * FROM p WHERE org_id = $1;
EXPLAIN (GENERIC_PLAN) SELECT * FROM p WHERE status = $1;
\echo '### H. enable_seqscan=off on fresh empty tables, no stats'
CREATE TABLE e (id bigint primary key, org_id bigint, status text, created_at timestamptz);
CREATE INDEX e_org ON e(org_id);
CREATE TABLE eo (id bigint primary key, name text);
SET enable_seqscan = off;
EXPLAIN (GENERIC_PLAN) SELECT * FROM e WHERE org_id = $1;
EXPLAIN (GENERIC_PLAN) SELECT * FROM e WHERE status = $1;
EXPLAIN SELECT * FROM e ORDER BY id DESC LIMIT 20;
EXPLAIN SELECT * FROM e ORDER BY created_at DESC LIMIT 20;
EXPLAIN SELECT * FROM e JOIN eo ON eo.id = e.org_id WHERE e.org_id = 1;
EXPLAIN SELECT * FROM eo JOIN e ON e.org_id = eo.id WHERE eo.name = 'x';
EXPLAIN SELECT * FROM e JOIN eo ON eo.name = e.status;
```

### Output

```
Pager usage is off.
### G. padded file: 1000 dead blocks + restored density
CREATE TABLE
CREATE INDEX
INSERT 0 100000
DELETE 100000
 blocks
--------
    736
(1 row)
 pg_restore_relation_stats
---------------------------
 t
(1 row)
 pg_restore_attribute_stats
----------------------------
 t
(1 row)
 pg_restore_attribute_stats
----------------------------
 t
(1 row)
                             QUERY PLAN
---------------------------------------------------------------------
 Bitmap Heap Scan on p  (cost=4.41..58.17 rows=15 width=32)
   Recheck Cond: (org_id = $1)
   ->  Bitmap Index Scan on p_org  (cost=0.00..4.41 rows=15 width=0)
         Index Cond: (org_id = $1)
(4 rows)
                       QUERY PLAN
---------------------------------------------------------
 Seq Scan on p  (cost=0.00..1656.00 rows=14720 width=32)
   Filter: (status = $1)
(2 rows)
### H. enable_seqscan=off on fresh empty tables, no stats
CREATE TABLE
CREATE INDEX
CREATE TABLE
SET
                             QUERY PLAN
--------------------------------------------------------------------
 Bitmap Heap Scan on e  (cost=4.19..12.66 rows=5 width=56)
   Recheck Cond: (org_id = $1)
   ->  Bitmap Index Scan on e_org  (cost=0.00..4.19 rows=5 width=0)
         Index Cond: (org_id = $1)
(4 rows)
                    QUERY PLAN
---------------------------------------------------
 Seq Scan on e  (cost=0.00..22.12 rows=5 width=56)
   Disabled: true
   Filter: (status = $1)
(3 rows)
                                    QUERY PLAN
-----------------------------------------------------------------------------------
 Limit  (cost=0.15..1.36 rows=20 width=56)
   ->  Index Scan Backward using e_pkey on e  (cost=0.15..58.70 rows=970 width=56)
(2 rows)
                           QUERY PLAN
-----------------------------------------------------------------
 Limit  (cost=45.51..45.56 rows=20 width=56)
   ->  Sort  (cost=45.51..47.94 rows=970 width=56)
         Sort Key: created_at DESC
         ->  Seq Scan on e  (cost=0.00..19.70 rows=970 width=56)
               Disabled: true
(5 rows)
                                QUERY PLAN
--------------------------------------------------------------------------
 Nested Loop  (cost=4.34..20.88 rows=5 width=96)
   ->  Index Scan using eo_pkey on eo  (cost=0.15..8.17 rows=1 width=40)
         Index Cond: (id = 1)
   ->  Bitmap Heap Scan on e  (cost=4.19..12.66 rows=5 width=56)
         Recheck Cond: (org_id = 1)
         ->  Bitmap Index Scan on e_org  (cost=0.00..4.19 rows=5 width=0)
               Index Cond: (org_id = 1)
(7 rows)
                                QUERY PLAN
--------------------------------------------------------------------------
 Nested Loop  (cost=1.01..102.86 rows=5 width=96)
   ->  Index Scan using eo_pkey on eo  (cost=0.15..65.15 rows=6 width=40)
         Filter: (name = 'x'::text)
   ->  Bitmap Heap Scan on e  (cost=0.86..6.23 rows=5 width=56)
         Recheck Cond: (eo.id = org_id)
         ->  Bitmap Index Scan on e_org  (cost=0.00..0.85 rows=5 width=0)
               Index Cond: (org_id = eo.id)
(7 rows)
                            QUERY PLAN
-------------------------------------------------------------------
 Merge Join  (cost=151.19..243.34 rows=5820 width=96)
   Merge Cond: (e.status = eo.name)
   ->  Sort  (cost=67.82..70.25 rows=970 width=56)
         Sort Key: e.status
         ->  Seq Scan on e  (cost=0.00..19.70 rows=970 width=56)
               Disabled: true
   ->  Sort  (cost=83.37..86.37 rows=1200 width=40)
         Sort Key: eo.name
         ->  Seq Scan on eo  (cost=0.00..22.00 rows=1200 width=40)
               Disabled: true
(10 rows)
```

## Findings

1. **Relation stats on an empty file are ignored-then-worse** (B, C, F). relpages=100000/reltuples=1e7
   is stored (pg_class shows it) but the plan is `rows=1 cost=0.00..0.00` Seq Scan for org_id, status,
   and even the PK. Attribute stats (n_distinct/null_frac) are stored (`pg_stats` shows them) and
   cannot help because the row count they divide is ~0.
2. **A few real rows** (D): 1 actual block × (1e7/1e5) = 100 rows estimate; status n_distinct=5 → rows=20;
   still Seq Scan. The scaling is exactly the feared one.
3. **Padding** (G): 736 dead blocks + restored density → `org_id` uses the index (rows=15) and status
   seq-scans (rows=14720). Plan _shape_ is right, but magnitudes are 100x off unless you materialize
   the real page count. Costs ~6 MB/table for a 1/100 scale model; impractical at full scale, and
   deletes leave bloat that autovacuum (disabled here) would truncate away.
4. **enable_seqscan=off, no stats** (H):
   - filter on indexed col → Bitmap Index Scan with `Index Cond`; unindexed col → `Seq Scan … Disabled: true`.
   - `ORDER BY id DESC LIMIT` → Index Scan Backward on pkey; `ORDER BY created_at` → Sort over disabled Seq Scan.
   - join on indexed FK → nested loop with `Index Cond` on both sides; join on unindexed cols → Merge Join
     over two `Disabled: true` seq scans.
   - **Trap:** `eo.name = 'x'` (no index on name) was answered by a _full Index Scan on eo_pkey with a
     `Filter:`_: the planner uses any index as a seqscan substitute. A classifier must require an
     `Index Cond` (or index-order satisfying ORDER BY) for the predicate, not merely an Index node.
     `Disabled: true` (new in PG18; the old disable_cost=1e10 hack is gone) is the reliable negative signal.
5. **Restore-function requirements** (PG18 floor; the functions are new in 18): caller must own the
   relation or have MAINTAIN on it (superuser here). They are ordinary transactional catalog writes:
   inside `BEGIN … ROLLBACK` the stats show then vanish (E). Named-arg variadic form needs exact types
   (`::integer` relpages, `::real` reltuples/null_frac/n_distinct, `inherited` required for attribute stats).
   They do not touch the file, so they cannot change `RelationGetNumberOfBlocks`.

## Recommendation for the scratch target

**Do not use stats restore.** On an empty scratch DB it is actively harmful (forces Seq Scan everywhere,
hides real index use), and the only way to make it work (physically padding each heap to the claimed page
count) does not scale. Instead: plan every query on the empty, never-analyzed schema with
`SET enable_seqscan = off` (plus `enable_sort = off` for ORDER BY … LIMIT probes if desired), use
`EXPLAIN (GENERIC_PLAN, FORMAT JSON)`, and classify per relation: **served** iff its scan node has an
`Index Cond` (or an index ordering that satisfies the ORDER BY); **unserved** iff it carries
`Disabled: true` _or_ is an index scan with only a `Filter`. Combine that boolean with declared size hints
(e.g. "table expected large") for severity, rather than trying to get the planner to believe the size.
