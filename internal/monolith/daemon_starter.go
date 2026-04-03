// Copyright (c) 2025 Reliant Labs
package monolith

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/pat"
	"github.com/reliant-labs/reliant/internal/patauth"
	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// LazyDaemonStarter manages the in-process daemon lifecycle for monolith mode.
// It can start the daemon eagerly at boot (if a user is already logged in) or
// lazily on the first authenticated request (first-time users who haven't logged in yet).
type LazyDaemonStarter struct {
	mu sync.Mutex

	// Configuration (set once at construction, read-only after)
	repo                    db.Repository
	sharedToolsDaemonSvc    *services.ToolsDaemonService
	remoteToolExecutor      *toolexec.RemoteExecutor
	tlsCertFile, tlsKeyFile string
	toolsDaemonPort         int
	dataDir                 string

	// Runtime state (protected by mu)
	started      bool
	userID       string
	daemonSrv    *grpcserver.DaemonServer
	daemonCancel context.CancelFunc
}

// LazyDaemonStarterConfig holds the static configuration needed to start the in-process daemon.
type LazyDaemonStarterConfig struct {
	Repo                    db.Repository
	SharedToolsDaemonSvc    *services.ToolsDaemonService
	RemoteToolExecutor      *toolexec.RemoteExecutor
	TLSCertFile, TLSKeyFile string
	ToolsDaemonPort         int
	DataDir                 string
}

// NewLazyDaemonStarter creates a starter that is ready to launch the daemon on demand.
func NewLazyDaemonStarter(cfg LazyDaemonStarterConfig) *LazyDaemonStarter {
	return &LazyDaemonStarter{
		repo:                 cfg.Repo,
		sharedToolsDaemonSvc: cfg.SharedToolsDaemonSvc,
		remoteToolExecutor:   cfg.RemoteToolExecutor,
		tlsCertFile:          cfg.TLSCertFile,
		tlsKeyFile:           cfg.TLSKeyFile,
		toolsDaemonPort:      cfg.ToolsDaemonPort,
		dataDir:              cfg.DataDir,
	}
}

// EnsureStarted starts the in-process daemon for the given user if it hasn't been started yet.
// Safe to call concurrently; only the first call with a non-empty userID takes effect.
// Returns true if the daemon was started by this call, false if already running.
func (s *LazyDaemonStarter) EnsureStarted(userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return false, nil
	}

	if err := s.startLocked(userID); err != nil {
		return false, err
	}
	return true, nil
}

// startLocked does the actual daemon startup. Caller must hold s.mu.
func (s *LazyDaemonStarter) startLocked(userID string) error {
	logging.Info("Starting in-process daemon (lazy)", "userID", userID)

	// Preload skills catalog for known projects
	var projectPaths []string
	projects, listErr := s.repo.ListProjects(context.Background(), db.ProjectFilters{UserID: userID, Limit: 100000, Offset: 0})
	if listErr != nil {
		logging.Warn("Failed listing projects for startup skills catalog preload", "error", listErr, "user_id", userID)
	} else {
		for _, project := range projects {
			if project == nil || strings.TrimSpace(project.Path) == "" {
				continue
			}
			projectPaths = append(projectPaths, project.Path)
		}
	}
	skillcatalog.DefaultCatalogIndex().PreloadProjects(context.Background(), projectPaths)
	logging.Info("Preloaded skills catalog snapshots", "project_count", len(projectPaths))

	// Create ephemeral PAT for in-process daemon
	patService := pat.NewService(s.repo)
	rawPAT, _, err := patService.CreatePAT(context.Background(), userID, "monolith-in-process-daemon", true, nil)
	if err != nil {
		return fmt.Errorf("create ephemeral PAT: %w", err)
	}
	logging.Info("Created ephemeral PAT for in-process daemon", "userID", userID)

	// Create PAT validator for daemon auth
	patValidator := patauth.NewDBPATValidator(s.repo)

	s.daemonSrv = grpcserver.NewDaemonServer(&grpcserver.DaemonConfig{
		Port:               s.toolsDaemonPort,
		ToolsDaemonService: s.sharedToolsDaemonSvc,
		ToolExecutor:       s.remoteToolExecutor,
		PATValidator:       patValidator,
		TLSCertFile:        s.tlsCertFile,
		TLSKeyFile:         s.tlsKeyFile,
	})
	if err := s.daemonSrv.Start(); err != nil {
		return fmt.Errorf("start dedicated tools-daemon gRPC server: %w", err)
	}

	daemonURL := fmt.Sprintf("http://127.0.0.1:%d", s.toolsDaemonPort)
	daemonTLSMode := bootstrap.TLSModeH2C
	if s.daemonSrv.IsTLS() {
		daemonURL = fmt.Sprintf("https://127.0.0.1:%d", s.toolsDaemonPort)
		daemonTLSMode = bootstrap.TLSModeInsecureTLSSkipVerify
	}

	logging.Info("Daemon runtime bootstrap config",
		"daemonURL", daemonURL,
		"tlsMode", string(daemonTLSMode),
		"serverIsTLS", s.daemonSrv.IsTLS(),
		"port", s.toolsDaemonPort,
	)

	daemonCtx, cancel := context.WithCancel(context.Background())
	s.daemonCancel = cancel

	go func() {
		err := daemonruntime.Start(daemonCtx, daemonruntime.StartOptions{
			BootstrapConfig: bootstrap.DaemonBootstrapConfig{
				UserID:    userID,
				AuthToken: rawPAT,
				GRPCURL:   daemonURL,
				TLSMode:   daemonTLSMode,
				DataDir:   s.dataDir,
			},
		})
		if err != nil && err != context.Canceled {
			logging.Error("In-process daemon runtime exited", "error", err)
		}
	}()

	s.started = true
	s.userID = userID
	logging.Info("In-process daemon started", "userID", userID)
	return nil
}

// Shutdown stops the daemon and revokes its ephemeral PATs.
func (s *LazyDaemonStarter) Shutdown(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		logging.Info("Dedicated tools-daemon gRPC server was not started (signed-out mode)")
		return
	}

	// Cancel daemon runtime context
	if s.daemonCancel != nil {
		s.daemonCancel()
	}

	// Revoke ephemeral PATs
	if s.userID != "" {
		revokeCtx, revokeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		patService := pat.NewService(s.repo)
		if err := patService.RevokeEphemeralPATs(revokeCtx, s.userID); err != nil {
			logging.Warn("Failed to revoke ephemeral PATs", "error", err)
		}
		revokeCancel()
	}

	// Stop daemon gRPC server
	if s.daemonSrv != nil {
		logging.Info("Stopping dedicated tools-daemon gRPC server")
		if err := s.daemonSrv.Stop(ctx); err != nil {
			logging.Error("Error stopping dedicated tools-daemon gRPC server", "error", err)
		} else {
			logging.Info("Dedicated tools-daemon gRPC server stopped successfully")
		}
	}
}

// Started returns whether the daemon has been started.
func (s *LazyDaemonStarter) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// UserID returns the user ID the daemon was started for, or empty if not started.
func (s *LazyDaemonStarter) UserID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userID
}
