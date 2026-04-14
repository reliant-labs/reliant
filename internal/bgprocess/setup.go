// Copyright (c) 2025 Reliant Labs
package bgprocess

import (
	"context"
	"encoding/json"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// SetupPersistence wires up the BackgroundManager to persist
// process state to the database. This enables recovery after server restarts.
func SetupPersistence(database db.Repository) {
	bgManager := shell.GetBackgroundManager()
	// Allow BackgroundManager to perform DB-backed operations (e.g., kill recovered processes by PID)
	bgManager.SetRepository(database)

	bgManager.SetPersistenceCallback(&shell.PersistenceCallback{
		OnCreate: func(process *shell.BackgroundProcess) error {
			ctx := context.Background()

			// We need a user ID to persist. Try to get it from chat or worktree.
			var userID string
			var projectID *string

			if process.ChatID != "" {
				chat, err := database.GetChat(ctx, process.ChatID)
				if err != nil {
					logging.Warn("Failed to get chat for process persistence, skipping",
						"processID", process.ID,
						"chatID", process.ChatID,
						"error", err)
					return shell.ErrPersistenceSkipped
				}
				userID = chat.UserID
				projectID = &chat.ProjectID
			} else if process.WorktreeID != "" {
				worktree, err := database.GetWorktree(ctx, process.WorktreeID)
				if err != nil {
					logging.Warn("Failed to get worktree for process persistence, skipping",
						"processID", process.ID,
						"worktreeID", process.WorktreeID,
						"error", err)
					return shell.ErrPersistenceSkipped
				}
				project, err := database.GetProject(ctx, worktree.ProjectID)
				if err != nil {
					logging.Warn("Failed to get project for process persistence, skipping",
						"processID", process.ID,
						"projectID", worktree.ProjectID,
						"error", err)
					return shell.ErrPersistenceSkipped
				}
				userID = project.UserID
				projectID = &worktree.ProjectID
			} else {
				// No chat or worktree, skip persistence
				logging.Debug("Process has no chat or worktree, skipping persistence",
					"processID", process.ID)
				return shell.ErrPersistenceSkipped
			}

			// Get PID for signature
			var pid *int
			var signature *string
			if process.GetPID() > 0 {
				p := process.GetPID()
				pid = &p
				sig := pkgmgr.GenerateProcessSignature(process.Command, process.StartTime)
				signature = &sig
			}

			// Convert to DB model
			var worktreeID, chatID *string
			if process.WorktreeID != "" {
				worktreeID = &process.WorktreeID
			}
			if process.ChatID != "" {
				chatID = &process.ChatID
			}

			dbProcess := &db.BackgroundProcess{
				ID:         process.ID,
				PID:        pid,
				Command:    process.Command,
				WorkingDir: process.WorkingDir,
				WorktreeID: worktreeID,
				ProjectID:  projectID,
				ChatID:     chatID,
				UserID:     userID,
				Status:     db.BgProcessStatusRunning,
				StartedAt:  process.StartTime,
				Signature:  signature,
				SourceType: db.BgProcessSourceLLM, // Default to LLM, could be enhanced
			}

			if err := database.CreateBackgroundProcess(ctx, dbProcess); err != nil {
				logging.Error("Failed to persist process to database",
					"processID", process.ID,
					"error", err)
				return err
			}

			logging.Debug("Persisted process to database",
				"processID", process.ID,
				"userID", userID)
			return nil
		},

		OnStatusChange: func(processID string, status string, exitCode *int, endTime *time.Time) error {
			ctx := context.Background()

			// Convert status string to DB status type
			var dbStatus db.BackgroundProcessStatus
			switch status {
			case "completed":
				dbStatus = db.BgProcessStatusCompleted
			case "failed":
				dbStatus = db.BgProcessStatusFailed
			case "killed":
				dbStatus = db.BgProcessStatusKilled
			case "killed_externally":
				dbStatus = db.BgProcessStatusKilledExternally
			default:
				dbStatus = db.BgProcessStatusFailed // unknown status treated as failed
			}

			if err := database.UpdateBackgroundProcessStatus(ctx, processID, dbStatus, exitCode, endTime); err != nil {
				logging.Error("Failed to update process status in database",
					"processID", processID,
					"status", status,
					"error", err)
				return err
			}

			logging.Debug("Updated process status in database",
				"processID", processID,
				"status", status)
			return nil
		},

		OnOutput: func(processID string, lines []shell.OutputLineWithSeq) error {
			dbLines := make([]db.BackgroundProcessOutputLine, len(lines))
			for i, line := range lines {
				dbLines[i] = db.BackgroundProcessOutputLine{
					ProcessID: processID,
					Seq:       line.Sequence,
					Stream:    line.Type,
					Line:      line.Text,
				}
			}
			return database.CreateBackgroundProcessOutputBatch(context.Background(), dbLines)
		},
	})

	logging.Info("Background process persistence configured")
}

// SetupEvents wires up the background process event callback
// to publish events to the user_updates table for real-time streaming delivery.
func SetupEvents(database db.Repository) {
	bgManager := shell.GetBackgroundManager()

	// Start the process monitor to detect port changes and external kills
	processMonitor := shell.GetProcessMonitor()
	processMonitor.Start()
	logging.Info("Started process monitor for port change detection")

	bgManager.SetEventCallback(func(event shell.ProcessEvent) {
		ctx := context.Background()

		// We need a user ID to publish to user_updates
		// Try to get it from chat first, then worktree
		var userID, projectID string
		var chatIDPtr, worktreeIDPtr *string

		if event.ChatID != "" {
			// Look up chat to get user ID and project ID
			chat, err := database.GetChat(ctx, event.ChatID)
			if err != nil {
				logging.Error("Failed to get chat for background process event",
					"error", err,
					"chatID", event.ChatID,
					"processID", event.ProcessID)
				return
			}
			userID = chat.UserID
			projectID = chat.ProjectID
			chatIDPtr = &event.ChatID
		} else if event.WorktreeID != "" {
			// Look up worktree to get project, then project to get user
			worktree, err := database.GetWorktree(ctx, event.WorktreeID)
			if err != nil {
				logging.Error("Failed to get worktree for background process event",
					"error", err,
					"worktreeID", event.WorktreeID,
					"processID", event.ProcessID)
				return
			}
			projectID = worktree.ProjectID

			// Get project to find user ID
			project, err := database.GetProject(ctx, worktree.ProjectID)
			if err != nil {
				logging.Error("Failed to get project for background process event",
					"error", err,
					"projectID", worktree.ProjectID,
					"processID", event.ProcessID)
				return
			}
			userID = project.UserID
			worktreeIDPtr = &event.WorktreeID
		} else {
			// No chat or worktree, skip
			logging.Debug("Background process event without chat or worktree, skipping user_update",
				"processID", event.ProcessID,
				"type", event.Type)
			return
		}

		// Determine update type based on event type
		var updateType db.UserUpdateType
		switch event.Type {
		case "started":
			updateType = db.UserUpdateProcessStarted
		case "completed":
			updateType = db.UserUpdateProcessCompleted
		case "failed", "killed":
			updateType = db.UserUpdateProcessFailed
		case "port_changed":
			updateType = db.UserUpdateProcessPortChanged
		default:
			logging.Warn("Unknown background process event type",
				"type", event.Type,
				"processID", event.ProcessID)
			return
		}

		// Build the data payload with full process information
		dataMap := map[string]interface{}{
			"process_id":  event.ProcessID,
			"command":     event.Command,
			"working_dir": event.WorkingDir,
			"status":      event.Status,
			"start_time":  event.StartTime,
			"session_id":  event.SessionID,
		}
		if event.ExitCode != nil {
			dataMap["exit_code"] = *event.ExitCode
		}
		if event.EndTime != nil {
			dataMap["end_time"] = *event.EndTime
		}
		if event.WorktreeID != "" {
			dataMap["worktree_id"] = event.WorktreeID
		}
		if event.ChatID != "" {
			dataMap["chat_id"] = event.ChatID
		}
		// Include port information for running processes
		if len(event.Ports) > 0 {
			ports := make([]map[string]interface{}, len(event.Ports))
			for i, p := range event.Ports {
				ports[i] = map[string]interface{}{
					"port":     p.Port,
					"protocol": p.Protocol,
					"state":    p.State,
					"address":  p.Address,
				}
			}
			dataMap["ports"] = ports
		}

		dataJSON, err := json.Marshal(dataMap)
		if err != nil {
			logging.Error("Failed to marshal background process event data",
				"error", err,
				"processID", event.ProcessID)
			return
		}

		// Create the user update
		userUpdate := &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &projectID,
			ChatID:     chatIDPtr,
			WorktreeID: worktreeIDPtr,
			UpdateType: updateType,
			EntityType: db.EntityTypeBackgroundProcess,
			EntityID:   event.ProcessID,
			Data:       dataJSON,
		}

		// Add worktree ID if not already set but available in event
		if userUpdate.WorktreeID == nil && event.WorktreeID != "" {
			userUpdate.WorktreeID = &event.WorktreeID
		}

		if err := database.CreateUserUpdate(ctx, userUpdate); err != nil {
			logging.Error("Failed to create user update for background process event",
				"error", err,
				"processID", event.ProcessID,
				"type", event.Type)
			return
		}

		logging.Info("Published background process event",
			"processID", event.ProcessID,
			"type", event.Type,
			"worktreeID", event.WorktreeID,
			"chatID", event.ChatID,
			"userID", userID)
	})
}

