// Copyright (c) 2025 Reliant Labs
package trigger

const (
	defaultReplayEventLimit = 20
	maxReplayEventLimit     = 50
)

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
