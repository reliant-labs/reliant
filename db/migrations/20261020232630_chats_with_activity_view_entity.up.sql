-- forge:backward-compatible — catalog comments only; no table, column or view definition changes, and the previous release never reads comments.

-- Opts the view into forge's generated data layer as a read-only entity
-- (internal/store: ChatsWithActivity, keyed by id, with Get/List/Count and no
-- writes).
COMMENT ON VIEW chats_with_activity IS 'forge:entity';

-- Integer columns that hold a reliantv1 proto enum project to that enum on the
-- generated entity, the type internal/db/core already uses for them, so a
-- ported query maps the field without a cast.
COMMENT ON COLUMN chats.state IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatState';
COMMENT ON COLUMN workflows.state IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.WorkflowState';
COMMENT ON COLUMN workflows.stop_reason IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.WorkflowStopReason';
COMMENT ON COLUMN messages.role IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.MessageRole';
COMMENT ON COLUMN messages.display_style IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.DisplayStyle';
COMMENT ON COLUMN message_content_blocks.block_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ContentBlockType';
COMMENT ON COLUMN chat_updates.update_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatUpdateType';

-- A view's comments (its own and its columns') are dropped with the view, so a
-- migration that DROPs and re-CREATEs chats_with_activity must repeat this
-- block.
COMMENT ON COLUMN chats_with_activity.state IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatState';
COMMENT ON COLUMN chats_with_activity.root_workflow_state IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.WorkflowState';
COMMENT ON COLUMN chats_with_activity.root_workflow_stop_reason IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.WorkflowStopReason';
