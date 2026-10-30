package core

import (
	"errors"
	"time"
)

// ErrMachinePoolNotFound is a pool that does not exist, or is not the
// caller's.
var ErrMachinePoolNotFound = errors.New("machine pool not found")

// ErrMachinePoolInUse is a pool a trigger still runs on: it cannot be deleted.
var ErrMachinePoolInUse = errors.New("machine pool is used by a trigger")

// MachinePool is a named set of the owner's machines: those whose durable
// labels contain Selector (dev-docs/design/machine-pools.md).
type MachinePool struct {
	ID       string
	UserID   string
	Name     string
	Selector map[string]string
	// MinMembers is advisory; MaxMembers bounds a scale-up (0: never grows).
	MinMembers int
	MaxMembers int
	MemberSize string
	// MaxRunsPerMember bounds the live runs the pool places on one member
	// (0: no bound).
	MaxRunsPerMember int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// MachinePoolMemberRow is one of a pool's machines with what placement reads
// about it, for one project. Installed and the catalog are about that
// project; with no project they are false and nil.
type MachinePoolMemberRow struct {
	DaemonID string
	Name     string
	Size     string
	// Phase is the trigger machine_state vocabulary: connected, starting,
	// suspended, disconnected or failed.
	Phase           string
	Installed       bool
	WorkflowsJSON   *string
	PresetsJSON     *string
	CatalogPushedAt *time.Time
	// LiveRuns is how many of the pool's placed chats on this machine have a
	// live root run.
	LiveRuns int
}
