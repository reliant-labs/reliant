-- forge:backward-compatible — catalog comments only; no table or column definition changes, and the previous release never reads comments.

-- plans.status, plans.complexity and tasks.status hold a proto enum's wire
-- number. With a forge:go-type comment forge projects them to the Go enum
-- type but cannot convert that type to or from the wire, so the generated CRUD
-- ops for PlanService and TaskService fail to generate. Without it they are
-- plain INT4 columns, which forge maps natively to the proto enum by number.
-- The internal/store mappers convert to core's int32 at the boundary as before.
COMMENT ON COLUMN plans.status IS NULL;
COMMENT ON COLUMN plans.complexity IS NULL;
COMMENT ON COLUMN tasks.status IS NULL;
