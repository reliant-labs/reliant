-- forge:backward-compatible — additive nullable column; the previous release reads questions by named columns and never writes it.

-- An agent that asked an async question (ask_user async: true) and then
-- proceeded on a default can withdraw it once it is moot (withdraw_question),
-- so the user's inbox does not fill with stale questions. A withdrawn
-- question is resolved (status 2), with no answer, and says why here; the
-- timeline card shows "Withdrawn: <reason>". NULL for every question that was
-- answered or closed rather than withdrawn.
ALTER TABLE questions ADD COLUMN withdrawn_reason text;
