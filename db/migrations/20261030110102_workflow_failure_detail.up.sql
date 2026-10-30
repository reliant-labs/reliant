-- forge:backward-compatible — adds one nullable column to workflows; the previous release neither reads nor writes it.

-- Why a run failed, on the run itself. Written with the FAILED status by the
-- status activity, from the error the run ended with, and cleared when the
-- workflow row goes live again (a chat's root workflow id is reused by every
-- run of the chat). It is what lets a run that failed before any node ran
-- (input validation, a definition that would not load, a missing machine)
-- say why: such a run has no node attempt to carry the error.
ALTER TABLE workflows ADD COLUMN failure_detail text;