// RecoverProcesses loads running processes from the database after
// validating they are still alive. This is called on server startup.
func RecoverProcesses(database db.Repository) {
	ctx := context.Background()

	// First, run recovery to validate and mark stale processes
	recoveryService := pkgmgr.NewRecoveryService(database)
	result, err := recoveryService.RecoverProcesses(ctx)
	if err != nil {
		logging.Error("Failed to recover background processes", "error", err)
		return
	}

	if result.TotalFound > 0 {
		logging.Info("Process recovery completed",
			"total", result.TotalFound,
			"stillRunning", result.StillRunning,
			"markedStale", result.MarkedStale)
	}

	// Now load still-running processes into the BackgroundManager
	processes, err := database.GetRunningBackgroundProcesses(ctx)
	if err != nil {
		logging.Error("Failed to get running processes from database", "error", err)
		return
	}

	bgManager := shell.GetBackgroundManager()
	loadedCount := 0

	for _, process := range processes {
		// Skip if no PID (shouldn't happen after recovery, but be safe)
		if process.PID == nil {
			continue
		}

		// Convert DB model to in-memory representation
		worktreeID := ""
		if process.WorktreeID != nil {
			worktreeID = *process.WorktreeID
		}
		chatID := ""
		if process.ChatID != nil {
			chatID = *process.ChatID
		}

		bgManager.LoadProcess(
			process.ID,
			process.Command,
			process.WorkingDir,
			worktreeID,
			worktreeID, // sessionID: use worktreeID for grouping consistency (matches pkgmgr.Service)
			chatID,
			db.BgProcessStatusToString(process.Status),
			process.StartedAt,
			*process.PID,
		)
		loadedCount++
	}

	if loadedCount > 0 {
		logging.Info("Loaded running processes into BackgroundManager",
			"count", loadedCount)
	}
}
