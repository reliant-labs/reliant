-- forge:backward-compatible — adds one nullable column with no default; the previous release neither reads nor writes it, and its inserts leave it NULL, which reads as "no failed start".
--
-- A root workflow is PENDING from the launch commit until its Temporal start
-- marks it ACTIVE. That one state used to cover two different things: a start
-- in flight (a fire that has just committed) and a start that failed (a
-- half-launched fire, stranded). A trigger's overlap=skip guard could not tell
-- them apart, so it let a fire through behind a predecessor that was about to
-- run (TestConcurrentFiresRespectOverlapSkip).
--
-- start_failed_at names the second case: set when a start attempt failed with
-- the row still PENDING (by the launcher's error branch, or by the reconciler
-- when a PENDING root's Temporal run never started within its bound), and
-- cleared when a retry of that start begins. PENDING with it NULL is a start in
-- flight; PENDING with it set is stranded.

ALTER TABLE workflows ADD COLUMN start_failed_at timestamp with time zone;
