-- forge:backward-compatible — new table, the previous release never reads it.

-- Migration: run_machine_placements
-- Created: 2026-11-21T04:38:58Z
--
-- Where a workflow's `daemon: {pool: …}` or labels-only `daemon: {labels: …}`
-- (workflow or node level) placed one run, or one node of it
-- (internal/machinepool, PlaceRunOnMachinePool). A pool-bound trigger places
-- its chat (chats.machine_pool_id + active_daemon_id); a workflow places a
-- run inside a chat, so its placement is its own row.
--
--   workflow_id, scope  the placement's identity: the run's workflows row
--                       (not a foreign key: the row is the runtime's, and a
--                       placement whose row is gone simply stops counting),
--                       and '' for the workflow-level selector or the node's
--                       path (loop iteration excluded) for a node-level one.
--                       Asking again returns the recorded member.
--   user_id, chat_id    whose run it is; it goes with the chat
--   machine_pool_id     the pool it was placed on; NULL for a labels
--                       placement (an unnamed pool) or a pool since deleted
--   selector            the labels the member was chosen by
--   daemon_id           the member
--
-- A placement holds a slot on its member while its workflows row is live
-- (store.CountMachinePoolMemberLiveRuns), so the pool's max_runs_per_member
-- counts runs a workflow placed beside the chats a trigger placed.
--
-- Rollback: none needed. The previous release never names the table.
CREATE TABLE run_machine_placements (
    workflow_id     text NOT NULL,
    scope           text NOT NULL DEFAULT '',
    user_id         text NOT NULL,
    chat_id         text NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    machine_pool_id text REFERENCES machine_pools (id) ON DELETE SET NULL,
    selector        jsonb NOT NULL DEFAULT '{}'::jsonb,
    daemon_id       text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workflow_id, scope)
);

-- A member's live runs count its placements (pool, member) and, for a
-- labels placement, every placement on the member.
CREATE INDEX run_machine_placements_pool_member ON run_machine_placements (machine_pool_id, daemon_id);
CREATE INDEX run_machine_placements_member ON run_machine_placements (daemon_id);
-- The chat FK's cascade.
CREATE INDEX run_machine_placements_chat ON run_machine_placements (chat_id);
