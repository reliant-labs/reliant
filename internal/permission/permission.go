// Copyright (c) 2025 Reliant Labs
package permission

// Action represents a permission action
type Action string

const (
	ActionView   Action = "view"
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)
