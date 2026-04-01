// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Service provides package management operations
type Service struct {
	adapters   []Adapter
	bgManager  *shell.BackgroundManager
	adaptersMu sync.RWMutex
}

// NewService creates a new package manager service with default adapters
func NewService() *Service {
	return &Service{
		adapters: []Adapter{
			NewMakefileAdapter(),
			NewNPMAdapter(),
			NewTaskfileAdapter(),
		},
		bgManager: shell.GetBackgroundManager(),
	}
}

// NewServiceWithAdapters creates a service with custom adapters
func NewServiceWithAdapters(adapters []Adapter) *Service {
	return &Service{
		adapters:  adapters,
		bgManager: shell.GetBackgroundManager(),
	}
}

// ListCommands discovers all available commands in the given directory and subdirectories.
// It automatically walks the directory tree to find all package.json, Makefile, and Taskfile files,
// skipping common non-project directories like node_modules, .git, dist, etc.
func (s *Service) ListCommands(ctx context.Context, rootDir string) (*CommandListResponse, error) {
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()

	response := &CommandListResponse{
		Commands:      make(map[PackageType][]Command),
		DetectedTypes: []PackageType{},
	}

	// Discover all directories containing package files
	opts := DefaultDiscoveryOptions()
	dirs, err := DiscoverDirectories(ctx, rootDir, opts)
	if err != nil {
		logging.Error("Error discovering directories",
			"root", rootDir,
			"error", err)
		// Fall back to just root directory
		dirs = []string{rootDir}
	}

	detectedTypesSet := make(map[PackageType]bool)

	// Search each directory
	for _, dir := range dirs {
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		default:
		}

		// Calculate relative path for display
		relativePath := ""
		if dir != rootDir {
			rel, err := filepath.Rel(rootDir, dir)
			if err == nil {
				relativePath = rel
			}
		}

		for _, adapter := range s.adapters {
			detected, err := adapter.Detect(ctx, dir)
			if err != nil {
				continue
			}

			if !detected {
				continue
			}

			detectedTypesSet[adapter.Type()] = true

			commands, err := adapter.Parse(ctx, dir)
			if err != nil {
				logging.Error("Error parsing package file",
					"type", adapter.Type(),
					"dir", dir,
					"error", err)
				continue
			}

			// Set WorkingDir and RelativePath on each command
			for i := range commands {
				commands[i].WorkingDir = dir
				commands[i].RelativePath = relativePath
			}

			// Append to existing commands for this type
			response.Commands[adapter.Type()] = append(response.Commands[adapter.Type()], commands...)
		}
	}

	// Convert detected types set to slice
	for t := range detectedTypesSet {
		response.DetectedTypes = append(response.DetectedTypes, t)
	}

	// Sort commands: by relative path first, then by name
	for pkgType := range response.Commands {
		sort.Slice(response.Commands[pkgType], func(i, j int) bool {
			ci, cj := response.Commands[pkgType][i], response.Commands[pkgType][j]
			// Root commands (empty RelativePath) come first
			if ci.RelativePath != cj.RelativePath {
				if ci.RelativePath == "" {
					return true
				}
				if cj.RelativePath == "" {
					return false
				}
				return ci.RelativePath < cj.RelativePath
			}
			return ci.Name < cj.Name
		})
	}

	return response, nil
}

// GetCommand finds a specific command by name and package type
func (s *Service) GetCommand(ctx context.Context, dir string, packageType PackageType, name string) (*Command, error) {
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()

	for _, adapter := range s.adapters {
		if adapter.Type() != packageType {
			continue
		}

		detected, err := adapter.Detect(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("failed to detect %s: %w", packageType, err)
		}

		if !detected {
			return nil, fmt.Errorf("%s not found in %s", packageType, dir)
		}

		commands, err := adapter.Parse(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", packageType, err)
		}

		for _, cmd := range commands {
			if cmd.Name == name {
				return &cmd, nil
			}
		}

		return nil, fmt.Errorf("command %q not found in %s", name, packageType)
	}

	return nil, fmt.Errorf("unknown package type: %s", packageType)
}

// RunCommand executes a package command as a background process
func (s *Service) RunCommand(ctx context.Context, req RunRequest) (*RunResponse, error) {
	// Find the command
	cmd, err := s.GetCommand(ctx, req.WorkingDir, req.PackageType, req.CommandName)
	if err != nil {
		return nil, err
	}

	// Start the background process
	process, err := s.bgManager.StartProcess(ctx, shell.StartProcessOptions{
		Command:    cmd.Command,
		WorkingDir: req.WorkingDir,
		WorktreeID: req.WorktreeID,
		SessionID:  req.WorktreeID, // Use worktree ID as session for grouping
		ChatID:     "",             // Not associated with a chat
		Env:        req.Env,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start process: %w", err)
	}

	return &RunResponse{
		ProcessID: process.ID,
		Command:   cmd.Command,
		StartTime: process.StartTime,
	}, nil
}

// GetProcessesByWorktree returns all processes for a worktree
func (s *Service) GetProcessesByWorktree(worktreeID string) []*shell.BackgroundProcess {
	return s.bgManager.GetProcessesByWorktree(worktreeID)
}

// GetAllProcesses returns all processes across all worktrees
func (s *Service) GetAllProcesses() []*shell.BackgroundProcess {
	return s.bgManager.GetAllProcesses()
}

// GetProcess returns a specific process by ID
func (s *Service) GetProcess(processID string) (*shell.BackgroundProcess, error) {
	return s.bgManager.GetProcess(processID)
}

// GetProcessOutput returns the output for a process (separate stdout/stderr)
func (s *Service) GetProcessOutput(processID string) (stdout, stderr string, err error) {
	return s.bgManager.GetOutput(processID)
}

// GetCombinedOutput returns the interleaved output for a process
func (s *Service) GetCombinedOutput(processID string) ([]shell.OutputLine, error) {
	return s.bgManager.GetCombinedOutput(processID)
}

// KillProcess terminates a background process
func (s *Service) KillProcess(processID string) error {
	return s.bgManager.KillProcess(processID)
}

// DetectedPackageTypes returns which package types exist in a directory
func (s *Service) DetectedPackageTypes(ctx context.Context, dir string) ([]PackageType, error) {
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()

	var types []PackageType
	for _, adapter := range s.adapters {
		detected, err := adapter.Detect(ctx, dir)
		if err != nil {
			continue
		}
		if detected {
			types = append(types, adapter.Type())
		}
	}
	return types, nil
}
