// Copyright (c) 2025 Reliant Labs
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/reliant-labs/reliant/internal/api/handlers"
	apimiddleware "github.com/reliant-labs/reliant/internal/api/middleware"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// Server is the HTTP API server
type Server struct {
	server      *http.Server
	handlers    *handlers.Handlers
	tlsCertFile string
	tlsKeyFile  string
}

// Config holds API server configuration
type Config struct {
	Port               int
	BindAddress        string   // Bind address (default: "127.0.0.1", use "0.0.0.0" for containers)
	JWTPublicKey       string   // Required RSA public key for Supabase JWT authentication
	CORSAllowedOrigins []string // Allowed CORS origins; defaults to ["*"] if empty

	// TLS configuration for HTTPS support
	TLSCertFile string // Path to TLS certificate file
	TLSKeyFile  string // Path to TLS private key file

	// ManagesLocalProcesses indicates whether this server manages local background
	// processes (shell commands, process monitor). Should be true in full/monolith
	// mode, false in stateless API mode.
	ManagesLocalProcesses bool

	NATSChecker func() bool // Optional: returns true if NATS is connected; nil means NATS not in use
}

// NewServer creates a new API server
func NewServer(cfg *Config, database db.Repository, dataDir string) *Server {
	// Require JWT public key
	if cfg.JWTPublicKey == "" {
		panic("JWT public key is required - create .supabase-public-key.pem file or update embedded key in internal/v2/auth/keys.go")
	}

	h := handlers.New()

	// Create auth middleware
	authMiddleware, err := auth.NewMiddleware(cfg.JWTPublicKey)
	if err != nil {
		panic(fmt.Sprintf("failed to create auth middleware: %v", err))
	}

	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(apimiddleware.SentryRecoverer) // Custom recoverer that reports panics to Sentry
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(middleware.StripSlashes) // Handle both /path and /path/ the same way

	// Rate limiting - 100 requests per minute per IP to prevent abuse
	r.Use(httprate.LimitByIP(100, 1*time.Minute))

	// Security headers middleware
	r.Use(securityHeaders)

	// CORS - MaxAge of 24 hours to cache preflight responses and reduce OPTIONS requests
	corsOrigins := cfg.CORSAllowedOrigins
	if len(corsOrigins) == 0 {
		corsOrigins = []string{"*"}
	}
	allowCreds := true
	for _, o := range corsOrigins {
		if o == "*" {
			allowCreds = false
			break
		}
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: allowCreds,
		MaxAge:           86400,
	}))

	// V2 API routes
	r.Route("/api/v2", func(r chi.Router) {
		// Register modular handlers with automatic route registration
		registry := handlers.NewHandlerRegistry(r, authMiddleware)
		registry.RegisterAll(
			handlers.NewSystemHandler(database, cfg.NATSChecker),
			handlers.NewWindowStateHandler(dataDir),
			handlers.NewFilePreviewHandler(database),
		)
	})

	// Wire up background process persistence and event publishing (only when managing local processes)
	if cfg.ManagesLocalProcesses {
		setupBackgroundProcessPersistence(database)
		setupBackgroundProcessEvents(database)

		// Recover processes from database and load into BackgroundManager
		recoverBackgroundProcesses(database)
	}

	bindAddr := cfg.BindAddress
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return &Server{
		server:      srv,
		handlers:    h,
		tlsCertFile: cfg.TLSCertFile,
		tlsKeyFile:  cfg.TLSKeyFile,
	}
}

// Start starts the API server
func (s *Server) Start() error {
	if s.tlsCertFile != "" && s.tlsKeyFile != "" {
		logging.Info("Starting HTTPS API server with TLS", "address", s.server.Addr)
		go func() {
			if err := s.server.ListenAndServeTLS(s.tlsCertFile, s.tlsKeyFile); err != nil && err != http.ErrServerClosed {
				logging.Error("HTTPS server failed", "error", err)
			}
		}()
	} else {
		logging.Info("Starting HTTP API server (no TLS)", "address", s.server.Addr)
		go func() {
			if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logging.Error("HTTP server failed", "error", err)
			}
		}()
	}

	return nil
}

// IsTLS returns whether the server is configured to use TLS
func (s *Server) IsTLS() bool {
	return s.tlsCertFile != "" && s.tlsKeyFile != ""
}

// Stop gracefully stops the API server
func (s *Server) Stop(ctx context.Context) error {
	logging.Info("Stopping HTTP API server")
	return s.server.Shutdown(ctx)
}

// setupBackgroundProcessEvents wires up the background process event callback
// to publish events to the user_updates table for real-time streaming delivery
func setupBackgroundProcessEvents(database db.Repository) {
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

// setupBackgroundProcessPersistence wires up the BackgroundManager to persist
// process state to the database. This enables recovery after server restarts.
func setupBackgroundProcessPersistence(database db.Repository) {
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

// recoverBackgroundProcesses loads running processes from the database after
// validating they are still alive. This is called on server startup.
func recoverBackgroundProcesses(database db.Repository) {
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

// securityHeaders adds security-related HTTP headers to responses
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")
		// Enable XSS filter in browsers
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
