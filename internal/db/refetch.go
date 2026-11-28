package db

import (
	"github.com/reliant-labs/reliant/internal/db/core"
)

// RefetchType identifies what data the frontend should re-fetch; the type and
// its values are core's.
type RefetchType = core.RefetchType

const (
	RefetchWorktreeChanges    = core.RefetchWorktreeChanges
	RefetchWorkflowExecutions = core.RefetchWorkflowExecutions
	RefetchConfigHealth       = core.RefetchConfigHealth
	RefetchPlanTasks          = core.RefetchPlanTasks
	RefetchFileTree           = core.RefetchFileTree
	RefetchAgentMailbox       = core.RefetchAgentMailbox
	RefetchInbox              = core.RefetchInbox
	RefetchProjectOrgMoved    = core.RefetchProjectOrgMoved
)

// RefetchData is the JSON payload for refetch events; core's.
type RefetchData = core.RefetchData

// RefetchOpts provides optional scoping for user-level refetch events; core's.
type RefetchOpts = core.RefetchOpts
