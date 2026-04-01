// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// RecoveryService handles process state recovery on startup
type RecoveryService struct {
	repo db.Repository
}

// NewRecoveryService creates a new recovery service
func NewRecoveryService(repo db.Repository) *RecoveryService {
	return &RecoveryService{repo: repo}
}

// RecoverProcesses validates running processes from the database and marks stale ones
// This should be called during server startup
func (s *RecoveryService) RecoverProcesses(ctx context.Context) (*RecoveryResult, error) {
	// Get all processes marked as running in the database
	processes, err := s.repo.GetRunningBackgroundProcesses(ctx)
	if err != nil {
		return nil, err
	}

	result := &RecoveryResult{
		TotalFound:     len(processes),
		StillRunning:   0,
		MarkedStale:    0,
		StaleProcesses: []string{},
	}

	if len(processes) == 0 {
		return result, nil
	}

	var staleIDs []string

	for _, process := range processes {
		// Check if we have a PID to validate
		if process.PID == nil || *process.PID <= 0 {
			// No PID stored, mark as stale
			staleIDs = append(staleIDs, process.ID)
			result.StaleProcesses = append(result.StaleProcesses, process.ID)
			continue
		}

		pid := *process.PID

		// Validate the process is still running
		if !IsProcessRunning(pid) {
			staleIDs = append(staleIDs, process.ID)
			result.StaleProcesses = append(result.StaleProcesses, process.ID)
			continue
		}

		// If we have a signature, validate it
		if process.Signature != nil && *process.Signature != "" {
			expectedSignature := GenerateProcessSignature(process.Command, process.StartedAt)
			if *process.Signature != expectedSignature {
				// PID is reused by a different process
				staleIDs = append(staleIDs, process.ID)
				result.StaleProcesses = append(result.StaleProcesses, process.ID)
				continue
			}
		}

		// Process is still running
		result.StillRunning++
	}

	// Mark stale processes in the database
	if len(staleIDs) > 0 {
		if err := s.repo.MarkStaleProcesses(ctx, staleIDs); err != nil {
			logging.Error("Failed to mark stale processes", "error", err)
			return result, err
		}
		result.MarkedStale = len(staleIDs)
	}

	return result, nil
}

// RecoveryResult contains the results of a recovery operation
type RecoveryResult struct {
	TotalFound     int      `json:"total_found"`
	StillRunning   int      `json:"still_running"`
	MarkedStale    int      `json:"marked_stale"`
	StaleProcesses []string `json:"stale_processes,omitempty"`
}
