-- forge:backward-compatible — one added nullable column the previous release never reads or writes; its inserts leave it NULL, which is today's behaviour.

-- The display style a queued message is saved with when the drain delivers it
-- into history. NULL is an ordinary visible message. HIDDEN (4) is LLM-only:
-- the answer to an async ask_user question reaches the asking agent as a
-- message, but the transcript shows it on the question's card ("Answered: …"),
-- never as a user bubble. Values are reliant.v1.DisplayStyle.
ALTER TABLE agent_messages
    ADD COLUMN display_style integer;
